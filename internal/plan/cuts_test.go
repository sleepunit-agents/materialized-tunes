package plan

import (
	"strconv"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
)

// polyendAnnotations is the real shape: one pack, one set of one-shots,
// three format trees — 24-bit stereo master and two cuts for the 16-bit
// hardware.
var polyendAnnotations = map[string]string{
	"annotations/vendors/polyend/vendor.toml": "[vendor]\nname=\"Polyend\"\nslug=\"polyend\"\n" +
		"[formats]\ncanonical_dir=\"* 24 bit stereo\"\nparallel_dirs=[\"* 16 bit mono\", \"* 16 bit stereo\"]\n",
}

// bassTools is Mids_Antidote_C.wav as the three cuts Polyend ships, all
// one second at 44.1k.
func bassTools() []catalog.Entry {
	return []catalog.Entry{
		wavEntry("Polyend/Bass Tools/Pack 24 bit stereo/MIDS/Mids_Antidote_C.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Bass Tools/Pack 16 bit stereo/MIDS/Mids_Antidote_C.wav", 2, 44100, 16, 44100),
		wavEntry("Polyend/Bass Tools/Pack 16 bit mono/MIDS/Mids_Antidote_C.wav", 1, 44100, 16, 44100),
	}
}

const bigStorage = `name = "sq"
kind = "quota"
capacity_bytes = 1073741824
`

// device is a minimal profile: the three knobs a cut is judged against.
func device(bitDepth, rate int, channels string) string {
	return "name = \"dev\"\n[audio]\nformat = \"wav\"\nbit_depth = " + strconv.Itoa(bitDepth) +
		"\nsample_rate = " + strconv.Itoa(rate) + "\nchannels = \"" + channels + "\"\n[delivery]\nmode = \"staged\"\n"
}

// cutPlan builds a plan over the Polyend cut set for one device.
func cutPlan(t *testing.T, dev string, extra ...string) *Plan {
	t.Helper()
	ws := testWorkspace(t, bassTools(), polyendAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", dev)
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n"+strings.Join(extra, "\n")+"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPickCutsDeliversMost: a DAW library takes everything the pack has,
// so the 24-bit stereo master wins and the two 16-bit cuts are dropped —
// the collision Jonathan hit on Bass Tools resolves instead of erroring.
func TestPickCutsDeliversMost(t *testing.T) {
	p := cutPlan(t, device(24, 44100, "stereo"))
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(p.Entries), p.Entries)
	}
	e := p.Entries[0]
	if want := "Polyend/Bass Tools/MIDS/Mids_Antidote_C.wav"; e.OutPath != want {
		t.Errorf("out %q, want %q", e.OutPath, want)
	}
	if !strings.Contains(e.SourcePath, "24 bit stereo") {
		t.Errorf("kept %q, want the 24-bit stereo cut", e.SourcePath)
	}
	if p.CutsDropped != 2 {
		t.Errorf("dropped %d, want 2", p.CutsDropped)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "Pack 24 bit stereo") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

// TestPickCutsPrefersNoWork: on the 16-bit mono hardware the vendor cut
// these for, all three deliver the same mono 16-bit result — so the cut
// that copies byte-for-byte wins over two that would need ffmpeg to
// arrive at the same place.
func TestPickCutsPrefersNoWork(t *testing.T) {
	p := cutPlan(t, device(16, 44100, "mono"))
	if len(p.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(p.Entries))
	}
	e := p.Entries[0]
	if !strings.Contains(e.SourcePath, "16 bit mono") {
		t.Errorf("kept %q, want the 16-bit mono cut", e.SourcePath)
	}
	if !e.Copy {
		t.Error("kept cut should copy without transcoding")
	}
}

// TestPickCutsStereo16: a 16-bit stereo device delivers 16 bits either
// way, so the vendor's own 16-bit stereo cut beats down-converting the
// master — same result, no transcode.
func TestPickCutsStereo16(t *testing.T) {
	p := cutPlan(t, device(16, 44100, "stereo"))
	if len(p.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(p.Entries))
	}
	if e := p.Entries[0]; !strings.Contains(e.SourcePath, "16 bit stereo") || !e.Copy {
		t.Errorf("kept %q (copy=%v), want the 16-bit stereo cut copied", e.SourcePath, e.Copy)
	}
}

// TestPickCutsOptOut: cuts = "all" renders every cut, and under a
// stripped format tree that is the collision it always was.
func TestPickCutsOptOut(t *testing.T) {
	p := cutPlan(t, device(24, 44100, "stereo"), `cuts="all"`)
	if p.CutsDropped != 0 || len(p.Entries) != 3 {
		t.Fatalf("dropped %d, %d entries — want 0 and 3", p.CutsDropped, len(p.Entries))
	}
	if len(p.Collisions) != 1 {
		t.Errorf("collisions %v, want the untouched collision", p.Collisions)
	}
}

// TestPickCutsKeepsLongestUnequalLength: the collision Jonathan hit on
// Thump. Its three format trees ship as separately produced zips, so the
// trims drift by more than a millisecond — a fact about the vendor's
// render pipeline, not about the content. The vendor's own structure
// (three declared trees, one relative path) says these are one sample,
// so length picks instead of blocking: the longest render is kept, and
// the trim is reported.
func TestPickCutsKeepsLongestUnequalLength(t *testing.T) {
	entries := []catalog.Entry{
		wavEntry("Polyend/Thump/Thump 24 bit stereo/artificial/kick_thick.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Thump/Thump 16 bit stereo/artificial/kick_thick.wav", 2, 44100, 16, 44000), // ~2ms short
		wavEntry("Polyend/Thump/Thump 16 bit mono/artificial/kick_thick.wav", 1, 44100, 16, 44010),
	}
	ws := testWorkspace(t, entries, polyendAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if len(p.Entries) != 1 || p.CutsDropped != 2 {
		t.Fatalf("%d entries, %d dropped — want 1 and 2", len(p.Entries), p.CutsDropped)
	}
	if e := p.Entries[0]; !strings.Contains(e.SourcePath, "24 bit stereo") {
		t.Errorf("kept %q, want the longest render", e.SourcePath)
	}
	if joined := strings.Join(p.Warnings, "\n"); !strings.Contains(joined, "trimmed to a different length") {
		t.Errorf("warnings %v, want the trims named", p.Warnings)
	}
}

// TestIsCutSetNeedsOneRelPath: the structural proof is the same relative
// path inside each tree — two different files that happen to land on one
// output path are not cuts of each other. The extension is not part of
// the coordinate: a re-render into another container is still the same
// recording.
func TestIsCutSetNeedsOneRelPath(t *testing.T) {
	set := []Entry{
		{SourcePath: "Polyend/Thump/Thump 24 bit stereo/acoustic/kick_a.wav", pack: "Polyend/Thump", tree: "Thump 24 bit stereo"},
		{SourcePath: "Polyend/Thump/Thump 16 bit mono/artificial/kick_a.wav", pack: "Polyend/Thump", tree: "Thump 16 bit mono"},
	}
	if isCutSet(set, []int{0, 1}) {
		t.Error("different relative paths inside their trees — not a cut set")
	}
	set[1].SourcePath = "Polyend/Thump/Thump 16 bit mono/ACOUSTIC/Kick_A.aif"
	if !isCutSet(set, []int{0, 1}) {
		t.Error("same coordinate up to case and extension — is a cut set")
	}
}

// TestPickCutsSameTreeStillCollides: two different files inside ONE format
// tree that render to the same path are a genuine collision — the cut
// resolver only ever chooses between trees.
func TestPickCutsSameTreeStillCollides(t *testing.T) {
	entries := []catalog.Entry{
		wavEntry("Polyend/Bass Tools/Pack 24 bit stereo/MIDS/x.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Bass Tools/Pack 24 bit stereo/mids/x.wav", 2, 44100, 24, 44100),
	}
	ws := testWorkspace(t, entries, polyendAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if p.CutsDropped != 0 {
		t.Errorf("dropped %d — two files in one tree are not cuts of each other", p.CutsDropped)
	}
	if len(p.Collisions) != 1 {
		t.Errorf("collisions %v, want 1", p.Collisions)
	}
}

// TestPickCutsCountersDescribeOutput: the plan's counters and warnings
// answer questions about what materializes. A pack that ships its library
// three ways would otherwise report every unplaced file three times.
func TestPickCutsCountersDescribeOutput(t *testing.T) {
	files := map[string]string{}
	for k, v := range polyendAnnotations {
		files[k] = v
	}
	// harvested, but "MIDS" names no instrument the lexicon knows — the
	// three cuts land unsorted, and only the kept one may be counted.
	var meta []string
	for _, e := range bassTools() {
		meta = append(meta, `{"sha":"`+e.SHA256+`"}`)
	}
	files["annotations-cache/meta/src.jsonl"] = strings.Join(meta, "\n") + "\n"

	ws := testWorkspace(t, bassTools(), files)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", `name="v"
device="dev"
storage="sq"
layout="{family}/{instrument}/{category}/{pack}/{file}"
[[include]]
location="src"
glob="**"
`)
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(p.Entries))
	}
	if p.Unsorted != 1 { // "MIDS" is not an instrument the lexicon knows
		t.Errorf("unsorted = %d, want 1 — counters must describe the surviving cut", p.Unsorted)
	}
	if p.StrippedFormatTree != 1 {
		t.Errorf("stripped_format_tree = %d, want 1", p.StrippedFormatTree)
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "1 file carry no instrument label") &&
		!strings.Contains(joined, "1 file ") {
		t.Errorf("warnings should count the output, got: %v", p.Warnings)
	}
}

// sfmAnnotations is the other real shape: Samples From Mars re-exports its
// whole library once per sampler (Battery, Maschine, Kontakt, MPC …)
// beside the canonical WAV tree, so the parallel trees hold the same hits
// re-rendered — same names, different bytes, trims a few frames apart.
var sfmAnnotations = map[string]string{
	"annotations/vendors/samples-from-mars/vendor.toml": "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n" +
		"[formats]\ncanonical_dir=\"WAV\"\nparallel_role=\"reexport\"\nparallel_dirs=[\"Maschine\", \"Battery\"]\n",
}

// sfmPlan builds a plan over one pack's sampler trees.
func sfmPlan(t *testing.T, entries []catalog.Entry) *Plan {
	t.Helper()
	ws := testWorkspace(t, entries, sfmAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPickCutsReexportKeepsLongest: the collision Jonathan hit on 727 From
// Mars. Battery and Maschine hold the same agogo hit, trimmed differently
// — for a re-export vendor equal length is not the proof of redundancy,
// the two parallel trees are, and the longest render is the safe keep.
func TestPickCutsReexportKeepsLongest(t *testing.T) {
	const dir = "Samples From Mars/727 From Mars/"
	const rest = "/727 From Mars/Assorted 1 Samples/Agogo Hi 727 25.wav"
	p := sfmPlan(t, []catalog.Entry{
		wavEntry(dir+"Battery"+rest, 2, 44100, 16, 22050),
		wavEntry(dir+"Maschine"+rest, 2, 44100, 16, 21000), // trimmed shorter
	})
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if len(p.Entries) != 1 || p.CutsDropped != 1 {
		t.Fatalf("%d entries, %d dropped — want 1 and 1", len(p.Entries), p.CutsDropped)
	}
	if e := p.Entries[0]; !strings.Contains(e.SourcePath, "Battery") {
		t.Errorf("kept %q, want the longer Battery render", e.SourcePath)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "trimmed to a different length") {
		t.Errorf("warnings %v, want the trim named", p.Warnings)
	}
}

// TestPickCutsReexportStillNeedsTwoTrees: parallel_role relaxes the length
// proof, not the structural one. Two files that collide from inside one
// tree are a real collision for any vendor.
func TestPickCutsReexportStillNeedsTwoTrees(t *testing.T) {
	p := sfmPlan(t, []catalog.Entry{
		wavEntry("Samples From Mars/727 From Mars/Battery/Kit/Agogo.wav", 2, 44100, 16, 22050),
		wavEntry("Samples From Mars/727 From Mars/Battery/kit/Agogo.wav", 2, 44100, 16, 21000),
	})
	if p.CutsDropped != 0 {
		t.Errorf("dropped %d — two files in one tree are not re-exports of each other", p.CutsDropped)
	}
	if len(p.Collisions) != 1 {
		t.Errorf("collisions %v, want 1", p.Collisions)
	}
}

// TestPickCutsHeuristicTrees: "Kit Name/Kit Name 16 bit mono" reads as a
// format tree by naming alone — a vendor nobody has annotated still sheds
// its format level and its cut set resolves, which is the "across the
// board" half of the Thump ask.
func TestPickCutsHeuristicTrees(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("Nobody/Boom Kit/Boom Kit 24 bit stereo/kick.wav", 2, 44100, 24, 44100),
		wavEntry("Nobody/Boom Kit/Boom Kit 16 bit stereo/kick.wav", 2, 44100, 16, 44100),
		wavEntry("Nobody/Boom Kit/Boom Kit 16-Bit_Mono/kick.wav", 1, 44100, 16, 44100),
	}, nil)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if len(p.Entries) != 1 || p.CutsDropped != 2 {
		t.Fatalf("%d entries, %d dropped — want 1 and 2", len(p.Entries), p.CutsDropped)
	}
	e := p.Entries[0]
	if want := "Nobody/Boom Kit/kick.wav"; e.OutPath != want {
		t.Errorf("out %q, want %q", e.OutPath, want)
	}
	if !strings.Contains(e.SourcePath, "24 bit stereo") {
		t.Errorf("kept %q, want the 24-bit stereo cut", e.SourcePath)
	}
}

// TestHeuristicTreeRank pins the naming rule's edges: format words after
// the pack's own name are a tree; words that carry meaning are not; a
// bare "WAV" or a number alone is not claimed.
func TestHeuristicTreeRank(t *testing.T) {
	claims := []struct {
		pack, dir string
		ok        bool
	}{
		{"Thump", "Thump 16 bit mono", true},
		{"Thump", "Thump 24 bit stereo", true},
		{"Bass Tools", "Pack 16 bit stereo", true}, // Polyend's literal "Pack"
		{"808 Kit", "808 Kit 16-Bit WAV", true},
		{"808 Kit", "808 Kit 16bit 44.1khz Stereo", true},
		{"Thump", "Thump", false},            // the pack's own name is no tree
		{"Thump", "Kicks mono", false},       // "Kicks" means something
		{"Thump", "artificial", false},       // category dir
		{"Pack", "WAV", false},               // no anchor — annotate the vendor instead
		{"Amen", "Original", false},          // Rhythm Lab's content dirs
		{"Loops", "Bass Lines 166.5", false}, // a BPM-suffixed loop dir
	}
	for _, c := range claims {
		if _, ok := heuristicTreeRank(c.pack, c.dir); ok != c.ok {
			t.Errorf("heuristicTreeRank(%q, %q) = %v, want %v", c.pack, c.dir, ok, c.ok)
		}
	}
	r24, _ := heuristicTreeRank("K", "K 24 bit stereo")
	r16s, _ := heuristicTreeRank("K", "K 16 bit stereo")
	r16m, _ := heuristicTreeRank("K", "K 16 bit mono")
	if !(r24 < r16s && r16s < r16m) {
		t.Errorf("rank order %d %d %d, want 24-bit stereo closest to canonical", r24, r16s, r16m)
	}
}

// cutPlanEntries is cutPlan over an arbitrary entry set.
func cutPlanEntries(t *testing.T, entries []catalog.Entry) *Plan {
	t.Helper()
	ws := testWorkspace(t, entries, polyendAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPickCutsWarningCountsSamples: the warning's "N samples shipped in
// more than one cut" counts samples, not the trees the picks came from.
// Two samples resolved from one pack is "2 samples" even though every
// keep came out of the same tree — Jonathan read "12 samples" on a plan
// that had resolved 2367 of them, because the count was len(trees).
func TestPickCutsWarningCountsSamples(t *testing.T) {
	entries := append(bassTools(),
		wavEntry("Polyend/Bass Tools/Pack 24 bit stereo/MIDS/Mids_Broke_F.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Bass Tools/Pack 16 bit stereo/MIDS/Mids_Broke_F.wav", 2, 44100, 16, 44100),
		wavEntry("Polyend/Bass Tools/Pack 16 bit mono/MIDS/Mids_Broke_F.wav", 1, 44100, 16, 44100),
	)
	p := cutPlanEntries(t, entries)
	if p.CutsDropped != 4 || len(p.Entries) != 2 {
		t.Fatalf("dropped %d, %d entries — want 4 and 2", p.CutsDropped, len(p.Entries))
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "2 samples shipped in more than one cut") {
		t.Errorf("warning should count 2 samples, got: %v", p.Warnings)
	}
}

// TestSplitCutsDetectsDivergentPaths: the Fractured tally hole. When a
// format tree files a sample under its own idea of the folder ("Toms"
// where the stereo trees say "Tom"), that cut lands on an output path of
// its own — the resolver, which groups by output path, never sees the
// three as one sample, so the stray cut ships alongside the kept one with
// no collision to say so. The plan must say so instead.
func TestSplitCutsDetectsDivergentPaths(t *testing.T) {
	entries := []catalog.Entry{
		wavEntry("Polyend/Fractured/Fractured 24 bit stereo/Tom/Perc_Tom_Bloat.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Fractured/Fractured 16 bit stereo/Tom/Perc_Tom_Bloat.wav", 2, 44100, 16, 44100),
		wavEntry("Polyend/Fractured/Fractured 16 bit mono/Toms/Perc_Tom_Bloat.wav", 1, 44100, 16, 44100),
	}
	p := cutPlanEntries(t, entries)
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if p.CutsDropped != 1 || len(p.Entries) != 2 {
		t.Fatalf("dropped %d, %d entries — want 1 and 2 (the stray mono cut survives)", p.CutsDropped, len(p.Entries))
	}
	if p.CutsSplit != 1 {
		t.Fatalf("cuts_split = %d, want 1", p.CutsSplit)
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "1 sample still ships in more than one cut") ||
		!strings.Contains(joined, "Toms/Perc_Tom_Bloat.wav") {
		t.Errorf("warning should name the split and the stray path, got: %v", p.Warnings)
	}
}

// TestSplitCutsIgnoresRepeatedNames: "Kick_01" existing in two kits of
// one tree is two samples sharing a name, not one sample in cuts — the
// split warning must stay silent.
func TestSplitCutsIgnoresRepeatedNames(t *testing.T) {
	entries := []catalog.Entry{
		wavEntry("Polyend/Fractured/Fractured 24 bit stereo/Kit A/Kick_01.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Fractured/Fractured 24 bit stereo/Kit B/Kick_01.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Fractured/Fractured 16 bit mono/Kit A/Kick_01.wav", 1, 44100, 16, 44100),
	}
	p := cutPlanEntries(t, entries)
	if p.CutsSplit != 0 {
		t.Errorf("cuts_split = %d, want 0 — a repeated name inside one tree disqualifies the group: %v", p.CutsSplit, p.Warnings)
	}
}
