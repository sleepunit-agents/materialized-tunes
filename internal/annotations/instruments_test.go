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
aliases = ["kick", "bass drum"]
codes = ["bd"]
avoid = ["kick bass"]

[[instrument]]
id = "clap"
family = "drums"
aliases = ["clap", "claps"]
codes = ["cp"]

[[instrument]]
id = "rim"
family = "drums"
aliases = ["rim", "rimshot"]

[[instrument]]
id = "tom"
family = "drums"
aliases = ["tom", "toms"]

[[instrument]]
id = "break"
family = "drums"
aliases = ["break", "breaks", "beat", "beats"]
category = "loops"

[[instrument]]
id = "shaker"
family = "percussion"
aliases = ["shaker", "shakers"]

[[instrument]]
id = "break"
family = "drums"
aliases = ["drum loop", "drum loops", "drumloop"]
category = "loops"

[[instrument]]
id = "drums"
family = "drums"
aliases = ["drum", "drums", "kit", "kits"]

[[instrument]]
id = "upright-bass"
family = "bass"
split = true
display = "Upright Bass"
aliases = ["upright bass", "double bass"]

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
		// codes: a two-letter abbreviation is also a Splice pack code or a
		// genre tag, so it speaks for a segment only when no word does
		{"pack code beside a word is not a clap", "FF_CP_124_drum_loop_venice_shaker", []string{"Loops"}, "shaker"},
		{"genre tag beside a word is not a clap", "AU_PC_94_drum_loop_full_cp", []string{"Loops"}, "break"},
		{"code beside another family's word", "AU_PC_94_bass_loop_cp", nil, "bass"},
		// but a name that says nothing longer still means the machine code,
		// and it ranks as its instrument against the folder's catch-all
		{"code alone is the instrument", "909 CP 01", []string{"Drum Hits"}, "clap"},
		{"code alone under an unlabelled folder", "CP A 808 01", []string{"Misc"}, "clap"},
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

func TestSplitAndDisplay(t *testing.T) {
	lx := testLex(t)
	// The one entry that keeps its folder inside a flat family.
	if !lx.SplitsFlat("upright-bass") {
		t.Error("upright-bass should split out of the flat bass family")
	}
	if lx.SplitsFlat("bass") || lx.SplitsFlat("kick") || lx.SplitsFlat("") {
		t.Error("only entries marked split may split")
	}
	if got := lx.DisplayName("upright-bass"); got != "Upright Bass" {
		t.Errorf("DisplayName = %q, want %q", got, "Upright Bass")
	}
	if lx.DisplayName("bass") != "" || lx.DisplayName("") != "" {
		t.Error("an entry with no display override must report none")
	}
	var nilLex *Lexicon
	if nilLex.SplitsFlat("upright-bass") || nilLex.DisplayName("upright-bass") != "" {
		t.Error("nil lexicon must report nothing")
	}
	// Order is specificity: the specific entry sits above generic bass, so
	// it wins wherever on the path the vendor wrote it.
	for _, c := range []struct {
		stem string
		dirs []string
		want string
	}{
		{"Upright Bass C2", []string{"Trio", "Bass"}, "upright-bass"},
		{"Bass_01", []string{"Trio", "Upright Bass"}, "upright-bass"},
		{"Double Bass 03", []string{"Acoustic"}, "upright-bass"},
		{"Reese_01", []string{"Bass"}, "bass"},
	} {
		if got, _ := lx.Resolve(c.stem, c.dirs, nil); got != c.want {
			t.Errorf("Resolve(%q, %v) = %q, want %q", c.stem, c.dirs, got, c.want)
		}
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
	// a vendor block's codes follow the same rule as the shared lexicon's:
	// they yield to any word in the segment, and speak when none does
	coded := []Instrument{{ID: "tom", Codes: []string{"lt", "mt", "ht"}}}
	if got, _ := lx.Resolve("LT 808 Kick Layer", nil, coded); got != "kick" {
		t.Errorf("vendor code beside a word: got %q, want kick", got)
	}
	if got, _ := lx.Resolve("LT 808 01", []string{"Drums"}, coded); got != "tom" {
		t.Errorf("vendor code alone: got %q, want tom", got)
	}
}

// A word that implies a category (break = loops) is a title on a file
// already known to be something else: "Beat" is a kit's name, and its kicks
// are kicks. Ruled out, it speaks for its family alone — through the
// catch-all, ranked where catch-alls rank — so lower entries still get
// their turn and any real label on the path wins.
func TestInstrumentCategoryGate(t *testing.T) {
	lx := testLex(t)
	cases := []struct {
		name, category, stem string
		dirs                 []string
		want                 string
	}{
		// the file's own word, once the glued take number is split off
		{"kick in a kit called Beat", "one-shots", "808_Kick02", []string{"Kits", "Beat"}, "kick"},
		// nothing but the ruled-out word: the family stands in
		{"only the title word", "one-shots", "Beat 03", []string{"Misc"}, "drums"},
		{"a hit cut from a break", "one-shots", "Break Chop Dr Sample 01", nil, "drums"},
		// a lower-ranked real label beats the ruled-out folder word
		{"shaker under Breaks", "one-shots", "Shaker Hit 02", []string{"Breaks"}, "shaker"},
		// on a loop, or when nothing said what the file is, the word stands
		{"a break is a break on a loop", "loops", "Amen Break 01", nil, "break"},
		{"unknown category leaves it alone", "", "Beat 03", []string{"Misc"}, "break"},
		// a multisample can't be a break either
		{"multisample", "multisamples", "Beat C3", nil, "drums"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := lx.ResolveIn(c.category, c.stem, c.dirs, nil)
			if got != c.want {
				t.Errorf("ResolveIn(%q, %q, %v) = %q, want %q", c.category, c.stem, c.dirs, got, c.want)
			}
		})
	}
	// a vendor block naming the gated id inherits the gate
	vendor := []Instrument{{ID: "break", Aliases: []string{"brk"}}}
	if got, _ := lx.ResolveIn("one-shots", "BRK 01", []string{"Misc"}, vendor); got != "drums" {
		t.Errorf("vendor alias of a gated id on a one-shot: got %q, want drums", got)
	}
	if got, _ := lx.ResolveIn("loops", "BRK 01", []string{"Misc"}, vendor); got != "break" {
		t.Errorf("vendor alias of a gated id on a loop: got %q, want break", got)
	}
}

// "Drum loop" is a break by its commonest name, carried by a second break
// entry ranked just above the drums catch-all: below every named piece, so
// a piece named anywhere on the path still wins, and above "drum" alone,
// the only word it displaces. (Jonathan, "01_RA_Drum_Loop_124_Full" filed
// as drums · loops, 2026-09-02.)
func TestDrumLoopIsABreak(t *testing.T) {
	lx := testLex(t)
	cases := []struct {
		name, category, stem string
		dirs                 []string
		want                 string
	}{
		{"the phrase alone", "loops", "01_RA_Drum_Loop_124_Full", nil, "break"},
		{"glued", "loops", "DrumLoop_07", []string{"Loops"}, "break"},
		{"the folder alone", "loops", "Groove 01", []string{"Drum Loops"}, "break"},
		{"a piece in the same segment wins", "loops", "FF_CP_124_drum_loop_venice_shaker", nil, "shaker"},
		{"a piece on the stem beats the folder", "loops", "Shaker 03", []string{"Drum Loops"}, "shaker"},
		{"a drum piece too", "loops", "Drum Loop Kick 02", nil, "kick"},
		{"a one-shot cannot be a break", "one-shots", "Drum Loop Hit 02", nil, "drums"},
		{"drum alone is still the catch-all", "loops", "Drum 02", nil, "drums"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := lx.ResolveIn(c.category, c.stem, c.dirs, nil)
			if got != c.want {
				t.Errorf("ResolveIn(%q, %q, %v) = %q, want %q", c.category, c.stem, c.dirs, got, c.want)
			}
		})
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
		// a take number glued to the word is not part of the word
		"808_Kick02":  "808 kick 02",
		"Snare01":     "snare 01",
		"BD3 Long":    "bd 3 long",
		"TR909 Hat":   "tr 909 hat",
		"80s Toms":    "80 s toms",
		"12bit Kit A": "12 bit kit a",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// compoundLexicon adds the pieces the compound rule needs that the small
// testLexicon lacks: a second word in the same family (snare/clap), a
// family catch-all to degrade to (drums, synth), a cross-family pair
// (pad/fx) and an alias written for a whole two-word phrase.
const compoundLexicon = `
[[instrument]]
id = "kick"
family = "drums"
aliases = ["kick", "kicks", "bass drum"]

[[instrument]]
id = "snare"
family = "drums"
aliases = ["snare", "snares"]

[[instrument]]
id = "clap"
family = "drums"
aliases = ["clap", "claps"]

[[instrument]]
id = "drums"
family = "drums"
aliases = ["drum", "drums"]

[[instrument]]
id = "clave"
family = "percussion"
aliases = ["clave", "claves", "guiro", "claves and guiro"]

[[instrument]]
id = "percussion"
family = "percussion"
aliases = ["percussion", "perc"]

[[instrument]]
id = "sub"
family = "bass"
aliases = ["sub", "rumble"]

[[instrument]]
id = "pad"
family = "synth"
aliases = ["pad", "pads", "texture", "textures"]

[[instrument]]
id = "lead"
family = "synth"
aliases = ["lead", "leads"]

[[instrument]]
id = "synth"
family = "synth"
aliases = ["synth", "synths"]

[[instrument]]
id = "sax"
family = "brass"
aliases = ["sax"]
avoid = ["brass sax"]

[[instrument]]
id = "brass"
family = "brass"
aliases = ["brass"]

[[instrument]]
id = "fx"
family = "fx"
aliases = ["fx", "effects"]
`

func compoundLex(t *testing.T) *Lexicon {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "instruments.toml"), []byte(compoundLexicon), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadInstruments(dir)
}

// A folder naming two things is a label for neither. Without this the
// earlier-ranked word takes the whole folder — every clap filed under Snare,
// silently, with nothing unsorted to notice.
func TestCompoundSegment(t *testing.T) {
	lx := compoundLex(t)
	cases := []struct {
		name, stem  string
		dirs        []string
		want, wantF string
	}{
		// the filename decides, either way round
		{"clap wins from filename", "Clap_Tight_1", []string{"CLAP AND SNARE"}, "clap", "drums"},
		{"snare wins from filename", "Snare_Rim_1", []string{"CLAP AND SNARE"}, "snare", "drums"},
		{"lead wins over pluck-side", "Lead_Saw_1", []string{"LEADS AND STABS"}, "lead", "synth"},
		// filename says nothing: degrade to the family both words share,
		// never to one of them
		{"same family degrades", "Antidote_04", []string{"CLAP AND SNARE"}, "drums", "drums"},
		{"same family, synth", "Antidote_04", []string{"Texture and Lead"}, "synth", "synth"},
		// families disagree: the segment says nothing at all
		{"cross-family stays silent", "Antidote_04", []string{"Texture and FX"}, "", ""},
		{"kick and rumble stays silent", "Antidote_04", []string{"KICKS AND RUMBLE"}, "", ""},
		{"rumble is a sub, not a kick", "Rumble_Long_1", []string{"KICKS AND RUMBLE"}, "sub", "bass"},
		// other separators
		{"ampersand", "Antidote_04", []string{"Kicks & Snares"}, "drums", "drums"},
		{"plus", "Antidote_04", []string{"Clap + Snare"}, "drums", "drums"},
		{"comma", "Antidote_04", []string{"Clap, Snare"}, "drums", "drums"},
		// NOT compounds
		{"one alias for the whole phrase is a pin", "Hit 01", []string{"Claves and Guiro"}, "clave", "percussion"},
		{"a longer alias swallows a shorter", "Hit 01", []string{"Bass Drum and Kicks"}, "kick", "drums"},
		{"no conjunction, no compound", "Deep 01", []string{"Synth Pad"}, "pad", "synth"},
		{"avoid still wins", "Section 01", []string{"Brass & Sax"}, "brass", "brass"},
		// the degraded family label must lose to a real one elsewhere
		{"specific label outranks the family floor", "Kick 01", []string{"Clap and Snare"}, "kick", "drums"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, family := lx.Resolve(c.stem, c.dirs, nil)
			if got != c.want || family != c.wantF {
				t.Errorf("Resolve(%q, %v) = %q/%q, want %q/%q", c.stem, c.dirs, got, family, c.want, c.wantF)
			}
		})
	}
}

// A vendor pin is the escape hatch: a vendor that really does mean one
// instrument by a two-word folder says so and the compound rule stands down.
func TestCompoundVendorPinWins(t *testing.T) {
	lx := compoundLex(t)
	vendor := []Instrument{{ID: "snare", Aliases: []string{"clap and snare"}}}
	if got, _ := lx.Resolve("Antidote_04", []string{"CLAP AND SNARE"}, vendor); got != "snare" {
		t.Errorf("vendor pin: got %q, want snare", got)
	}
}
