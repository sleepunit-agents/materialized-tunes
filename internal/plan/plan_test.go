package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/view"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

// testWorkspace builds a workspace with one local location "src" and a
// hand-written catalog, so plans run against fully controlled inputs.
func testWorkspace(t *testing.T, entries []catalog.Entry, files map[string]string) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = append(ws.Config.Locations,
		workspace.LocationConfig{Name: "src", Type: "local", Root: dir})
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	cat := map[string]catalog.Entry{}
	for _, e := range entries {
		e.ScannedAt = time.Now()
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// reload so the location config is live
	ws2, err := workspace.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws2
}

// wavEntry is a synthetic catalog entry whose Size is the canonical WAV
// size for its audio (header + data), so passthrough entries carry honest
// byte counts.
func wavEntry(path string, channels, rate, depth int, frames int64) catalog.Entry {
	data := frames * int64(channels) * int64(depth) / 8
	return catalog.Entry{
		Path: path, Size: wavHeaderBytes(depth) + data + data%2, SHA256: "aa" + path,
		Audio: &audio.Meta{
			Format: "wav", Channels: channels, SampleRate: rate, BitDepth: depth,
			Frames: frames, DurationS: float64(frames) / float64(rate),
		},
	}
}

const syntaktDevice = `name = "syntakt"
[audio]
format = "wav"
bit_depth = 16
sample_rate = 48000
channels = "mono"
max_duration_seconds = 5.0
[delivery]
mode = "staged"
`

const flatDevice = syntaktDevice + `layout = "flatten"
`

const quotaStorage = `name = "sq"
kind = "quota"
capacity_bytes = 33554432
max_files = 2
`

func writeView(t *testing.T, ws *workspace.Workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws.Root, "views", name+".toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeProfile(t *testing.T, ws *workspace.Workspace, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws.Root, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSizeMathAndDurationExclusion(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("kits/bd.wav", 2, 44100, 16, 44100),    // 1s stereo 44.1 → 1s mono 48k
		wavEntry("kits/long.wav", 1, 44100, 16, 308700), // 7s → excluded
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice)
	writeProfile(t, ws, "storage/sq.toml", quotaStorage)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="kits/**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(p.Entries))
	}
	e := p.Entries[0]
	// 44100 frames at 44.1k resampled to 48k = 48000 frames; mono 16-bit
	// = 96000 data bytes + 44 header.
	if e.OutFrames != 48000 || e.OutChannels != 1 || e.OutBytes != 96044 {
		t.Errorf("got frames=%d ch=%d bytes=%d", e.OutFrames, e.OutChannels, e.OutBytes)
	}
	if e.OutPath != "kits/bd.wav" {
		t.Errorf("out path = %q", e.OutPath)
	}
	if len(p.SkippedDuration) != 1 || p.SkippedDuration[0].Path != "kits/long.wav" {
		t.Errorf("duration exclusion missing: %+v", p.SkippedDuration)
	}
	if len(p.Errors) != 0 {
		t.Errorf("unexpected errors: %v", p.Errors)
	}
}

func TestPlanCollisionCaseInsensitiveAndExtension(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("kits/BD.wav", 1, 48000, 16, 1000),
		func() catalog.Entry {
			e := wavEntry("kits/bd.flac", 1, 48000, 16, 1000)
			e.Audio.Format = "flac"
			return e
		}(),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice)
	writeProfile(t, ws, "storage/sq.toml", quotaStorage)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="kits/**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	// BD.wav and bd.flac both render to (case-folded) kits/bd.wav → collision.
	if len(p.Collisions) != 1 {
		t.Fatalf("collisions = %+v, want exactly 1", p.Collisions)
	}
	if len(p.Errors) == 0 {
		t.Error("collision must be an error")
	}
}

func TestPlanQuotaSlotOverflowAndAsPrefix(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("packs/x/a.wav", 1, 48000, 16, 1000),
		wavEntry("packs/x/b.wav", 1, 48000, 16, 1000),
		wavEntry("packs/x/c.wav", 1, 48000, 16, 1000),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice)
	writeProfile(t, ws, "storage/sq.toml", quotaStorage) // max_files = 2
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="packs/x/**"
as="x"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if p.SlotsUsed != 3 || len(p.Errors) == 0 {
		t.Errorf("slots=%d errors=%v — want overflow error", p.SlotsUsed, p.Errors)
	}
	if p.Entries[0].OutPath != "x/a.wav" {
		t.Errorf("as-prefix mapping wrong: %q", p.Entries[0].OutPath)
	}
}

func TestPlanFilesystemClusterRoundingAndReserve(t *testing.T) {
	// One 1-frame file: 46 output bytes rounds up to one full 32 KiB cluster.
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("a.wav", 1, 44100, 16, 1),
	}, nil)
	writeProfile(t, ws, "devices/ot.toml", `name="ot"
[audio]
format="wav"
bit_depth=16
sample_rate=44100
channels="stereo"
`)
	// capacity 100 KiB, 10% reserve → 92160 usable; one cluster (32768) fits.
	writeProfile(t, ws, "storage/card.toml", `name="card"
kind="filesystem"
capacity_bytes=102400
cluster_bytes=32768
`)
	writeView(t, ws, "v", `name="v"
device="ot"
storage="card"
[[include]]
location="src"
glob="**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if p.TotalBytes != 46 || p.TotalOnDisk != 32768 {
		t.Errorf("bytes=%d onDisk=%d", p.TotalBytes, p.TotalOnDisk)
	}
	if p.UsableBytes != 92160 || !p.Fits {
		t.Errorf("usable=%d fits=%v", p.UsableBytes, p.Fits)
	}
}

func TestFlattenDisambiguation(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("packs/KitA/Kick 01.wav", 1, 48000, 16, 1000),
		wavEntry("packs/KitB/Kick 01.wav", 1, 48000, 16, 1000),
		wavEntry("packs/KitA/Snare 01.wav", 1, 48000, 16, 1000),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", flatDevice)
	writeProfile(t, ws, "storage/sq.toml", `name="sq"
kind="quota"
capacity_bytes=33554432
max_files=64
`)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="packs/**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", p.Errors)
	}
	var got []string
	for _, e := range p.Entries {
		got = append(got, e.OutPath)
	}
	want := []string{"KitA - Kick 01.wav", "KitB - Kick 01.wav", "Snare 01.wav"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("out[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFlattenTrueCollisionStillErrors(t *testing.T) {
	// Identical relative paths can't collide within one location, but the
	// same basename in the SAME directory under different case can.
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("packs/KitA/Kick.wav", 1, 48000, 16, 1000),
		wavEntry("packs/kita/KICK.wav", 1, 48000, 16, 1000),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", flatDevice)
	writeProfile(t, ws, "storage/sq.toml", `name="sq"
kind="quota"
capacity_bytes=33554432
max_files=64
`)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="packs/**"
`)
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Collisions) == 0 {
		t.Errorf("case-folded identical flat names must still collide, got entries %+v", p.Entries)
	}
}

func TestGlobRoot(t *testing.T) {
	cases := map[string]string{
		"a/b/**":        "a/b/",
		"a/*.wav":       "a/",
		"**":            "",
		"a/b c/d/**/*x": "a/b c/d/",
	}
	for glob, want := range cases {
		if got := view.GlobRoot(glob); got != want {
			t.Errorf("GlobRoot(%q) = %q, want %q", glob, got, want)
		}
	}
}

func TestSanitizeRewritesPathsAndSurvivesChecks(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("arps/C#1.wav", 1, 48000, 16, 1000),
		wavEntry("dr#ms/D#1.wav", 1, 48000, 16, 1000),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice+`[naming]
allowed_chars = "A-Za-z0-9 ._()-"
sanitize = { "#" = "s" }
`)
	writeProfile(t, ws, "storage/sq.toml", quotaStorage)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("sanitized plan should have no errors, got %v", p.Errors)
	}
	// Directories get rewritten too, and entries stay sorted post-rewrite.
	want := []string{"arps/Cs1.wav", "drsms/Ds1.wav"}
	for i, w := range want {
		if p.Entries[i].OutPath != w {
			t.Errorf("entry %d OutPath = %q, want %q", i, p.Entries[i].OutPath, w)
		}
	}
}

func TestSanitizeMergeCollisionErrors(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("x/C#1.wav", 1, 48000, 16, 1000),
		wavEntry("x/Cs1.wav", 1, 48000, 16, 1000),
	}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice+`[naming]
sanitize = { "#" = "s" }
`)
	writeProfile(t, ws, "storage/sq.toml", quotaStorage)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="x/**"
`)

	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Collisions) != 1 {
		t.Fatalf("sanitize merging two names must collide, got %+v", p.Collisions)
	}
}

// TestFormatTreeStrip: with annotations present, the vendor's format-tree
// level (SFM "WAV", Polyend "* 24 bit stereo") disappears from output
// paths under both location layouts; category dirs at pack root and
// unknown vendors are left alone; format_tree = "keep" mirrors; an include
// whose glob root already reaches into the tree is not stripped twice.
func TestFormatTreeStrip(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("Samples From Mars/808 From Mars/WAV/Kicks/BD 01.wav", 1, 48000, 16, 4800),
		wavEntry("Samples From Mars/808 From Mars/Ableton Live/BD 01.wav", 1, 48000, 16, 4800),
		wavEntry("Polyend/ASMR/ASMR 24 bit stereo/kit_a_1.wav", 1, 48000, 16, 4800),
		wavEntry("Polyend/Heights/Riser/rise 1.wav", 1, 48000, 16, 4800),  // no format level: category at root
		wavEntry("Rhythm Lab/Amen/Original/amen.wav", 1, 48000, 16, 4800), // canonical "." → nothing is a tree
		wavEntry("Nobody/Pack/WAV/x.wav", 1, 48000, 16, 4800),             // unknown vendor: mirrored
	}, map[string]string{
		"annotations/vendors/sfm/vendor.toml":            "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n[formats]\ncanonical_dir=\"WAV\"\nparallel_dirs=[\"Ableton Live*\", \"Kontakt*\"]\n",
		"annotations/vendors/polyend/vendor.toml":        "[vendor]\nname=\"Polyend\"\nslug=\"polyend\"\n[formats]\ncanonical_dir=\"* 24 bit stereo\"\nparallel_dirs=[\"* 16 bit mono\"]\n",
		"annotations/vendors/polyend/packs/heights.toml": "[pack]\nname=\"Heights\"\nslug=\"heights\"\ndir=\"Heights\"\n[[dir]]\npath=\"Riser\"\nrole=\"canonical-audio\"\ncategory=\"fx\"\n",
		"annotations/vendors/rhythm-lab/vendor.toml":     "[vendor]\nname=\"Rhythm Lab\"\nslug=\"rhythm-lab\"\n[formats]\ncanonical_dir=\".\"\n",
	})
	// the test workspace's location is flat; switch it to vendor-dirs
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice)
	writeProfile(t, ws, "storage/sq.toml", `name = "sq"
kind = "quota"
capacity_bytes = 33554432
`)
	writeView(t, ws, "v", `name="v"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="**"
`)
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range p.Entries {
		got[e.SourcePath] = e.OutPath
	}
	want := map[string]string{
		"Samples From Mars/808 From Mars/WAV/Kicks/BD 01.wav":    "Samples From Mars/808 From Mars/Kicks/BD 01.wav",
		"Samples From Mars/808 From Mars/Ableton Live/BD 01.wav": "Samples From Mars/808 From Mars/BD 01.wav",
		"Polyend/ASMR/ASMR 24 bit stereo/kit_a_1.wav":            "Polyend/ASMR/kit_a_1.wav",
		"Polyend/Heights/Riser/rise 1.wav":                       "Polyend/Heights/Riser/rise 1.wav",
		"Rhythm Lab/Amen/Original/amen.wav":                      "Rhythm Lab/Amen/Original/amen.wav",
		"Nobody/Pack/WAV/x.wav":                                  "Nobody/Pack/WAV/x.wav",
	}
	for src, out := range want {
		if got[src] != out {
			t.Errorf("%s → %q, want %q", src, got[src], out)
		}
	}
	if p.StrippedFormatTree != 3 {
		t.Errorf("stripped = %d, want 3", p.StrippedFormatTree)
	}

	// keep: verbatim mirror
	writeView(t, ws, "k", `name="k"
device="syntakt"
storage="sq"
format_tree="keep"
[[include]]
location="src"
glob="**"
`)
	p, err = Build(ws, "k")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Entries {
		if e.OutPath != e.SourcePath {
			t.Errorf("keep: %s → %s", e.SourcePath, e.OutPath)
		}
	}

	// glob root reaching into the tree, with `as`: the include owns that level
	writeView(t, ws, "a", `name="a"
device="syntakt"
storage="sq"
[[include]]
location="src"
glob="Samples From Mars/808 From Mars/WAV/**"
as="808"
`)
	p, err = Build(ws, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 1 || p.Entries[0].OutPath != "808/Kicks/BD 01.wav" || p.StrippedFormatTree != 0 {
		t.Errorf("as over tree: %+v stripped=%d", p.Entries, p.StrippedFormatTree)
	}
}

// TestDisplayAwareNaming: display_length warns about names a cropped
// device browser can't tell apart; rename = "distinguishing-first" moves
// the differing tokens to the front for exactly those names and leaves
// everything else alone.
func TestDisplayAwareNaming(t *testing.T) {
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("kit/BD A 808 Decay A 01.wav", 1, 48000, 16, 4800),
		wavEntry("kit/BD A 808 Decay A 02.wav", 1, 48000, 16, 4800),
		wavEntry("kit/BD A 808 Decay A 03.wav", 1, 48000, 16, 4800),
		wavEntry("kit/SD Short.wav", 1, 48000, 16, 4800),
		wavEntry("kit/SD Long.wav", 1, 48000, 16, 4800), // distinct within 8 chars ("SD Short" vs "SD Long")
	}, nil)
	writeProfile(t, ws, "storage/sq.toml", `name = "sq"
kind = "quota"
capacity_bytes = 33554432
`)
	// warn only
	writeProfile(t, ws, "devices/warn.toml", syntaktDevice+`layout = "flatten"
[naming]
display_length = 16
`)
	writeView(t, ws, "w", `name="w"
device="warn"
storage="sq"
[[include]]
location="src"
glob="**"
`)
	p, err := Build(ws, "w")
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayClashes != 3 || p.Renamed != 0 {
		t.Errorf("warn-only: clashes=%d renamed=%d", p.DisplayClashes, p.Renamed)
	}
	if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "distinguishing-first") {
		t.Errorf("expected a hint to enable the rename policy: %v", p.Warnings)
	}

	// rename policy
	writeProfile(t, ws, "devices/fix.toml", syntaktDevice+`layout = "flatten"
[naming]
display_length = 16
rename = "distinguishing-first"
`)
	writeView(t, ws, "f", `name="f"
device="fix"
storage="sq"
[[include]]
location="src"
glob="**"
`)
	p, err = Build(ws, "f")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range p.Entries {
		got[e.OutPath] = true
	}
	for _, want := range []string{"01 BD A 808 Decay A.wav", "02 BD A 808 Decay A.wav", "03 BD A 808 Decay A.wav", "SD Short.wav", "SD Long.wav"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if p.Renamed != 3 || p.DisplayClashes != 0 || len(p.Errors) != 0 {
		t.Errorf("renamed=%d clashes=%d errors=%v", p.Renamed, p.DisplayClashes, p.Errors)
	}
}

// TestDualMonoFold: a mono device folds a dual-mono source by taking one
// channel (no pad); a stereo-preserving device keeps it stereo unless
// audio.dual_mono = "fold", in which case size halves. Unknown verdicts
// (nil) never fold.
func TestDualMonoFold(t *testing.T) {
	yes, no := true, false
	dm := wavEntry("k/dm.wav", 2, 48000, 16, 4800)
	dm.Audio.DualMono = &yes
	st := wavEntry("k/st.wav", 2, 48000, 16, 4800)
	st.Audio.DualMono = &no
	unk := wavEntry("k/unk.wav", 2, 48000, 16, 4800)
	ws := testWorkspace(t, []catalog.Entry{dm, st, unk}, nil)
	writeProfile(t, ws, "storage/sq.toml", "name = \"sq\"\nkind = \"quota\"\ncapacity_bytes = 33554432\n")
	writeProfile(t, ws, "devices/mono.toml", syntaktDevice)
	writeProfile(t, ws, "devices/stereo.toml", "name=\"s\"\n[audio]\nformat=\"wav\"\nbit_depth=16\nsample_rate=48000\nchannels=\"stereo\"\n[delivery]\nmode=\"staged\"\n")
	writeProfile(t, ws, "devices/fold.toml", "name=\"f\"\n[audio]\nformat=\"wav\"\nbit_depth=16\nsample_rate=48000\nchannels=\"stereo\"\ndual_mono=\"fold\"\n[delivery]\nmode=\"staged\"\n")
	for _, d := range []string{"mono", "stereo", "fold"} {
		writeView(t, ws, d, "name=\""+d+"\"\ndevice=\""+d+"\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	}
	get := func(view, path string) Entry {
		p, err := Build(ws, view)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Entries {
			if e.SourcePath == path {
				return e
			}
		}
		t.Fatalf("%s: %s missing", view, path)
		return Entry{}
	}
	// mono device: everything folds; only the dual-mono one skips the pad
	if e := get("mono", "k/dm.wav"); e.OutChannels != 1 || !e.DualMono {
		t.Errorf("mono/dm: %+v", e)
	} else if ch, dmx := e.FoldSpec("sum-3db"); ch != "mono" || dmx != "left" {
		t.Errorf("mono/dm fold spec: %s %s", ch, dmx)
	}
	if e := get("mono", "k/st.wav"); e.OutChannels != 1 || e.DualMono {
		t.Errorf("mono/st: %+v", e)
	} else if _, dmx := e.FoldSpec("sum-3db"); dmx != "sum-3db" {
		t.Errorf("mono/st keeps device downmix, got %s", dmx)
	}
	if e := get("mono", "k/unk.wav"); e.DualMono {
		t.Errorf("unknown verdict must not fold as dual-mono: %+v", e)
	}
	// stereo device, default keep
	if e := get("stereo", "k/dm.wav"); e.OutChannels != 2 || e.DualMono {
		t.Errorf("stereo/keep: %+v", e)
	}
	// stereo device, fold: one channel, half the bytes of its stereo sibling
	fd, fs := get("fold", "k/dm.wav"), get("fold", "k/st.wav")
	if fd.OutChannels != 1 || !fd.DualMono || fs.OutChannels != 2 {
		t.Errorf("fold: dm=%+v st=%+v", fd, fs)
	}
	if fd.OutBytes-44 != (fs.OutBytes-44)/2 {
		t.Errorf("fold bytes: dm=%d st=%d", fd.OutBytes, fs.OutBytes)
	}
}

// TestDedupContent: identical bytes at two paths render once (first
// output path wins) only when the view opts in.
func TestDedupContent(t *testing.T) {
	a := wavEntry("kit/Kicks/BD 01.wav", 1, 48000, 16, 4800)
	b := wavEntry("kit/Kit A/BD 01.wav", 1, 48000, 16, 4800)
	b.SHA256 = a.SHA256 // same content, different path
	c := wavEntry("kit/Kicks/BD 02.wav", 1, 48000, 16, 4800)
	ws := testWorkspace(t, []catalog.Entry{a, b, c}, nil)
	writeProfile(t, ws, "devices/syntakt.toml", syntaktDevice)
	writeProfile(t, ws, "storage/sq.toml", "name = \"sq\"\nkind = \"quota\"\ncapacity_bytes = 33554432\n")
	writeView(t, ws, "keep", "name=\"keep\"\ndevice=\"syntakt\"\nstorage=\"sq\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	writeView(t, ws, "dd", "name=\"dd\"\ndevice=\"syntakt\"\nstorage=\"sq\"\ndedup=\"content\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "keep")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 3 || p.Deduped != 0 {
		t.Errorf("keep: %d entries, deduped=%d", len(p.Entries), p.Deduped)
	}
	p, err = Build(ws, "dd")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != 2 || p.Deduped != 1 {
		t.Fatalf("dedup: %d entries, deduped=%d", len(p.Entries), p.Deduped)
	}
	// "kit/Kicks/BD 01.wav" sorts before "kit/Kit A/BD 01.wav"? No: "Kicks" > "Kit A"
	// ('c' 0x63 > 't' 0x74 is false; 'c' < 't') — Kicks first. Either way: exactly one survives.
	names := map[string]bool{}
	for _, e := range p.Entries {
		names[e.OutPath] = true
	}
	if !names["kit/Kicks/BD 01.wav"] || names["kit/Kit A/BD 01.wav"] {
		t.Errorf("first-in-sort-order must win: %v", names)
	}
}

func TestPassthrough(t *testing.T) {
	dev := &profile.Device{}
	dev.Audio.Format, dev.Audio.BitDepth, dev.Audio.SampleRate, dev.Audio.Channels = "wav", 24, 48000, "stereo"

	match := wavEntry("a.wav", 2, 48000, 24, 100)
	if !Passthrough(dev, match) {
		t.Error("matching wav must pass through")
	}
	if got := ConvertedBytes(dev, match); got != match.Size {
		t.Errorf("passthrough bytes = source size: got %d want %d", got, match.Size)
	}
	if Passthrough(dev, wavEntry("mono.wav", 1, 48000, 24, 100)) != true {
		t.Error("mono source on a stereo-preserving device is still untouched → copy")
	}
	for name, ce := range map[string]catalog.Entry{
		"rate":  wavEntry("r.wav", 2, 44100, 24, 100),
		"depth": wavEntry("d.wav", 2, 48000, 16, 100),
		"float": wavEntry("f.wav", 2, 48000, 32, 100),
	} {
		if Passthrough(dev, ce) {
			t.Errorf("%s: must transcode", name)
		}
	}
	flac := wavEntry("x.flac", 2, 48000, 24, 100)
	flac.Audio.Format = "flac"
	if Passthrough(dev, flac) {
		t.Error("flac must transcode")
	}
	mono := &profile.Device{}
	mono.Audio.Format, mono.Audio.BitDepth, mono.Audio.SampleRate, mono.Audio.Channels = "wav", 16, 48000, "mono"
	if Passthrough(mono, wavEntry("s.wav", 2, 48000, 16, 100)) {
		t.Error("stereo source on a mono device folds → transcode")
	}
}
