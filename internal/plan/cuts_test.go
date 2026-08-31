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

// TestPickCutsRefusesUnequalLength: two files of different durations
// under one name are different recordings, whatever tree they sit in.
// Dropping one would lose audio, so the collision stands.
func TestPickCutsRefusesUnequalLength(t *testing.T) {
	entries := []catalog.Entry{
		wavEntry("Polyend/Bass Tools/Pack 24 bit stereo/MIDS/x.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Bass Tools/Pack 16 bit mono/MIDS/x.wav", 1, 44100, 16, 88200), // twice as long
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
		t.Errorf("dropped %d cuts of unequal length — should have kept both", p.CutsDropped)
	}
	if len(p.Collisions) != 1 {
		t.Errorf("collisions %v, want 1", p.Collisions)
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
