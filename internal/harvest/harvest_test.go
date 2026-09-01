package harvest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
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
		// looks notey but isn't: lowercase take names, too few pitches
		mk("Nobody/Pack2/kits dir/kit_a_1.wav", "k1"),
		mk("Nobody/Pack2/kits dir/kit_a_2.wav", "k2"),
		mk("Nobody/Pack2/kits dir/kit_b_1.wav", "k3"),
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
	if res.Files != 16 {
		t.Errorf("files with metadata = %d, want 16", res.Files)
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
	if m := got["s11"]; m.Instrument != "break" || m.Family != "drums" || m.Category != "loops" {
		// a jungle groove named after its source must not read as sub bass
		t.Errorf("s11: got instrument=%q family=%q cat=%q, want break/drums/loops", m.Instrument, m.Family, m.Category)
	}
	if _, ok := got["s7"]; ok {
		t.Error("s7 has nothing to harvest and must be absent")
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
