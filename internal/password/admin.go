package password

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"zipgo/internal/sites"
)

// AdminUsage is the `zipgo admin` help text.
const AdminUsage = `Usage: zipgo admin set <host> [--days N] [--title "…"] [--stdin | --generate]
       zipgo admin remove <host> [--yes]

Turn <host> into the back-office of the site it is nested in: a page, behind
zipgo's login page, listing that site and every subdomain under it, with a
switch to turn each one off and on (the same switch as zipgo off/on) and a
password to put on it or take off. admin.example.com manages example.com and
all of *.example.com; admin.dev.example.com would manage *.dev.example.com.

It writes {"admin": true, "password": {…}} into the host's .zipgoconfig.json
(the folder is created; it should hold nothing else) and zipgo hot-reloads it.
The page has no state of its own: what it shows and changes is the sites'
.zipgoconfig.json files, so it never disagrees with the CLI.

  set <host>       create the back-office, or change its password (prompts
                   twice, without echo; changing it logs everyone out)
      --days N     how long a login lasts without a visit (default: kept,
                   else 7)
      --title T    the login page's heading (default: kept, else the host)
      --stdin      read the password from stdin instead of prompting
      --generate   make up a password and print it once
      --keep-password  change --title/--days only
  remove <host>    take the back-office down (the sites it managed are
                   untouched)
      --yes        don't ask for confirmation

Where: --ssh/--target user@host:/base or --dir <domains folder>, as for
zipgo password.

Examples:
  zipgo admin set admin.gabvdl.xyz --generate
  zipgo admin remove admin.gabvdl.xyz --yes
`

// ParseAdminArgs parses everything after `zipgo admin`: the flags of
// `zipgo password`, with set and remove as the only commands.
func ParseAdminArgs(args []string) (Options, error) {
	o, err := ParseArgs(args)
	if err != nil {
		return o, err
	}
	if o.Verb != "set" && o.Verb != "remove" {
		return o, fmt.Errorf("unknown command %q: use set or remove", args[0])
	}
	return o, nil
}

// RunAdmin executes a parsed `zipgo admin` invocation against store.
func RunAdmin(o Options, store Store, stdin io.Reader, stdout io.Writer) error {
	raw, _, err := store.Read(o.Host)
	if err != nil {
		return err
	}
	cur, _, err := readPassword(raw)
	if err != nil {
		return fmt.Errorf("%s: %w — fix it first (zipgo doctor)", o.Host, err)
	}
	cfg, err := sites.ParseConfig(orEmptyObject(raw))
	if err != nil {
		return fmt.Errorf("%s: %s: %w — fix it first (zipgo doctor)", o.Host, sites.ConfigFileName, err)
	}

	if o.Verb == "remove" {
		if !cfg.Admin {
			fmt.Fprintf(stdout, "%s is not a back-office — nothing to do\n", o.Host)
			return nil
		}
		if !o.Yes {
			return fmt.Errorf("this takes the back-office at %s down: pass --yes to confirm", o.Host)
		}
		out, err := SetPassword(raw, nil)
		if err == nil {
			out, err = setAdmin(out, false)
		}
		if err != nil {
			return err
		}
		if err := store.Write(o.Host, out); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "🛠  %s is no longer a back-office\n", o.Host)
		return nil
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
			return fmt.Errorf("%s has no password to keep: run zipgo admin set %s", o.Host, o.Host)
		}
	} else {
		if pw, err = obtain(o, stdin, "Back-office password for "+o.Host+": ", true); err != nil {
			return err
		}
		if next.Hash, err = Hash(pw); err != nil {
			return err
		}
	}
	out, err := SetPassword(raw, &next)
	if err == nil {
		out, err = setAdmin(out, true)
	}
	if err != nil {
		return err
	}
	if err := store.Write(o.Host, out); err != nil {
		return err
	}

	managed := o.Host
	if _, parent, ok := strings.Cut(o.Host, "."); ok {
		managed = parent
	}
	if cfg.Admin && cur != nil {
		fmt.Fprintf(stdout, "🛠  https://%s keeps managing %s\n", o.Host, managed)
	} else {
		fmt.Fprintf(stdout, "🛠  https://%s is now the back-office of %s and every subdomain under it\n", o.Host, managed)
	}
	fmt.Fprintf(stdout, "   sessions: %d days, renewed while in use\n", next.Days())
	if cur != nil && !o.KeepPassword {
		fmt.Fprintln(stdout, "   new password: every open session was logged out")
	}
	if o.Generate {
		fmt.Fprintf(stdout, "   password: %s   (shown once: zipgo only keeps its hash)\n", pw)
	}
	return nil
}

// setAdmin returns raw with "admin": true added, or the key removed, every
// other key untouched.
func setAdmin(raw []byte, on bool) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, errors.New(sites.ConfigFileName + " is not valid JSON")
		}
	}
	if on {
		m["admin"] = json.RawMessage("true")
	} else {
		delete(m, "admin")
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func orEmptyObject(raw []byte) []byte {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return []byte("{}")
	}
	return raw
}
