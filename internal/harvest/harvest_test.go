package harvest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/version"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

func TestHarvest(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/vendors/bmt/vendor.toml", "[vendor]\nname=\"Blu Mar Ten\"\nslug=\"blu-mar-ten\"\n[naming]\nkey_suffix=\" - <camelot>\"\n[[category]]\nid=\"loops\"\nmatch=[\"Breaks\"]\n")
	write("annotations/vendors/bmt/packs/jj.toml", "[pack]\nname=\"JJ\"\nslug=\"jj\"\ndir=\"JJ\"\n[[dir]]\npath=\"Bass\"\ncategory=\"one-shots\"\ntags=[\"bass\",\"sub\"]\n")
	write("annotations/vendors/zg/vendor.toml", "[vendor]\nname=\"Zero-G\"\nslug=\"zero-g\"\n[naming]\nbpm_dir_suffix=true\n[[category]]\nid=\"loops\"\nmatch=[\"Bass Lines*\"]\n")
	write("annotations/vendors/zg/packs/jw1.toml", "[pack]\nname=\"Jungle Warfare Vol 1\"\nslug=\"jungle-warfare-vol-1\"\ndir=\"Jungle Warfare Vol 1\"\n[[dir]]\npath=\"Programmed Loops\"\ncategory=\"loops\"\ninstrument=\"break\"\n")
	write("annotations/instruments.toml", "[[instrument]]\nid=\"break\"\nfamily=\"drums\"\naliases=[\"break\",\"breaks\",\"amen\"]\n[[instrument]]\nid=\"sub\"\nfamily=\"bass\"\naliases=[\"sub\",\"subs\",\"sub bass\"]\n")
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n[naming]\nnote_suffix=\"_<note><octave>\"\n[[category]]\nid=\"one-shots\"\nmatch=[\"*Individual Hits*\"]\n[[category]]\nid=\"multisamples\"\nmatch=[\"*Synths*\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\",\"loops\",\"full breaks\",\"break\",\"breaks\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"one shots\",\"hit\",\"hits\"]\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Blu Mar Ten/JJ/Bass/Champion Sub - 10A.wav", "s1"),
		mk("Blu Mar Ten/JJ/Breaks/Amen Cut.wav", "s2"),
		mk("Zero-G/Jungle Warfare Vol 2/Bass Lines 166.5/808 Standard 1 (JW2).wav", "s3"),
		mk("Samples From Mars/2600 From Mars/WAV/Synths/37_ItsASurprise_2600_C#1.wav", "s4"),
		mk("Samples From Mars/808 From Mars/WAV/01. Individual Hits/BD 01.wav", "s5"),
		mk("Elektron/OT/Loops/Hat Loop 03 124 Bpm.wav", "s6"),                         // unknown vendor: generic bpm still works
		mk("Nobody/Pack/x.wav", "s7"),                                                 // nothing to say
		mk("Nobody/Vinyl Breaks Vol 4/Full Breaks/VB 01.wav", "s8"),                   // shared lexicon, no vendor annotation
		mk("Nobody/Pack/Snare Hit 03.wav", "s9"),                                      // shared lexicon from the stem
		mk("Nobody/Pack/Loops/Snare Hit 03.wav", "s10"),                               // dir label beats the stem
		mk("Zero-G/Jungle Warfare Vol 1/Programmed Loops/Sub-Urban 155 1.wav", "s11"), // [[dir]] instrument pin beats the lexicon's "sub"
		// a multisample dir with no category word anywhere: chromatic
		// note-suffixed siblings (SFM 101-style naming, random suffixes)
		mk("Samples From Mars/101 From Mars/WAV/Bass/0_FishFriend_SH101_C-2-NBQM.wav", "ms1"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/1_FishFriend_SH101_C#-2-U1UX.wav", "ms2"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/2_FishFriend_SH101_D-2-1RQQ.wav", "ms3"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/3_FishFriend_SH101_D#-2-U65Z.wav", "ms4"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/4_FishFriend_SH101_E-2-XEVR.wav", "ms5"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/12_BigSub1_SH101_D1_C2IQ.wav", "ms6"),
		mk("Samples From Mars/101 From Mars/WAV/Bass/5_FishFriend_SH101_F-2-K2LP.wav", "ms7"), // FishFriend's sixth note: one name spans six, the dir is a multisample
		// looks notey but isn't: lowercase take names, too few pitches
		mk("Nobody/Pack2/kits dir/kit_a_1.wav", "k1"),
		mk("Nobody/Pack2/kits dir/kit_a_2.wav", "k2"),
		mk("Nobody/Pack2/kits dir/kit_b_1.wav", "k3"),
		// one-shots with flavours: six pitches, six NAMES — no one patch
		// spans the keyboard, so the folder is not a multisample
		mk("Nobody/Pack3/Synths/Alpha Bass C1.wav", "fl1"),
		mk("Nobody/Pack3/Synths/Beta Bass G0.wav", "fl2"),
		mk("Nobody/Pack3/Synths/Gamma Bass D#1.wav", "fl3"),
		mk("Nobody/Pack3/Synths/Delta Bass F2.wav", "fl4"),
		mk("Nobody/Pack3/Synths/Epsilon Bass A1.wav", "fl5"),
		mk("Nobody/Pack3/Synths/Zeta Bass B2.wav", "fl6"),
		mk("Nobody/Pack3/Synths/Eta Bass E1.wav", "fl7"),
		// one name, six pitches, round-robin takes: a multisample even
		// though half the files repeat a note (SFM DX100 "_0001" grammar)
		mk("Nobody/Pack4/Bells/60_TBells_DX100_C3.wav", "rr1"),
		mk("Nobody/Pack4/Bells/60_TBells_DX100_C3_0001.wav", "rr2"),
		mk("Nobody/Pack4/Bells/61_TBells_DX100_C#3.wav", "rr3"),
		mk("Nobody/Pack4/Bells/61_TBells_DX100_C#3_0001.wav", "rr4"),
		mk("Nobody/Pack4/Bells/62_TBells_DX100_D3.wav", "rr5"),
		mk("Nobody/Pack4/Bells/62_TBells_DX100_D3_0001.wav", "rr6"),
		mk("Nobody/Pack4/Bells/63_TBells_DX100_D#3.wav", "rr7"),
		mk("Nobody/Pack4/Bells/64_TBells_DX100_E3.wav", "rr8"),
		mk("Nobody/Pack4/Bells/65_TBells_DX100_F3.wav", "rr9"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	res, err := Run(ws, ws.Config.Locations[0])
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 33 { // 16 + ms7 + the 7 flavoured hits (bass) + the 9 bell takes
		t.Errorf("files with metadata = %d, want 33", res.Files)
	}
	got := map[string]Meta{}
	f, _ := os.Open(filepath.Join(dir, "annotations-cache", "meta", "src.jsonl"))
	defer f.Close()
	dec := jsonDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			break
		}
		got[m.SHA] = m
	}
	check := func(sha, key string, bpm int, cat string, tags ...string) {
		m := got[sha]
		if m.Key != key || m.BPM != bpm || m.Category != cat {
			t.Errorf("%s: got key=%q bpm=%d cat=%q, want %q %d %q", sha, m.Key, m.BPM, m.Category, key, bpm, cat)
		}
		if len(tags) > 0 && len(m.Tags) != len(tags) {
			t.Errorf("%s: tags %v want %v", sha, m.Tags, tags)
		}
	}
	check("s1", "Bm", 0, "one-shots", "bass", "sub") // camelot 10A → B minor; pack [[dir]] governs
	check("s2", "", 0, "loops")                      // vendor [[category]]
	check("s3", "", 167, "loops")                    // bpm from dir suffix (166.5 → 167)
	check("s4", "C#1", 0, "multisamples")            // SFM note suffix
	check("s5", "", 0, "one-shots")                  // "01. Individual Hits" via glob
	check("s6", "", 124, "loops")                    // no vendor — the shared lexicon reads the "Loops" dir
	check("s8", "", 0, "loops")                      // shared lexicon: "Full Breaks" dir, unannotated vendor
	check("s9", "", 0, "one-shots")                  // shared lexicon: "Hit" in the stem
	check("s10", "", 0, "loops")                     // dirs deepest-first beat the stem
	check("ms1", "", 0, "multisamples")              // chromatic dir shape, no label anywhere
	check("ms6", "", 0, "multisamples")              // …and the C2 in the random suffix didn't fool it
	if _, ok := got["k1"]; ok {
		t.Error("k1: lowercase take names must not read as a multisample dir")
	}
	if m := got["fl1"]; m.Category != "" {
		t.Errorf("fl1: seven differently-named hits that carry keys read as %q, want no category (flavours, not a multisample)", m.Category)
	}
	check("rr1", "C3", 0, "multisamples") // one name over six pitches, round-robin takes and all
	if m := got["rr2"]; m.Category != "multisamples" {
		t.Errorf("rr2: the _0001 take of a multisample read as %q, want multisamples", m.Category)
	}
	if m := got["s11"]; m.Instrument != "break" || m.Family != "drums" || m.Category != "loops" {
		// a jungle groove named after its source must not read as sub bass
		t.Errorf("s11: got instrument=%q family=%q cat=%q, want break/drums/loops", m.Instrument, m.Family, m.Category)
	}
	if _, ok := got["s7"]; ok {
		t.Error("s7 has nothing to harvest and must be absent")
	}

	// every resolved facet says which tier answered and what it fired on
	why := func(sha, facet, tier, segment, word string) {
		m := got[sha]
		var src *annotations.Source
		if m.Why != nil {
			if facet == "category" {
				src = m.Why.Category
			} else {
				src = m.Why.Instrument
			}
		}
		if src == nil {
			t.Errorf("%s: %s has no source, want %s %q on %q", sha, facet, tier, word, segment)
			return
		}
		if src.Tier != tier || src.Segment != segment || src.Word != word {
			t.Errorf("%s: %s source = %s %q on %q, want %s %q on %q", sha, facet, src.Tier, src.Word, src.Segment, tier, word, segment)
		}
	}
	why("s1", "category", annotations.TierDir, "Bass", "Bass")                                        // pack [[dir]] pin
	why("s2", "category", annotations.TierVendorCategory, "Breaks", "Breaks")                         // vendor glob on the dir
	why("s5", "category", annotations.TierVendorCategory, "01. Individual Hits", "*Individual Hits*") // the glob, not the dir
	why("s6", "category", annotations.TierCategories, "Loops", "loops")                               // shared alias, normalized
	why("s9", "category", annotations.TierCategories, "Snare Hit 03", "hit")                          // from the stem
	why("s11", "category", annotations.TierDir, "Programmed Loops", "Programmed Loops")               //
	why("s11", "instrument", annotations.TierDir, "Programmed Loops", "Programmed Loops")             // the pin, not the lexicon's "sub"
	why("s1", "instrument", annotations.TierLexicon, "Champion Sub - 10A", "sub")                     // shared lexicon from the stem
	why("ms1", "category", annotations.TierMultisample, "WAV/Bass", "")                               // no word; the directory's shape
	if m := got["s7"]; m.Why != nil {
		t.Errorf("s7: nothing spoke, why must be nil, got %+v", m.Why)
	}

	// Explain answers for one path from the annotations on disk, siblings included
	ex, err := Explain(ws, ws.Config.Locations[0], "Samples From Mars/101 From Mars/WAV/Bass/0_FishFriend_SH101_C-2-NBQM.wav")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Category != "multisamples" || ex.Why == nil || ex.Why.Category == nil || ex.Why.Category.Tier != annotations.TierMultisample {
		t.Errorf("Explain: got %+v, want multisamples via the directory shape", ex)
	}
	if _, err := Explain(ws, ws.Config.Locations[0], "Nobody/nope.wav"); err == nil {
		t.Error("Explain of a path outside the catalog must fail")
	}
}

func jsonDecoder(f *os.File) interface{ Decode(any) error } { return json.NewDecoder(f) }

// A pack can say what a word means inside it. Drumtrax From Mars labels
// its kick "Bass" — in the hits folder, in the filename, and again in
// the Kits copies — and the shared lexicon reads "bass" as bass. A [[dir]]
// pin can't reach a filename, so the pack carries its own [[instrument]]
// block, consulted before the vendor's and before the shared lexicon;
// the vendor's other packs keep reading "Bass" as bass.
func TestHarvestPackInstrumentOverride(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\",\"bass drum\"]\n"+
		"[[instrument]]\nid=\"bass\"\nfamily=\"bass\"\naliases=[\"bass\"]\n")
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n"+
		"[[instrument]]\nid=\"kick\"\naliases=[\"bd\"]\n")
	write("annotations/vendors/sfm/packs/drumtrax.toml", "[pack]\nname=\"Drumtrax From Mars\"\nslug=\"drumtrax-from-mars\"\ndir=\"Drumtrax From Mars\"\n"+
		"[[instrument]]\nid=\"kick\"\naliases=[\"bass\"]\n")
	write("annotations/vendors/sfm/packs/ob.toml", "[pack]\nname=\"OB From Mars\"\nslug=\"ob-from-mars\"\ndir=\"OB From Mars\"\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Samples From Mars/Drumtrax From Mars/WAV/01. Individual Hits/01. Bass/Bass Drumtrax 08.wav", "d1"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/02. Kits/Kit 1/Bass Drumtrax 05.wav", "d2"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/01. Individual Hits/04. Snare/Snare Drumtrax 05.wav", "d3"),
		mk("Samples From Mars/OB From Mars/WAV/Bass/Bass Growl OB 01.wav", "o1"),
		mk("Samples From Mars/OB From Mars/WAV/Drums/BD OB 01.wav", "o2"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := map[string]Meta{}
	f, _ := os.Open(filepath.Join(dir, "annotations-cache", "meta", "src.jsonl"))
	defer f.Close()
	dec := jsonDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			break
		}
		got[m.SHA] = m
	}
	want := map[string][2]string{
		"d1": {"kick", "drums"}, // the pack's word, in folder and stem
		"d2": {"kick", "drums"}, // the kit copy carries the same name — no dir pin reaches it
		"d3": {"", ""},          // the pack block adds a meaning, it doesn't invent labels
		"o1": {"bass", "bass"},  // a sibling pack still reads "Bass" as bass
		"o2": {"kick", "drums"}, // the vendor block still applies beneath the pack's
	}
	for sha, w := range want {
		m := got[sha]
		if m.Instrument != w[0] || m.Family != w[1] {
			t.Errorf("%s: got %q/%q, want %q/%q", sha, m.Instrument, m.Family, w[0], w[1])
		}
	}
}

// A vendor ships the same bytes at two paths — a Step Kit folder holds
// copies of the pack's own hits. Each path's labels come from that path;
// under content keying, whichever copy harvested last wrote the
// classification for both, and Polyend's mono cuts filed under Drums
// because their kit twins said "kit" (the 33-stray split of 2026-09-01).
func TestHarvestSharedBytesKeepTheirOwnLabels(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	inst := filepath.Join(dir, "annotations", "instruments.toml")
	os.MkdirAll(filepath.Dir(inst), 0o755)
	os.WriteFile(inst, []byte("[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drum\",\"drums\",\"kit\",\"kits\"]\n"+
		"[[instrument]]\nid=\"percussion\"\nfamily=\"percussion\"\naliases=[\"percussion\",\"perc\"]\n"), 0o644)
	mk := func(path string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: "same-bytes", Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cut := "Polyend/Fractured/Fractured 16 bit mono/Bright/Perc_Bright.wav"
	kit := "Polyend/Fractured/Step Kits/Bright Kit/Perc_Bright.wav"
	cat := map[string]catalog.Entry{cut: mk(cut), kit: mk(kit)}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if MetaFresh(ws) {
		t.Fatal("no harvest has run — the format must not read as fresh")
	}
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	m := LoadMeta(ws, "src")
	if got := m[cut].Family; got != "percussion" {
		t.Errorf("cut family = %q, want percussion — its kit twin must not label it", got)
	}
	if got := m[kit].Family; got != "drums" {
		t.Errorf("kit copy family = %q, want drums", got)
	}
	if !MetaFresh(ws) {
		t.Error("harvest must stamp the meta cache format")
	}
	if got := MetaBuild(ws); got != version.Version {
		t.Errorf("MetaBuild = %q, want this build %q", got, version.Version)
	}
	// Another build wrote it: same record format, different harvest. A
	// self-update relaunch must treat that as stale, not fresh.
	stamp := filepath.Join(dir, "annotations-cache", "meta", ".format")
	if err := os.WriteFile(stamp, []byte(MetaFormat+"\nsome-other-build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if MetaFresh(ws) {
		t.Error("a cache written by another build must not read as fresh")
	}
	// A cache from before the stamp existed (format line only) is stale too.
	if err := os.WriteFile(stamp, []byte(MetaFormat+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if MetaFresh(ws) {
		t.Error("an unstamped cache must not read as fresh")
	}
	if MetaBuild(ws) != "" {
		t.Error("an unstamped cache has no build")
	}
}

// A dir that restates the pack's own name is not a label. Splice wraps
// nearly every pack in one (Label_-_Title_Audio); with the wrapper read as
// a label, a kick in "Vocal Pop House 2" is a vocal and every one-shot in a
// Function Loops pack is a loop. Unwrapped packs put real labels at the
// same depth ("DRUMS", "Loops") — those must keep speaking — and a file
// whose path says nothing else still gets the pack's word as a fallback.
func TestHarvestPackEchoIsNoLabel(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.Init(dir)
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	// vocal above kick, as the shared lexicon has it since 2026-09-01
	write("annotations/instruments.toml", "[[instrument]]\nid=\"vocal\"\nfamily=\"vocal\"\naliases=[\"vocal\",\"vocals\"]\n"+
		"[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n"+
		"[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drum\",\"drums\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\",\"loops\"]\n"+
		"[[category]]\nid=\"one-shots\"\naliases=[\"one shot\",\"one shots\",\"hit\"]\n")
	write("annotations/vendors/splice/vendor.toml", "[vendor]\nname=\"Splice\"\nslug=\"splice\"\n[packs]\ngrammar=\"top-level-dirs\"\nresolver=\"splice-graphql\"\n"+
		"[[category]]\nid=\"loops\"\nmatch=[\"*loop*\"]\n[[category]]\nid=\"one-shots\"\nmatch=[\"*one_shot*\",\"*hit*\"]\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Splice/Vocal Pop House 2/Dropgun_Samples_-_Vocal_Pop_House_2/drum_one_shots/DS_VPH_kick_01.wav", "s1"),
		mk("Splice/Vocal Pop House 2/Dropgun_Samples_-_Vocal_Pop_House_2/vocal_loops/DS_VPH_vocal_loop_01.wav", "s2"),
		mk("Splice/Cinematic Cyberpunk/Function_Loops_-_Cinematic_Cyberpunk/one_shots/FL_CC_hit_01.wav", "s3"),
		mk("Splice/Silk Vocals/RNT_silk_vocals/loops/RNT_SV_01.wav", "s4"),
		mk("Splice/Nu Disco Dynamite/DRUMS/DRUM_LOOPS/TND_01.wav", "s5"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := map[string]Meta{}
	f, _ := os.Open(filepath.Join(dir, "annotations-cache", "meta", "src.jsonl"))
	defer f.Close()
	dec := jsonDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			break
		}
		got[m.SHA] = m
	}
	want := map[string][2]string{
		"s1": {"kick", "one-shots"}, // the wrapper's "Vocal" is the pack's name, not this file's
		"s2": {"vocal", "loops"},    // the file's own words still speak
		"s3": {"", "one-shots"},     // "Function_Loops" no longer makes a hit a loop
		"s4": {"vocal", "loops"},    // nothing else spoke, so the echo may: the pack is vocals
		"s5": {"drums", "loops"},    // an unwrapped pack's "DRUMS" is a real label
	}
	for sha, w := range want {
		m := got[sha]
		if m.Instrument != w[0] || m.Category != w[1] {
			t.Errorf("%s: got %q/%q, want %q/%q", sha, m.Instrument, m.Category, w[0], w[1])
		}
	}
}

// A kit called "Beat" holds 808/909 kicks. Two things had to be true for
// them to file as breaks: the take number glued to the word hid the kick
// ("Kick02" is no whole word), and the break word had no idea the file was
// a one-shot. Both are fixed; a break is a loop by definition, and a
// one-shot the break word alone describes is a drum hit of unknown piece.
func TestHarvestBreakIsALoop(t *testing.T) {
	dir := t.TempDir()
	ws, _ := workspace.Init(dir)
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n"+
		"[[instrument]]\nid=\"break\"\nfamily=\"drums\"\naliases=[\"break\",\"beat\"]\ncategory=\"loops\"\n"+
		"[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drum\",\"drums\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\",\"loops\"]\n"+
		"[[category]]\nid=\"one-shots\"\naliases=[\"chop\"]\n"+
		"[[category]]\nid=\"loops\"\naliases=[\"break\",\"breaks\"]\n"+
		"[[category]]\nid=\"one-shots\"\naliases=[\"one shot\",\"one shots\",\"kit\",\"kits\"]\n")
	write("annotations/vendors/house/vendor.toml", "[vendor]\nname=\"House\"\nslug=\"house\"\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("House/Drum Machines/Kits/Beat/808_Kick02.wav", "k1"),
		mk("House/Drum Machines/Kits/Beat/909_Kick04.wav", "k2"),
		mk("House/Drum Machines/Kits/Beat/Perc07.wav", "k3"),
		mk("House/Dr Sample From Mars/WAV/Break Chop Dr Sample 01.wav", "c1"),
		mk("House/Vinyl Breaks/Loops/Amen Break 01.wav", "b1"),
		mk("House/Vinyl Breaks/Break 04.wav", "b2"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := map[string]Meta{}
	f, _ := os.Open(filepath.Join(dir, "annotations-cache", "meta", "src.jsonl"))
	defer f.Close()
	dec := jsonDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			break
		}
		got[m.SHA] = m
	}
	want := map[string][2]string{
		"k1": {"kick", "one-shots"},  // the glued take number no longer hides the kick
		"k2": {"kick", "one-shots"},  // same
		"k3": {"drums", "one-shots"}, // nothing but the kit's title word: a drum hit, not a break
		"c1": {"drums", "one-shots"}, // a hit cut from a break is a drum hit
		"b1": {"break", "loops"},     // a break on a loop is a break
		"b2": {"break", "loops"},     // the word itself says loop when nothing else did
	}
	for sha, w := range want {
		m := got[sha]
		if m.Instrument != w[0] || m.Category != w[1] {
			t.Errorf("%s: got %q/%q, want %q/%q", sha, m.Instrument, m.Category, w[0], w[1])
		}
	}
}

// A [[dir]] default speaks last — only for a file no word and no shape
// claimed — where a pin would have overruled the filenames. And the local
// layer's entries win over the checkout's at the same path, without any
// new precedence rule.
func TestHarvestDefaultsAndLocalLayer(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n[[instrument]]\nid=\"lead\"\nfamily=\"synth\"\naliases=[\"lead\"]\n[[instrument]]\nid=\"synth\"\nfamily=\"synth\"\naliases=[\"synth\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"hit\"]\n")
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n")
	// the checkout: Patches is a default (patch names say nothing), Hits is a pin
	write("annotations/vendors/sfm/packs/x.toml", "[pack]\nname=\"X\"\nslug=\"x\"\ndir=\"X\"\n[[dir]]\npath=\"WAV/Patches\"\ndefault_category=\"multisamples\"\ndefault_instrument=\"synth\"\n[[dir]]\npath=\"WAV/Hits\"\ncategory=\"one-shots\"\n")
	// the user's copy: Hits are actually loops here, and Extras is theirs alone
	write("annotations.local/vendors/sfm/packs/x.toml", "[pack]\nslug=\"x\"\n[[dir]]\npath=\"WAV/Hits\"\ncategory=\"loops\"\nobserved=\"2026-09-01\"\n[[dir]]\npath=\"WAV/Extras\"\ndefault_instrument=\"kick\"\nlocal=true\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Samples From Mars/X/WAV/Patches/David Lynch.wav", "d1"),  // nothing spoke → both defaults
		mk("Samples From Mars/X/WAV/Patches/Kick Loop 01.wav", "d2"), // its own words win over both defaults
		mk("Samples From Mars/X/WAV/Patches/Cosmic Lead.wav", "d3"),  // instrument word, category default
		mk("Samples From Mars/X/WAV/Hits/Clint Eastwood.wav", "l1"),  // local pin beats the checkout's at the same path
		mk("Samples From Mars/X/WAV/Extras/Untitled 3.wav", "l2"),    // local-only default
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := LoadMeta(ws, "src")
	want := func(p, cat, catTier, inst, instTier string) {
		m := got["Samples From Mars/X/WAV/"+p]
		ct, it := "", ""
		if m.Why != nil && m.Why.Category != nil {
			ct = m.Why.Category.Tier
		}
		if m.Why != nil && m.Why.Instrument != nil {
			it = m.Why.Instrument.Tier
		}
		if m.Category != cat || ct != catTier || m.Instrument != inst || it != instTier {
			t.Errorf("%s: got %s(%s) %s(%s), want %s(%s) %s(%s)", p, m.Category, ct, m.Instrument, it, cat, catTier, inst, instTier)
		}
	}
	want("Patches/David Lynch.wav", "multisamples", annotations.TierDirDefault, "synth", annotations.TierDirDefault)
	want("Patches/Kick Loop 01.wav", "loops", annotations.TierCategories, "kick", annotations.TierLexicon)
	want("Patches/Cosmic Lead.wav", "multisamples", annotations.TierDirDefault, "lead", annotations.TierLexicon)
	want("Hits/Clint Eastwood.wav", "loops", annotations.TierDir, "", "")
	want("Extras/Untitled 3.wav", "", "", "kick", annotations.TierDirDefault)
	if m := got["Samples From Mars/X/WAV/Hits/Clint Eastwood.wav"]; m.Why.Category.Word != "WAV/Hits" {
		t.Errorf("the winning entry is the local one at the same path: %+v", m.Why.Category)
	}
}

// The document tier: a Live document the vendor filed under a labelled
// folder speaks for the samples it references — after every word on the
// sample's own path, before echoes, shape and defaults; documents that
// disagree say nothing but explain why.
func TestHarvestDocumentFolderLabelsItsSamples(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n[formats]\ncanonical_dir=\"WAV\"\nparallel_dirs=[\"Ableton Live*\"]\n")
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n[[instrument]]\nid=\"lead\"\nfamily=\"synth\"\naliases=[\"lead\",\"leads\"]\n[[instrument]]\nid=\"bass\"\nfamily=\"bass\"\naliases=[\"bass\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"hit\",\"kit\",\"kits\"]\n")

	wav := func(p, sha string) catalog.Entry {
		return catalog.Entry{Path: p, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	doc := func(p string, names ...string) catalog.Entry {
		d := &ableton.Doc{}
		for _, n := range names {
			d.Refs = append(d.Refs, ableton.Ref{Name: n})
		}
		return catalog.Entry{Path: p, SHA256: "d-" + p, Size: 1, ScannedAt: time.Now(), Doc: d}
	}
	const ms10 = "Samples From Mars/MS10 From Mars/"
	const live = ms10 + "Ableton Live/MS10 From Mars/Presets/"
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		// a multisample whose own path says nothing but the patch name
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 C0.wav", "p1"),
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 C#0.wav", "p2"),
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 D0.wav", "p3"),
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 D#0.wav", "p4"),
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 E0.wav", "p5"),
		wav(ms10+"WAV/SuperPulse/12 SuperPulse MS10 F0.wav", "p6"),
		// its own folder speaks: the rack's folder does not override it
		wav(ms10+"WAV/Bass/12 Fat MS10 C0.wav", "b1"),
		// nothing on the path, no document points at it: still silent
		wav(ms10+"WAV/Untitled/12 Mystery MS10 C0.wav", "u1"),
		// a hit named by a kit rack: category from the rack's folder
		wav("Samples From Mars/808 From Mars/WAV/Assorted/Thump 01.wav", "k1"),
		doc(live+"Leads/SuperPulse.adg",
			"12 SuperPulse MS10 C0.wav", "12 SuperPulse MS10 C#0.wav", "12 SuperPulse MS10 D0.wav",
			"12 SuperPulse MS10 D#0.wav", "12 SuperPulse MS10 E0.wav", "12 SuperPulse MS10 F0.wav", "12 Fat MS10 C0.wav"),
		// a second rack in a folder that disagrees, naming one of them
		doc(live+"Bass/Other.adg", "12 SuperPulse MS10 E0.wav"),
		doc("Samples From Mars/808 From Mars/Ableton Live/808 From Mars/Presets/Kits/Clean Kit.adg", "Thump 01.wav"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := LoadMeta(ws, "src")
	want := func(p, cat, catTier, inst, fam, instTier string) *Meta {
		m := got[p]
		ct, it := "", ""
		if m.Why != nil && m.Why.Category != nil {
			ct = m.Why.Category.Tier
		}
		if m.Why != nil && m.Why.Instrument != nil {
			it = m.Why.Instrument.Tier
		}
		if m.Category != cat || ct != catTier || m.Instrument != inst || m.Family != fam || it != instTier {
			t.Errorf("%s: got %s(%s) %s/%s(%s), want %s(%s) %s/%s(%s)", p, m.Category, ct, m.Instrument, m.Family, it, cat, catTier, inst, fam, instTier)
		}
		return &m
	}
	m := want(ms10+"WAV/SuperPulse/12 SuperPulse MS10 C0.wav", "multisamples", annotations.TierMultisample, "lead", "synth", annotations.TierDocument)
	if m.Why == nil || m.Why.Instrument == nil || m.Why.Instrument.Doc != live+"Leads/SuperPulse.adg" || m.Why.Instrument.Segment != "Leads" {
		t.Errorf("the source should name the document and its folder: %+v", m.Why)
	}
	want(ms10+"WAV/SuperPulse/12 SuperPulse MS10 D#0.wav", "multisamples", annotations.TierMultisample, "lead", "synth", annotations.TierDocument)
	// own path wins over the rack's folder
	want(ms10+"WAV/Bass/12 Fat MS10 C0.wav", "", "", "bass", "bass", annotations.TierLexicon)
	// no document: nothing spoke
	want(ms10+"WAV/Untitled/12 Mystery MS10 C0.wav", "", "", "", "", "")
	// two racks, two folders: silence with a reason
	m = want(ms10+"WAV/SuperPulse/12 SuperPulse MS10 E0.wav", "multisamples", annotations.TierMultisample, "", "", annotations.TierDocumentConflict)
	if m.Why == nil || m.Why.Instrument == nil || !strings.Contains(m.Why.Instrument.Segment, "≠") {
		t.Errorf("conflict should name both documents: %+v", m.Why)
	}
	// a kit rack's folder says one-shots; nothing names the instrument
	m = want("Samples From Mars/808 From Mars/WAV/Assorted/Thump 01.wav", "one-shots", annotations.TierDocument, "", "", "")
	if m.Why == nil || m.Why.Category == nil || m.Why.Category.Word != "kits" {
		t.Errorf("category source should carry the alias: %+v", m.Why)
	}
	// Explain sees the same tier for a single path
	ex, err := Explain(ws, ws.Config.Locations[0], ms10+"WAV/SuperPulse/12 SuperPulse MS10 C0.wav")
	if err != nil || ex.Instrument != "lead" || ex.Why == nil || ex.Why.Instrument == nil || ex.Why.Instrument.Tier != annotations.TierDocument {
		t.Errorf("Explain: got %+v, %v; want lead via the document tier", ex, err)
	}
	if s := ex.Why.Instrument.Describe(); !strings.Contains(s, "SuperPulse.adg") || !strings.Contains(s, "Leads") {
		t.Errorf("Describe should name folder and document: %q", s)
	}
}

// A [[dir]] path may name a file or a file glob — the pin for a folder
// that mixes kinds under names carrying no kind word (Elektron's 25
// Octatrack demos: "Ebass" is an 8-second loop beside "Mdkick", a hit).
// Matched against the full in-pack path; longer than its folder's entry,
// it wins the deepest-match rule; a folder entry never matches a file.
func TestHarvestDirEntryNamesAFile(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\",\"mdkick\"]\n[[instrument]]\nid=\"bass\"\nfamily=\"bass\"\naliases=[\"bass\",\"ebass\"]\n[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drums\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"hit\"]\n")
	write("annotations/vendors/el/vendor.toml", "[vendor]\nname=\"Elektron\"\nslug=\"elektron\"\n")
	write("annotations/vendors/el/packs/ot.toml", "[pack]\nname=\"OT\"\nslug=\"ot\"\ndir=\"OT\"\n"+
		"[[dir]]\npath=\"AUDIO/Elektron\"\ncategory=\"one-shots\"\ntags=[\"elektron\"]\n"+
		"[[dir]]\npath=\"AUDIO/Elektron/Ebass.wav\"\ncategory=\"loops\"\ntags=[\"demo\"]\n"+
		"[[dir]]\npath=\"AUDIO/Elektron/Acdrum.wav\"\ncategory=\"loops\"\ninstrument=\"drums\"\n"+
		"[[dir]]\npath=\"AUDIO/Textures\"\ncategory=\"loops\"\n"+
		"[[dir]]\npath=\"AUDIO/Textures/Chop *.wav\"\ncategory=\"one-shots\"\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Elektron/OT/AUDIO/Elektron/Ebass.wav", "e1"),        // the file entry beats the folder's pin
		mk("Elektron/OT/AUDIO/Elektron/Mdkick.wav", "e2"),       // the folder's pin still governs its siblings
		mk("Elektron/OT/AUDIO/Elektron/Acdrum.wav", "e3"),       // both facets pinned on one file
		mk("Elektron/OT/AUDIO/Textures/Chop 01.wav", "t1"),      // the file glob
		mk("Elektron/OT/AUDIO/Textures/Long Tail 01.wav", "t2"), // not matched by it
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	if _, err := Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	got := LoadMeta(ws, "src")
	want := func(p, cat, word, inst string) {
		m := got["Elektron/OT/AUDIO/"+p]
		w := ""
		if m.Why != nil && m.Why.Category != nil {
			w = m.Why.Category.Word
		}
		if m.Category != cat || w != word || m.Instrument != inst {
			t.Errorf("%s: got %s by %q inst %q, want %s by %q inst %q", p, m.Category, w, m.Instrument, cat, word, inst)
		}
	}
	want("Elektron/Ebass.wav", "loops", "AUDIO/Elektron/Ebass.wav", "bass")
	want("Elektron/Mdkick.wav", "one-shots", "AUDIO/Elektron", "kick")
	want("Elektron/Acdrum.wav", "loops", "AUDIO/Elektron/Acdrum.wav", "drums")
	want("Textures/Chop 01.wav", "one-shots", "AUDIO/Textures/Chop *.wav", "")
	want("Textures/Long Tail 01.wav", "loops", "AUDIO/Textures", "")
	if m := got["Elektron/OT/AUDIO/Elektron/Ebass.wav"]; len(m.Tags) != 2 || m.Tags[0] != "elektron" || m.Tags[1] != "demo" {
		t.Errorf("tags union along the chain, file entry included: %v", m.Tags)
	}
	if m := got["Elektron/OT/AUDIO/Elektron/Ebass.wav"]; m.Why.Category.Segment != "AUDIO/Elektron/Ebass.wav" {
		t.Errorf("why names the file the entry matched: %+v", m.Why.Category)
	}
}
