// Package password implements `zipgo password`: put a site behind zipgo's
// login page, rotate its password, check it, take it off. It edits the
// "password" block of the site's .zipgoconfig.json — on the deploy target over
// SSH (resolved the way `zipgo deploy` resolves it), or in a local domains
// folder with --dir — and leaves every other key alone.
//
// The password itself never leaves this machine: it is bcrypt-hashed here and
// only the hash is written. zipgo hot-reloads the config, so a set, a rotation
// or a removal takes effect within a second, and rotating logs every open
// session out.
package password

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"zipgo/internal/deploy"
	"zipgo/internal/sites"
)

// Usage is the `zipgo password` help text.
const Usage = `Usage: zipgo password set <host> [--title "…"] [--days N] [--stdin | --generate]
       zipgo password status [host] [--json]
       zipgo password remove <host> [--yes]
       zipgo password hash [--stdin]

Put a whole site behind zipgo's login page: one shared password, a session
cookie, nothing of the site served before it. Only a bcrypt hash is stored, in
the "password" block of the site's .zipgoconfig.json; zipgo hot-reloads it.

  set <host>       lock the site, or change its password (alias: rotate).
                   Prompts twice, without echo. Changing the password logs
                   everyone out. The site folder is created if needed, so a
                   site can be locked before its first deploy.
      --title T    the login page's heading (default: kept, else the host)
      --days N     how long a login lasts without a visit (default: kept,
                   else 7); a session in use is renewed
      --stdin      read the password from stdin instead of prompting
      --generate   make up a password and print it once
      --keep-password  change --title/--days only; sessions stay valid
  status [host]    is the site locked, with which title and session length;
                   without a host, every locked site on the target
      --json       machine-readable output
  remove <host>    take the password off: the site becomes public
      --yes        don't ask for confirmation
  hash             print the bcrypt hash of a password, for a hand-written
                   .zipgoconfig.json

Where (every command but hash):
      --ssh, --target user@host:/base   the zipgo host, like zipgo deploy
                   (default: package.json "zipgo" target, then .zipgo.json)
      --dir <folder>  a local domains folder instead (e.g. on the server)

Deploys keep the lock: zipgo deploy never removes or overwrites a site's
password, even when the build ships its own .zipgoconfig.json.

Examples:
  zipgo password set ballance.game.gabvdl.xyz --title "Ballance"
  echo -n "$PW" | zipgo password set spore.gabvdl.xyz --stdin
  zipgo password status
  zipgo deploy dist -d ballance.game.gabvdl.xyz     # still locked
`

// Cost is the bcrypt cost of the hashes `set` writes: ~0.3 s per login check
// on a Raspberry Pi 5, which is only paid on a login — sessions are checked
// with an HMAC.
const Cost = 12

// Options is a parsed `zipgo password` invocation.
type Options struct {
	Verb         string // set | status | remove | hash
	Host         string
	Title        *string
	Days         int
	Stdin        bool
	Generate     bool
	KeepPassword bool
	Yes          bool
	JSON         bool
	SSH          string
	Dir          string
	// Scope is `zipgo admin set`'s --all / --scope: "" keeps the
	// back-office's scope (a new one manages its site), else site or all.
	Scope sites.AdminScope
}

// ParseArgs parses everything after `zipgo password`.
func ParseArgs(args []string) (Options, error) {
	o, err := parseArgs(args)
	if err != nil {
		return o, err
	}
	return o, o.validate()
}

// parseArgs is ParseArgs without the final validation, which `zipgo admin`
// runs once it has added its own flags.
func parseArgs(args []string) (Options, error) {
	var o Options
	if len(args) == 0 {
		return o, errors.New("missing command: set, status, remove or hash")
	}
	switch v := args[0]; v {
	case "set", "rotate":
		o.Verb = "set"
	case "status", "show-status", "ls":
		o.Verb = "status"
	case "remove", "rm", "unset":
		o.Verb = "remove"
	case "hash":
		o.Verb = "hash"
	default:
		return o, fmt.Errorf("unknown command %q: use set, status, remove or hash", v)
	}
	args = args[1:]
	value := func(i *int, flag string) (string, error) {
		if k, v, ok := strings.Cut(args[*i], "="); ok && strings.HasPrefix(k, "--") {
			return v, nil
		}
		*i++
		if *i >= len(args) {
			return "", fmt.Errorf("%s requires a value", flag)
		}
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, _, _ := strings.Cut(a, "=")
		var err error
		switch flag {
		case "--title":
			var t string
			if t, err = value(&i, flag); err == nil {
				o.Title = &t
			}
		case "--days":
			var d string
			if d, err = value(&i, flag); err == nil {
				o.Days, err = strconv.Atoi(d)
				if err != nil || o.Days < 1 || o.Days > sites.MaxSessionDays {
					err = fmt.Errorf("--days must be a number from 1 to %d", sites.MaxSessionDays)
				}
			}
		case "--ssh", "--target":
			o.SSH, err = value(&i, flag)
		case "--dir":
			o.Dir, err = value(&i, flag)
		case "--stdin":
			o.Stdin = true
		case "--generate":
			o.Generate = true
		case "--keep-password":
			o.KeepPassword = true
		case "--yes", "-y":
			o.Yes = true
		case "--json":
			o.JSON = true
		default:
			if strings.HasPrefix(a, "-") {
				return o, fmt.Errorf("unknown flag %q", a)
			}
			if o.Host != "" {
				return o, fmt.Errorf("unexpected argument %q", a)
			}
			o.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a)), ".")
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func (o Options) validate() error {
	sources := 0
	for _, b := range []bool{o.Stdin, o.Generate, o.KeepPassword} {
		if b {
			sources++
		}
	}
	switch {
	case (o.Verb == "set" || o.Verb == "remove") && o.Host == "":
		return fmt.Errorf("%s needs a host, e.g. zipgo password %s app.example.com", o.Verb, o.Verb)
	case o.Verb == "hash" && o.Host != "":
		return errors.New("hash takes no host")
	case sources > 1:
		return errors.New("pick one of --stdin, --generate and --keep-password")
	case o.Verb != "set" && (o.Title != nil || o.Days != 0 || o.Generate || o.KeepPassword):
		return errors.New("--title, --days, --generate and --keep-password only go with set")
	case o.Verb == "set" && o.KeepPassword && o.Title == nil && o.Days == 0 && o.Scope == "":
		return errors.New("--keep-password changes --title or --days: pass one")
	case o.SSH != "" && o.Dir != "":
		return errors.New("--ssh and --dir are exclusive")
	}
	return nil
}

// ---- where configs live --------------------------------------------------------

// Store reads and writes a site's .zipgoconfig.json.
type Store interface {
	// Read returns the file's content, and whether it exists.
	Read(host string) ([]byte, bool, error)
	// Write replaces it atomically, creating the site folder if needed.
	Write(host string, data []byte) error
	// Path is where the file is, for messages.
	Path(host string) (string, error)
}

// Local is a domains folder on this machine.
type Local struct{ Base string }

func (l Local) Path(host string) (string, error) {
	dir, err := deploy.RemoteDir(l.Base, host)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sites.ConfigFileName), nil
}

func (l Local) Read(host string) ([]byte, bool, error) {
	p, err := l.Path(host)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// Write goes through a temporary file in the domains root — same filesystem,
// but outside every site, so it is never served — and a rename, so zipgo's
// watcher never reads half a config.
func (l Local) Write(host string, data []byte) error {
	p, err := l.Path(host)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(l.Base, ".zipgo-tmp.*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// Remote is a zipgo host reached over SSH.
type Remote struct{ Target deploy.Target }

func (r Remote) Path(host string) (string, error) {
	dir, err := deploy.RemoteDir(r.Target.BaseDir, host)
	if err != nil {
		return "", err
	}
	return dir + "/" + sites.ConfigFileName, nil
}

func (r Remote) Read(host string) ([]byte, bool, error) {
	return deploy.ReadConfig(r.Target, host)
}

func (r Remote) Write(host string, data []byte) error {
	dir, err := deploy.RemoteDir(r.Target.BaseDir, host)
	if err != nil {
		return err
	}
	return deploy.WriteConfig(r.Target, dir, data)
}

// ---- config edits ----------------------------------------------------------------

// readPassword returns the site's current password block (nil when it has
// none) and the raw config it came from.
func readPassword(raw []byte) (*sites.Password, map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil, fmt.Errorf("%s is not valid JSON: %w", sites.ConfigFileName, err)
		}
	}
	v, ok := m["password"]
	if !ok || string(v) == "null" {
		return nil, m, nil
	}
	var p sites.Password
	if err := json.Unmarshal(v, &p); err != nil {
		return nil, nil, fmt.Errorf("its \"password\" block is malformed: %w", err)
	}
	return &p, m, nil
}

// SetPassword returns raw with its "password" block replaced by p, every other
// key untouched.
func SetPassword(raw []byte, p *sites.Password) ([]byte, error) {
	_, m, err := readPassword(raw)
	if err != nil {
		return nil, err
	}
	if p == nil {
		delete(m, "password")
	} else {
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		m["password"] = b
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ---- reading the password ----------------------------------------------------------

// Hash bcrypt-hashes a password at Cost.
func Hash(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("empty password")
	}
	if len(pw) > 72 {
		return "", errors.New("a password can be 72 bytes at most (bcrypt ignores the rest)")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), Cost)
	return string(h), err
}

// fromStdin reads the password as the first line of r.
func fromStdin(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("no password on stdin")
	}
	return pw, nil
}

// prompt asks for a new password twice on the terminal, without echo.
func prompt(label string, confirm bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("no terminal to prompt on: pipe the password with --stdin, or use --generate")
	}
	read := func(q string) (string, error) {
		fmt.Fprint(os.Stderr, q)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	pw, err := read(label)
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", errors.New("empty password")
	}
	if confirm {
		again, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			return "", errors.New("the two passwords differ")
		}
	}
	return pw, nil
}

// passphraseAlphabet leaves out the characters people misread or mistype on a
// phone keyboard (0/o, 1/l/i).
const passphraseAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// Generate makes up a password: four groups of four characters, ~79 bits.
func Generate() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(passphraseAlphabet)))
	for g := 0; g < 4; g++ {
		if g > 0 {
			b.WriteByte('-')
		}
		for i := 0; i < 4; i++ {
			k, err := rand.Int(rand.Reader, n)
			if err != nil {
				return "", err
			}
			b.WriteByte(passphraseAlphabet[k.Int64()])
		}
	}
	return b.String(), nil
}

// ---- commands ------------------------------------------------------------------------

// Run executes a parsed invocation against store (nil for hash).
func Run(o Options, store Store, stdin io.Reader, stdout io.Writer) error {
	switch o.Verb {
	case "hash":
		pw, err := obtain(o, stdin, "Password: ", false)
		if err != nil {
			return err
		}
		h, err := Hash(pw)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, h)
		return nil
	case "set":
		return set(o, store, stdin, stdout)
	case "remove":
		return remove(o, store, stdin, stdout)
	}
	return fmt.Errorf("unknown command %q", o.Verb)
}

// obtain gets the password the way the flags say.
func obtain(o Options, stdin io.Reader, label string, confirm bool) (string, error) {
	switch {
	case o.Generate:
		return Generate()
	case o.Stdin:
		return fromStdin(stdin)
	}
	return prompt(label, confirm)
}

func set(o Options, store Store, stdin io.Reader, stdout io.Writer) error {
	raw, _, err := store.Read(o.Host)
	if err != nil {
		return err
	}
	cur, _, err := readPassword(raw)
	if err != nil {
		return fmt.Errorf("%s: %w — fix it first (zipgo doctor)", o.Host, err)
	}
	next := sites.Password{}
	if cur != nil {
		next = *cur
	}
	if o.Title != nil {
		next.Title = strings.TrimSpace(*o.Title)
	}
	if o.Days != 0 {
		next.SessionDays = o.Days
	}

	var pw string
	if o.KeepPassword {
		if cur == nil || cur.Hash == "" {
			return fmt.Errorf("%s has no password to keep: run zipgo password set %s", o.Host, o.Host)
		}
	} else {
		if pw, err = obtain(o, stdin, "New password for "+o.Host+": ", true); err != nil {
			return err
		}
		if next.Hash, err = Hash(pw); err != nil {
			return err
		}
	}

	out, err := SetPassword(raw, &next)
	if err != nil {
		return err
	}
	if err := store.Write(o.Host, out); err != nil {
		return err
	}

	title := next.Title
	if title == "" {
		title = o.Host
	}
	verb := "is now password-protected"
	if cur != nil {
		verb = "has a new password"
		if o.KeepPassword {
			verb = "keeps its password"
		}
	}
	fmt.Fprintf(stdout, "🔒  %s %s\n", o.Host, verb)
	fmt.Fprintf(stdout, "   login page: %q · sessions: %d days, renewed while in use\n", title, next.Days())
	if cur != nil && !o.KeepPassword {
		fmt.Fprintln(stdout, "   every open session was logged out")
	}
	if o.Generate {
		fmt.Fprintf(stdout, "   password: %s   (shown once: zipgo only keeps its hash)\n", pw)
	}
	if cur == nil {
		fmt.Fprintf(stdout, "   deploy into it as usual — the lock is kept:  zipgo deploy <dir> -d %s\n", o.Host)
	}
	return nil
}

func remove(o Options, store Store, stdin io.Reader, stdout io.Writer) error {
	raw, exists, err := store.Read(o.Host)
	if err != nil {
		return err
	}
	cur, m, err := readPassword(raw)
	if err != nil {
		return fmt.Errorf("%s: %w — fix it first (zipgo doctor)", o.Host, err)
	}
	if !exists || cur == nil {
		fmt.Fprintf(stdout, "🔓  %s has no password — nothing to do\n", o.Host)
		return nil
	}
	if !o.Yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("this makes %s public: pass --yes to confirm", o.Host)
		}
		fmt.Fprintf(os.Stderr, "Make %s public? [y/N] ", o.Host)
		ans, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	out, err := SetPassword(raw, nil)
	if err != nil {
		return err
	}
	if err := store.Write(o.Host, out); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "🔓  %s no longer has a password", o.Host)
	if _, basic := m["basicAuth"]; basic {
		fmt.Fprint(stdout, " (its basicAuth still applies)")
	} else {
		fmt.Fprint(stdout, " — it is public")
	}
	fmt.Fprintln(stdout)
	return nil
}

// ---- status ------------------------------------------------------------------------

// Status is one site's lock, as `status --json` reports it. Never the hash.
type Status struct {
	Host        string `json:"host"`
	Password    bool   `json:"password"`
	Title       string `json:"title,omitempty"`
	SessionDays int    `json:"sessionDays,omitempty"`
	BasicAuth   bool   `json:"basicAuth"`
	// Usable is false when the hash isn't a bcrypt hash: the site is locked
	// and nobody can log in.
	Usable bool   `json:"usable"`
	Config string `json:"config"`
}

// StatusOf reads one host's lock.
func StatusOf(store Store, host string) (Status, error) {
	p, err := store.Path(host)
	if err != nil {
		return Status{}, err
	}
	raw, _, err := store.Read(host)
	if err != nil {
		return Status{}, err
	}
	return statusFrom(host, p, raw)
}

func statusFrom(host, path string, raw []byte) (Status, error) {
	s := Status{Host: host, Config: path}
	cur, m, err := readPassword(raw)
	if err != nil {
		return s, fmt.Errorf("%s: %w", host, err)
	}
	if v, ok := m["basicAuth"]; ok {
		var b map[string]string
		s.BasicAuth = json.Unmarshal(v, &b) == nil && len(b) > 0
	}
	if cur != nil {
		s.Password = true
		s.Title = cur.Title
		s.SessionDays = cur.Days()
		_, err := bcrypt.Cost([]byte(cur.Hash))
		s.Usable = err == nil
	}
	return s, nil
}

// LocalLocked walks a local domains folder for every locked site.
func LocalLocked(base string) ([]Status, error) {
	var out []Status
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != sites.ConfigFileName {
			return err
		}
		host, ok := deploy.HostFromRemote(base, filepath.Dir(p))
		if !ok {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s, err := statusFrom(host, p, raw)
		if err != nil {
			return err
		}
		if s.Password || s.BasicAuth {
			out = append(out, s)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out, err
}

// PrintStatus prints statuses for people, or as JSON.
func PrintStatus(w io.Writer, list []Status, asJSON, one bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if one {
			return enc.Encode(list[0])
		}
		if list == nil {
			list = []Status{}
		}
		return enc.Encode(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "No password-protected site.")
		return nil
	}
	for _, s := range list {
		switch {
		case s.Password && !s.Usable:
			fmt.Fprintf(w, "⛔  %s — locked, but its hash is unusable: nobody can log in (zipgo password set %s)\n", s.Host, s.Host)
		case s.Password:
			title := s.Title
			if title == "" {
				title = s.Host
			}
			fmt.Fprintf(w, "🔒  %s — password, login page %q, sessions %d days\n", s.Host, title, s.SessionDays)
		case s.BasicAuth:
			fmt.Fprintf(w, "🔒  %s — basicAuth only\n", s.Host)
		default:
			fmt.Fprintf(w, "🔓  %s — no password (public)\n", s.Host)
		}
		if s.Password && s.BasicAuth {
			fmt.Fprintln(w, "   and basicAuth as well: visitors get two prompts")
		}
		if one {
			fmt.Fprintf(w, "   config: %s\n", s.Config)
		}
	}
	return nil
}

// RemoteLocked lists every locked site on an SSH target, in one round trip.
func RemoteLocked(t deploy.Target) ([]Status, error) {
	script := "find " + deploy.Quote(t.BaseDir) + " -mindepth 2 -maxdepth 8 -type f -name " +
		deploy.Quote(sites.ConfigFileName) + " 2>/dev/null | while IFS= read -r f; do " +
		`printf '%s\t%s\n' "$f" "$(base64 < "$f" | tr -d '\n')"; done`
	out, err := deploy.RunRemote(t.Host, script)
	if err != nil {
		return nil, fmt.Errorf("scanning %s:%s: %w", t.Host, t.BaseDir, err)
	}
	return parseLocked(t.BaseDir, out)
}

// parseLocked turns RemoteLocked's "<path>\t<base64 config>" lines into the
// locked sites among them.
func parseLocked(base, out string) ([]Status, error) {
	var list []Status
	for _, line := range strings.Split(out, "\n") {
		p, b64, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if !ok {
			continue
		}
		host, ok := deploy.HostFromRemote(base, pathDir(p))
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("%s: unreadable config", p)
		}
		s, err := statusFrom(host, p, raw)
		if err != nil {
			return nil, err
		}
		if s.Password || s.BasicAuth {
			list = append(list, s)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Host < list[j].Host })
	return list, nil
}

// pathDir is path.Dir for a remote (always slash-separated) path.
func pathDir(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return p
}
