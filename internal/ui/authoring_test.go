package ui

import (
	"strings"
	"testing"
)

func TestRemoveIncludesForLocation(t *testing.T) {
	src := `name = "push"
device = "push3"
storage = "cargo"

# my library
[[include]]
location = "library"
glob     = "Elektron/**"

# added from the library: Grit
[[include]]
location = "splice"
glob     = "Grit - Tech House/**"
as       = "SPLICE/Grit - Tech House"

[[include]]
location = "library"
glob     = "Zero-G/**"

[[include]]
location = "splice"
glob     = "Junkie Kid/**"
as       = "SPLICE/Junkie Kid"
`
	out, n, err := removeIncludesForLocation(src, "splice")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	for _, bad := range []string{"splice", "Grit", "Junkie"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q survived:\n%s", bad, out)
		}
	}
	for _, keep := range []string{"# my library", `glob     = "Elektron/**"`, `glob     = "Zero-G/**"`, `name = "push"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q lost:\n%s", keep, out)
		}
	}
	if strings.Count(out, "[[include]]") != 2 {
		t.Errorf("want 2 includes left:\n%s", out)
	}
	// nothing to do is not an error
	if _, n, err := removeIncludesForLocation(out, "splice"); err != nil || n != 0 {
		t.Errorf("second pass: n=%d err=%v", n, err)
	}
}
