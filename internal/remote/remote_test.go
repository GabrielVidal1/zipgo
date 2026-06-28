package remote

import "testing"

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
