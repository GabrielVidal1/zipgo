package remote

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestApexOf(t *testing.T) {
	cases := map[string]string{
		"gabvdl.xyz":                   "gabvdl.xyz",
		"love-letters.game.gabvdl.xyz": "gabvdl.xyz",
		"a.b.c.zipgo.xyz":              "zipgo.xyz",
		"www.dev.gabvdl.xyz.":          "gabvdl.xyz",
		"localhost":                    "localhost",
	}
	for in, want := range cases {
		if got := apexOf(in); got != want {
			t.Errorf("apexOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// record builds one statScript output line.
func record(dir, exists, size, files, mtime, index, bundle, cfg string) string {
	if cfg != "" {
		cfg = base64.StdEncoding.EncodeToString([]byte(cfg))
	}
	return strings.Join([]string{dir, exists, size, files, mtime, index, bundle, cfg}, "\t")
}

func TestParseSites(t *testing.T) {
	want := []hostDir{
		{host: "example.com", dir: "/base/example.com"},
		{host: "docs.example.com", dir: "/base/example.com/docs."},
		{host: "api.example.com", dir: "/base/example.com/api."},
		{host: "old.example.com", dir: "/base/example.com/old."},
		{host: "bad.example.com", dir: "/base/example.com/bad."},
		{host: "gone.example.com", dir: "/base/example.com/gone."},
	}
	out := strings.Join([]string{
		record("/base/example.com", "1", "2048", "3", "1752500000", "1", "0", ""),
		record("/base/example.com/docs.", "1", "9000", "12", "1752500001", "1", "1", ""),
		record("/base/example.com/api.", "1", "0", "0", "1752500002", "0", "0", `{"rewrite":"localhost:8080"}`),
		record("/base/example.com/old.", "1", "10", "1", "1752500003", "1", "0", `{"enable":false}`),
		record("/base/example.com/bad.", "1", "10", "1", "1752500004", "1", "0", `{"enable":`),
		record("/base/example.com/gone.", "0", "0", "0", "", "0", "0", ""),
	}, "\n")

	got := parseSites(want, out)
	if len(got) != len(want) {
		t.Fatalf("got %d sites, want %d", len(got), len(want))
	}

	apex := got[0]
	if apex.Host != "example.com" || apex.URL != "https://example.com" {
		t.Errorf("apex host/url = %q/%q", apex.Host, apex.URL)
	}
	if apex.Type != typeStatic || !apex.Enabled || !apex.Deployed {
		t.Errorf("apex = %+v, want a deployed, enabled, static site", apex)
	}
	if apex.Size != 2048 || apex.Files != 3 {
		t.Errorf("apex size/files = %d/%d, want 2048/3", apex.Size, apex.Files)
	}
	if apex.Modified != "2025-07-14T13:33:20Z" {
		t.Errorf("apex modified = %q", apex.Modified)
	}

	if docs := got[1]; docs.Type != typeSPA {
		t.Errorf("docs type = %q, want %q", docs.Type, typeSPA)
	}
	if api := got[2]; api.Type != typeProxy || api.Proxy != "localhost:8080" {
		t.Errorf("api = %q/%q, want proxy/localhost:8080", api.Type, api.Proxy)
	}
	if old := got[3]; old.Enabled {
		t.Errorf("old.Enabled = true, want false (enable:false)")
	}
	if bad := got[4]; bad.ConfigError == "" || !bad.Enabled {
		t.Errorf("bad = %+v, want a ConfigError on a default-enabled site", bad)
	}
	if gone := got[5]; gone.Deployed || gone.Modified != "" {
		t.Errorf("gone = %+v, want not deployed, with no mtime", gone)
	}
}

// A redirecting site is reported as type "redirect", with its target and the
// status it is sent with (the default when the config omits redirectStatus).
func TestParseSitesRedirect(t *testing.T) {
	want := []hostDir{
		{host: "old.example.com", dir: "/base/example.com/old."},
		{host: "moved.example.com", dir: "/base/example.com/moved."},
	}
	out := strings.Join([]string{
		record("/base/example.com/old.", "1", "0", "0", "1752500000", "0", "0",
			`{"redirect":"https://elsewhere.example"}`),
		// index.html + a redirect: the redirect still decides the type.
		record("/base/example.com/moved.", "1", "10", "1", "1752500001", "1", "0",
			`{"redirect":"https://elsewhere.example/moved","redirectStatus":301}`),
	}, "\n")

	got := parseSites(want, out)
	if old := got[0]; old.Type != typeRedirect ||
		old.Redirect != "https://elsewhere.example" || old.RedirectStatus != 302 {
		t.Errorf("old = %+v, want redirect → https://elsewhere.example (302)", old)
	}
	if moved := got[1]; moved.Type != typeRedirect ||
		moved.Redirect != "https://elsewhere.example/moved" || moved.RedirectStatus != 301 {
		t.Errorf("moved = %+v, want redirect → https://elsewhere.example/moved (301)", moved)
	}
}

// A folder the remote said nothing about must still come back, as not-deployed.
func TestParseSitesMissingRecord(t *testing.T) {
	got := parseSites([]hostDir{{host: "a.example.com", dir: "/base/example.com/a."}}, "")
	if len(got) != 1 {
		t.Fatalf("got %d sites, want 1", len(got))
	}
	if got[0].Host != "a.example.com" || got[0].Deployed || got[0].Type != typeStatic {
		t.Errorf("got %+v, want an undeployed static a.example.com", got[0])
	}
}

func TestStatScriptQuotesDirs(t *testing.T) {
	s := statScript([]string{"/base/a b/it's."})
	if !strings.Contains(s, `'/base/a b/it'\''s.'`) {
		t.Errorf("statScript did not quote the folder:\n%s", s)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{0: "0B", 512: "512B", 1024: "1.0K", 1536: "1.5K", 5 * 1024 * 1024: "5.0M"}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestUnixToRFC3339(t *testing.T) {
	if got := unixToRFC3339("1752500000"); got != "2025-07-14T13:33:20Z" {
		t.Errorf("unixToRFC3339 = %q", got)
	}
	for _, in := range []string{"", "0", "not-a-number"} {
		if got := unixToRFC3339(in); got != "" {
			t.Errorf("unixToRFC3339(%q) = %q, want empty", in, got)
		}
	}
}

// A basicAuth site is reported as protected — a served site that needs
// credentials — without ever exposing the hashes themselves.
func TestParseSitesProtected(t *testing.T) {
	const hash = "$2y$05$lxJfaQMx3WaZsB7K5ZvLz.5Sw1mdcAx2fwJycu2oQJfxqLSySlTK."
	want := []hostDir{
		{host: "example.com", dir: "/base/example.com"},
		{host: "staging.example.com", dir: "/base/example.com/staging."},
	}
	out := strings.Join([]string{
		record("/base/example.com", "1", "10", "1", "1752500000", "1", "0", ""),
		record("/base/example.com/staging.", "1", "10", "1", "1752500001", "1", "0",
			`{"basicAuth":{"claude":"`+hash+`"}}`),
	}, "\n")

	got := parseSites(want, out)
	if apex := got[0]; apex.Protected {
		t.Errorf("apex.Protected = true, want false (no basicAuth)")
	}
	staging := got[1]
	if !staging.Protected {
		t.Errorf("staging.Protected = false, want true (basicAuth)")
	}
	if !staging.Enabled {
		t.Errorf("staging.Enabled = false — a protected site is still served")
	}
	blob, err := json.Marshal(staging)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), hash) {
		t.Errorf("JSON record leaks the password hash: %s", blob)
	}
}

func TestParseSwitchesCascade(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	found := []hostDir{
		{"example.com", "/d/example.com"},
		{"app.example.com", "/d/example.com/app."},
		{"pb.app.example.com", "/d/example.com/app./pb."},
		{"x.pb.app.example.com", "/d/example.com/app./pb./x."},
		{"blog.example.com", "/d/example.com/blog."},
		{"broken.example.com", "/d/example.com/broken."},
	}
	out := "/d/example.com\t\n" +
		"/d/example.com/app.\t" + b64(`{"enable": false}`) + "\n" +
		"/d/example.com/app./pb.\t" + b64(`{"enable": false}`) + "\n" +
		"/d/example.com/app./pb./x.\t\n" +
		"/d/example.com/blog.\t" + b64(`{"enable": true}`) + "\n" +
		"/d/example.com/broken.\t" + b64(`{oops`) + "\n"
	got := map[string]string{}
	for _, s := range parseSwitches(found, out) {
		got[s.Host] = offNote(s)
	}
	want := map[string]string{
		"example.com":          "",
		"app.example.com":      "  ⏸ off",
		"pb.app.example.com":   "  ⏸ off",                            // off on its own too
		"x.pb.app.example.com": "  ⏸ off through pb.app.example.com", // nearest parent
		"blog.example.com":     "",
		"broken.example.com":   "", // unreadable config: reported by info, not guessed at
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// A password site is protected, says so, and never exposes its hash.
func TestParseSitesPassword(t *testing.T) {
	const hash = "$2a$12$lxJfaQMx3WaZsB7K5ZvLz.5Sw1mdcAx2fwJycu2oQJfxqLSySlTK."
	want := []hostDir{
		{host: "ballance.game.example.com", dir: "/base/example.com/game./ballance."},
		{host: "staging.example.com", dir: "/base/example.com/staging."},
	}
	out := strings.Join([]string{
		record("/base/example.com/game./ballance.", "1", "10", "1", "1752500000", "1", "1",
			`{"password":{"hash":"`+hash+`","title":"Ballance"}}`),
		record("/base/example.com/staging.", "1", "10", "1", "1752500001", "1", "0",
			`{"basicAuth":{"claude":"`+hash+`"}}`),
	}, "\n")
	got := parseSites(want, out)
	b := got[0]
	if !b.Protected || !b.Password || b.PasswordTitle != "Ballance" || b.SessionDays != 7 || b.Type != typeSPA {
		t.Errorf("ballance = %+v", b)
	}
	if s := got[1]; !s.Protected || s.Password {
		t.Errorf("basicAuth site = %+v, want protected without a password", s)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), hash) {
		t.Errorf("JSON leaks the hash: %s", blob)
	}
}
