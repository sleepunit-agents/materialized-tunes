package ui

import (
	"strings"
	"testing"
)

func TestRemoveIncludesUnder(t *testing.T) {
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
	out, n, err := removeIncludesUnder(src, "splice", "")
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
	if _, n, err := removeIncludesUnder(out, "splice", ""); err != nil || n != 0 {
		t.Errorf("second pass: n=%d err=%v", n, err)
	}
}

// A vendor lives inside a location that holds several: "all of Samples From
// Mars" must take its own per-pack rules and leave the neighbours alone.
func TestRemoveIncludesUnderPrefix(t *testing.T) {
	src := `name = "push"
device = "push3"
storage = "cargo"

[[include]]
location = "archive"
glob     = "Samples From Mars/808 From Mars/**"

[[include]]
location = "archive"
glob     = "Samples From Mars/SH-101 From Mars/**"

[[include]]
location = "archive"
glob     = "Goldbaby/**"

# a location-wide rule covers more than this vendor — it must survive
[[include]]
location = "archive"
glob     = "**"

[[include]]
location = "splice"
glob     = "Samples From Mars/nope/**"
`
	out, n, err := removeIncludesUnder(src, "archive", "Samples From Mars/")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	if strings.Contains(out, "808 From Mars") || strings.Contains(out, "SH-101") {
		t.Errorf("vendor rules survived:\n%s", out)
	}
	for _, keep := range []string{`glob     = "Goldbaby/**"`, `glob     = "**"`, `location = "splice"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q lost:\n%s", keep, out)
		}
	}
}

func TestRemoveIncludeBlocks(t *testing.T) {
	src := `name = "push"
device = "push3"
storage = "cargo"

[[include]]
location = "a"
glob     = "one/**"

[[include]]
location = "b"
glob     = "two/**"

[[include]]
location = "c"
glob     = "three/**"
`
	// out of order on purpose: the caller passes indexes as it read them
	out, n, err := removeIncludeBlocks(src, []int{2, 0, 2})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	if !strings.Contains(out, `glob     = "two/**"`) || strings.Count(out, "[[include]]") != 1 {
		t.Errorf("wrong block left:\n%s", out)
	}
}

func TestFindExclude(t *testing.T) {
	src := `name = "push"

[[exclude]]
glob = "Splice/Junk/**"

[[exclude]]
glob = "Splice/Other/**"
`
	if got := findExclude(src, "Splice/Other/**"); got != 1 {
		t.Errorf("findExclude = %d, want 1", got)
	}
	if got := findExclude(src, "nope/**"); got != -1 {
		t.Errorf("findExclude(missing) = %d, want -1", got)
	}
	out, err := removeBlock(src, "[[exclude]]", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Junk") || !strings.Contains(out, "Other") {
		t.Errorf("removed the wrong exclude:\n%s", out)
	}
}
