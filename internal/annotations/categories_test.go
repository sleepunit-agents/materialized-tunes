package annotations

import (
	"os"
	"path/filepath"
	"testing"
)

const testCategories = `
[[category]]
id = "loops"
aliases = ["loop", "loops", "full breaks", "break", "breaks", "construction"]

[[category]]
id = "one-shots"
aliases = ["one shot", "one shots", "hit", "hits", "stab", "stabs", "kit", "kits"]

[[category]]
id = "fx"
aliases = ["fx", "sfx"]
`

func testCats(t *testing.T) *CategoryLexicon {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "categories.toml"), []byte(testCategories), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadCategories(dir)
}

func TestCategoryResolve(t *testing.T) {
	cx := testCats(t)
	cases := []struct {
		stem string
		dirs []string
		want string
	}{
		{"VB 01", []string{"Full Breaks"}, "loops"},
		{"Amen Break 01", nil, "loops"},                       // stem speaks when no dir does
		{"DJD_bass_loop_coming_home_75_A", []string{"DOMINIC_DAVIS_sample_pack", "DJD_coming_home_75"}, "loops"}, // Splice pack, no category dirs — the stem's "loop" is the only label (Jonathan, 2026-08-30)
		{"Snare Hit 03", []string{"Elements"}, "one-shots"},   // stem, unlabeled dir
		{"Snare Hit 03", []string{"Loops"}, "loops"},          // dir label beats the stem
		{"BD 01", []string{"01. Individual Hits"}, "one-shots"},
		{"x", []string{"Kits", "808 Kit 1"}, "one-shots"},     // kit == one-shots slang; deepest dir first
		{"Riser", []string{"FX"}, "fx"},
		{"White Noise", nil, ""},                              // "hit" inside a word is not a hit
		{"Construction Kits", nil, "loops"},                   // entry order: specific sense first
		{"x", nil, ""},
	}
	for _, c := range cases {
		if got := cx.Resolve(c.stem, c.dirs); got != c.want {
			t.Errorf("Resolve(%q, %v) = %q, want %q", c.stem, c.dirs, got, c.want)
		}
	}
	empty := LoadCategories(t.TempDir())
	if got := empty.Resolve("Drum Loop", nil); got != "" {
		t.Errorf("empty lexicon resolved %q", got)
	}
}
