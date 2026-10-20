package doctor

import "testing"

func TestCheckFlags(t *testing.T) {
	tests := []struct {
		name, config, wantMsg string
		wantLevel             Level
	}{
		{"valid flags: nothing to say", `{"flags": {"coop": {"on": false, "description": "Co-op", "paths": ["/docs/61.html"]}, "daily": true}}`, "", 0},
		{"a typo'd key inside a flag", `{"flags": {"coop": {"enabled": true}}}`, `flag "coop": unknown key "enabled"`, Warn},
		{"a bad name", `{"flags": {"co op": true}}`, `flag "co op"`, Error},
		{"a path covering the whole site", `{"flags": {"coop": {"paths": ["/"]}}}`, `flag "coop" path "/"`, Error},
		{"a value of the wrong type", `{"flags": {"coop": "on"}}`, `flag "coop"`, Error},
		{"not an object", `{"flags": []}`, `"flags" is not an object`, Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, map[string]string{
				"example.com/index.html":        "<title>x</title>",
				"example.com/.zipgoconfig.json": tc.config,
			})
			rep, err := Check(root)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantMsg == "" {
				if len(rep.Findings) != 0 {
					t.Fatalf("findings: %+v", rep.Findings)
				}
				return
			}
			f, ok := find(rep, tc.wantMsg)
			if !ok {
				t.Fatalf("no finding %q in %+v", tc.wantMsg, rep.Findings)
			}
			if f.Level != tc.wantLevel {
				t.Fatalf("level = %v, want %v", f.Level, tc.wantLevel)
			}
		})
	}
}
