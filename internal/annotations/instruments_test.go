package annotations

import (
	"os"
	"path/filepath"
	"testing"
)

const testLexicon = `
[[instrument]]
id = "kick"
family = "drums"
aliases = ["kick", "bass drum", "bd"]
avoid = ["kick bass"]

[[instrument]]
id = "rim"
family = "drums"
aliases = ["rim", "rimshot"]

[[instrument]]
id = "tom"
family = "drums"
aliases = ["tom", "toms"]

[[instrument]]
id = "drums"
family = "drums"
aliases = ["drum", "drums", "kit", "kits"]

[[instrument]]
id = "bass"
family = "bass"
aliases = ["bass"]

[[instrument]]
id = "vocal"
family = "vocal"
aliases = ["vocal", "vox", "voices"]

[[family]]
id = "bass"
flat = true

[[family]]
id = "drums"
flat = false
`

func testLex(t *testing.T) *Lexicon {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "instruments.toml"), []byte(testLexicon), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadInstruments(dir)
}

func TestInstrumentResolve(t *testing.T) {
	lx := testLex(t)
	cases := []struct {
		name, stem string
		dirs       []string
		want       string
	}{
		// the machine name in the filename must not beat the folder's label
		{"rimshot in TOM pack", "Rimshot TOM 31", []string{"01. Individual Hits", "01. TOM", "04. Rimshot"}, "rim"},
		// but a specific filename still beats a generic folder
		{"kick in a drums folder", "Kick 01", []string{"Drums"}, "kick"},
		{"bd abbreviation", "BD A 808 Decay A 01", []string{"01. Bass Drum"}, "kick"},
		// "bass drum" must not read as bass
		{"bass drum is a kick", "hit 3", []string{"01. Bass Drum"}, "kick"},
		{"plain bass stays bass", "Reese 4", []string{"01. Bass"}, "bass"},
		// avoid list: a "kick bass" is a bass patch named for its pairing,
		// so kick steps aside and bass takes it
		{"kick bass reads as bass", "Kick Bass Long", nil, "bass"},
		// order prefixes and separators are noise
		{"underscores", "ff_at_vox_chop", []string{"one-shots"}, "vocal"},
		{"no label anywhere", "Untitled 4", []string{"Misc"}, ""},
		// whole-word only: "subtomic" must not match tom
		{"substring is not a match", "subtomic", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := lx.Resolve(c.stem, c.dirs, nil)
			if got != c.want {
				t.Errorf("Resolve(%q, %v) = %q, want %q", c.stem, c.dirs, got, c.want)
			}
		})
	}
}

func TestFlatFamily(t *testing.T) {
	lx := testLex(t)
	if !lx.FlatFamily("bass") {
		t.Error("bass should be flat")
	}
	if lx.FlatFamily("drums") || lx.FlatFamily("vocal") || lx.FlatFamily("") {
		t.Error("drums (flat=false), vocal (no block) and \"\" must not be flat")
	}
	var nilLex *Lexicon
	if nilLex.FlatFamily("bass") {
		t.Error("nil lexicon must report nothing flat")
	}
}

func TestInstrumentVendorOverride(t *testing.T) {
	lx := testLex(t)
	// SFM's "CH" is a closed hat; far too generic for the shared lexicon.
	vendor := []Instrument{{ID: "hat", Aliases: []string{"ch", "hh"}}}
	if got, family := lx.Resolve("CH Clean 04", []string{"05. HH"}, vendor); got != "hat" || family != "" {
		t.Errorf("vendor override: got %q/%q", got, family)
	}
	// without the override the same path says nothing
	if got, _ := lx.Resolve("CH Clean 04", []string{"05. HH"}, nil); got != "" {
		t.Errorf("without override: got %q, want none", got)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"01. Bass Drum": "bass drum",
		"one-shots":     "one shots",
		"  Hi_Hat  ":    "hi hat",
		"05. HH":        "hh",
		"C#min":         "c min",
		"2) Snare":      "snare",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
