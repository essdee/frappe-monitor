package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandHomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		"~/.ssh/id_ed25519": filepath.Join(home, ".ssh/id_ed25519"),
		"~":                 home,
		"/abs/key":          "/abs/key",   // absolute untouched
		"rel/key":           "rel/key",    // relative untouched
		"~other/key":        "~other/key", // only ~ and ~/ expand, not ~user
	}
	for in, want := range cases {
		if got := expandHomePath(in); got != want {
			t.Errorf("expandHomePath(%q) = %q, want %q", in, got, want)
		}
	}
}
