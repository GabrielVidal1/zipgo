package password

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"zipgo/internal/sites"
)

func TestParseArgs(t *testing.T) {
	title := func(s string) *string { return &s }
	tests := []struct {
		args    string
		want    Options
		wantErr string
	}{
		{args: "set ballance.game.gabvdl.xyz --title Ballance --days 14",
			want: Options{Verb: "set", Host: "ballance.game.gabvdl.xyz", Title: title("Ballance"), Days: 14}},
		{args: "rotate Ballance.Game.Gabvdl.xyz. --stdin --ssh u@h:/base",
			want: Options{Verb: "set", Host: "ballance.game.gabvdl.xyz", Stdin: true, SSH: "u@h:/base"}},
		{args: "set a.example.com --title= --generate --dir=/srv/domains",
			want: Options{Verb: "set", Host: "a.example.com", Title: title(""), Generate: true, Dir: "/srv/domains"}},
		{args: "set a.example.com --keep-password --days 30",
			want: Options{Verb: "set", Host: "a.example.com", KeepPassword: true, Days: 30}},
		{args: "status", want: Options{Verb: "status"}},
		{args: "show-status a.example.com --json", want: Options{Verb: "status", Host: "a.example.com", JSON: true}},
		{args: "remove a.example.com -y", want: Options{Verb: "remove", Host: "a.example.com", Yes: true}},
		{args: "hash --stdin", want: Options{Verb: "hash", Stdin: true}},

		{args: "", wantErr: "missing command"},
		{args: "open a.example.com", wantErr: "unknown command"},
		{args: "set", wantErr: "needs a host"},
		{args: "remove", wantErr: "needs a host"},
		{args: "set a.example.com b.example.com", wantErr: "unexpected argument"},
		{args: "set a.example.com --days 0", wantErr: "--days must be"},
		{args: "set a.example.com --days 401", wantErr: "--days must be"},
		{args: "set a.example.com --days", wantErr: "requires a value"},
		{args: "set a.example.com --stdin --generate", wantErr: "pick one"},
		{args: "set a.example.com --keep-password", wantErr: "pass one"},
		{args: "status --title x", wantErr: "only go with set"},
		{args: "hash a.example.com", wantErr: "takes no host"},
		{args: "set a.example.com --ssh u@h:/b --dir /x", wantErr: "exclusive"},
		{args: "set a.example.com --bogus", wantErr: "unknown flag"},
	}
	for _, tc := range tests {
		t.Run(tc.args, func(t *testing.T) {
			got, err := ParseArgs(strings.Fields(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(tc.want)
			if string(gj) != string(wj) {
				t.Fatalf("got  %s\nwant %s", gj, wj)
			}
		})
	}
}

func TestSetPassword(t *testing.T) {
	p := &sites.Password{Hash: "$2a$12$x", Title: "Ballance"}
	tests := []struct {
		name, raw string
		p         *sites.Password
		want      string // compact JSON
		wantErr   bool
	}{
		{"new file", "", p, `{"password":{"hash":"$2a$12$x","title":"Ballance"}}`, false},
		{"other keys kept", `{"headers": {"A": "1"}, "enable": true}`, p,
			`{"enable":true,"headers":{"A":"1"},"password":{"hash":"$2a$12$x","title":"Ballance"}}`, false},
		{"replaced", `{"password": {"hash": "old", "sessionDays": 3}}`, p, `{"password":{"hash":"$2a$12$x","title":"Ballance"}}`, false},
		{"removed", `{"password": {"hash": "old"}, "headers": {}}`, nil, `{"headers":{}}`, false},
		{"malformed", `{"password": `, p, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := SetPassword([]byte(tc.raw), tc.p)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var c bytes.Buffer
			if err := json.Compact(&c, out); err != nil {
				t.Fatal(err)
			}
			if c.String() != tc.want {
				t.Fatalf("got  %s\nwant %s", c.String(), tc.want)
			}
		})
	}
}

func TestGenerate(t *testing.T) {
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[` + passphraseAlphabet + `]{4}(-[` + passphraseAlphabet + `]{4}){3}$`)
	for i := 0; i < 50; i++ {
		pw, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(pw) || seen[pw] {
			t.Fatalf("Generate() = %q", pw)
		}
		seen[pw] = true
	}
}

func TestFromStdin(t *testing.T) {
	for in, want := range map[string]string{
		"ilikeballance":         "ilikeballance",
		"ilikeballance\n":       "ilikeballance",
		"ilikeballance\r\nrest": "ilikeballance",
		"with spaces ok \n":     "with spaces ok ",
	} {
		if got, err := fromStdin(strings.NewReader(in)); err != nil || got != want {
			t.Errorf("fromStdin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\n"} {
		if _, err := fromStdin(strings.NewReader(in)); err == nil {
			t.Errorf("fromStdin(%q): want an error", in)
		}
	}
}

func readCfg(t *testing.T, base, rel string) sites.Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, rel, sites.ConfigFileName))
	if err != nil {
		t.Fatal(err)
	}
	var c sites.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func run(t *testing.T, store Store, stdin string, args ...string) string {
	t.Helper()
	o, err := ParseArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(o, store, strings.NewReader(stdin), &out); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

// The whole life of a locked site on a local domains folder: lock it before its
// first deploy, rename the page, rotate, remove.
func TestLifecycle(t *testing.T) {
	base := t.TempDir()
	store := Local{Base: base}
	const host = "ballance.game.example.com"
	const rel = "example.com/game./ballance."

	out := run(t, store, "ilikeballance\n", "set", host, "--stdin", "--title", "Ballance")
	if !strings.Contains(out, "is now password-protected") || !strings.Contains(out, "zipgo deploy <dir> -d "+host) {
		t.Fatalf("set output: %s", out)
	}
	c := readCfg(t, base, rel)
	if c.Password == nil || c.Password.Title != "Ballance" ||
		bcrypt.CompareHashAndPassword([]byte(c.Password.Hash), []byte("ilikeballance")) != nil {
		t.Fatalf("after set: %+v", c.Password)
	}
	if cost, _ := bcrypt.Cost([]byte(c.Password.Hash)); cost != Cost {
		t.Errorf("bcrypt cost = %d, want %d", cost, Cost)
	}
	first := c.Password.Hash
	if left, _ := filepath.Glob(filepath.Join(base, ".zipgo-tmp.*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}

	// Title and days only: the hash (and so every session) survives.
	out = run(t, store, "", "set", host, "--keep-password", "--days", "30")
	if c = readCfg(t, base, rel); c.Password.Hash != first || c.Password.Days() != 30 || c.Password.Title != "Ballance" {
		t.Fatalf("after --keep-password: %+v", c.Password)
	}
	if !strings.Contains(out, "keeps its password") {
		t.Errorf("keep output: %s", out)
	}

	// Rotation: a new hash (even for the same password: a new salt), title kept.
	out = run(t, store, "ilikeballance\n", "rotate", host, "--stdin")
	if c = readCfg(t, base, rel); c.Password.Hash == first || c.Password.Title != "Ballance" {
		t.Fatalf("after rotate: %+v", c.Password)
	}
	if !strings.Contains(out, "every open session was logged out") {
		t.Errorf("rotate output: %s", out)
	}

	st, err := StatusOf(store, host)
	if err != nil || !st.Password || !st.Usable || st.Title != "Ballance" || st.SessionDays != 30 {
		t.Fatalf("status: %+v %v", st, err)
	}
	list, err := LocalLocked(base)
	if err != nil || len(list) != 1 || list[0].Host != host {
		t.Fatalf("LocalLocked: %+v %v", list, err)
	}
	var js bytes.Buffer
	PrintStatus(&js, list, true, false)
	if strings.Contains(js.String(), c.Password.Hash) {
		t.Fatal("status --json leaks the hash")
	}

	out = run(t, store, "", "remove", host, "--yes")
	if c = readCfg(t, base, rel); c.Password != nil || !strings.Contains(out, "it is public") {
		t.Fatalf("after remove: %+v, %s", c.Password, out)
	}
	if list, _ := LocalLocked(base); len(list) != 0 {
		t.Fatalf("still listed: %+v", list)
	}
	if out := run(t, store, "", "remove", host, "--yes"); !strings.Contains(out, "nothing to do") {
		t.Errorf("second remove: %s", out)
	}
}

func TestSetGenerate(t *testing.T) {
	base := t.TempDir()
	out := run(t, Local{Base: base}, "", "set", "x.example.com", "--generate")
	m := regexp.MustCompile(`password: (\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no password printed: %s", out)
	}
	c := readCfg(t, base, "example.com/x.")
	if bcrypt.CompareHashAndPassword([]byte(c.Password.Hash), []byte(m[1])) != nil {
		t.Fatal("the printed password doesn't match the stored hash")
	}
}

func TestKeepPasswordNeedsOne(t *testing.T) {
	o, _ := ParseArgs([]string{"set", "x.example.com", "--keep-password", "--title", "X"})
	if err := Run(o, Local{Base: t.TempDir()}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("--keep-password on a site without a password should fail")
	}
}

func TestHashCommand(t *testing.T) {
	out := strings.TrimSpace(run(t, nil, "s3cret\n", "hash", "--stdin"))
	if bcrypt.CompareHashAndPassword([]byte(out), []byte("s3cret")) != nil {
		t.Fatalf("hash output %q doesn't verify", out)
	}
}

func TestParseLocked(t *testing.T) {
	line := func(p, cfg string) string { return p + "\t" + base64.StdEncoding.EncodeToString([]byte(cfg)) }
	out := strings.Join([]string{
		line("/b/example.com/game./ballance./.zipgoconfig.json", `{"password": {"hash": "$2a$04$abcdefghijklmnopqrstuuJ5OQb8F9uOQb0B3Hn1y8M1z9t3n5vlS", "title": "Ballance"}}`),
		line("/b/example.com/spore./.zipgoconfig.json", `{"basicAuth": {"gate": "h"}}`),
		line("/b/example.com/open./.zipgoconfig.json", `{"headers": {}}`),
		line("/b/example.com/spore./editor/.zipgoconfig.json", `{"password": {"hash": "x"}}`), // content, not a site
	}, "\n")
	list, err := parseLocked("/b", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Host != "ballance.game.example.com" || !list[0].Password ||
		list[1].Host != "spore.example.com" || !list[1].BasicAuth || list[1].Password {
		t.Fatalf("parseLocked = %+v", list)
	}
}
