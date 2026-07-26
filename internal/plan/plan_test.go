package plan

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
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

func wavEntry(path string, channels, rate, depth int, frames int64) catalog.Entry {
	return catalog.Entry{
		Path: path, Size: 1000, SHA256: "aa" + path,
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
