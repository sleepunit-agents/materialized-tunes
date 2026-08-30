package materialize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbarket/materialized-tunes/internal/ableton"
	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/view"
)

// Swapped destinations (a→b while b→a) must both survive: the two-phase
// rename is the guarantee that no move lands on a file that hasn't left.
func TestMigrateSwapsSafely(t *testing.T) {
	ws := testWorkspace(t, nil)
	target := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(target, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("A/one.wav", "content-one")
	write("B/one.wav", "content-two!")

	l := &lock.Lock{View: "v", Entries: []lock.Entry{
		{Source: lock.Source{Location: "src", Path: "p/a.wav", SHA256: "aa"}, Transform: lock.Transform{Copy: true}, Output: lock.Output{Path: "A/one.wav", Bytes: 11}},
		{Source: lock.Source{Location: "src", Path: "p/b.wav", SHA256: "bb"}, Transform: lock.Transform{Copy: true}, Output: lock.Output{Path: "B/one.wav", Bytes: 12}},
	}}
	p := &plan.Plan{View: &view.View{Name: "v"}, Entries: []plan.Entry{
		{Location: "src", SourcePath: "p/a.wav", SHA256: "aa", Copy: true, OutPath: "B/one.wav"},
		{Location: "src", SourcePath: "p/b.wav", SHA256: "bb", Copy: true, OutPath: "A/one.wav"},
	}}
	m := lock.PlanMigration(l, p)
	if len(m.Moves) != 2 {
		t.Fatalf("moves = %+v", m.Moves)
	}
	out, err := Migrate(context.Background(), ws, l, p, m, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Renamed != 2 || len(out.Skipped) != 0 {
		t.Fatalf("outcome: %+v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "B", "one.wav")); string(b) != "content-one" {
		t.Errorf("B/one.wav = %q, want the bytes that lived at A", b)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "A", "one.wav")); string(b) != "content-two!" {
		t.Errorf("A/one.wav = %q, want the bytes that lived at B", b)
	}
	l2, err := lock.Read(out.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l2.Entries {
		want := map[string]string{"p/a.wav": "B/one.wav", "p/b.wav": "A/one.wav"}[e.Source.Path]
		if e.Output.Path != want {
			t.Errorf("lock %s → %s, want %s", e.Source.Path, e.Output.Path, want)
		}
	}
}

// A vanished old file and a size-drifted one are both left for
// materialize; the healthy sibling still moves and its emptied dir goes.
func TestMigrateSkipsDriftAndPrunes(t *testing.T) {
	ws := testWorkspace(t, nil)
	target := t.TempDir()
	os.MkdirAll(filepath.Join(target, "Old", "deep"), 0o755)
	os.WriteFile(filepath.Join(target, "Old", "deep", "ok.wav"), []byte("okok"), 0o644)
	os.WriteFile(filepath.Join(target, "Old", "edited.wav"), []byte("user made this longer"), 0o644)

	l := &lock.Lock{View: "v", Entries: []lock.Entry{
		{Source: lock.Source{Location: "src", Path: "ok.wav", SHA256: "aa"}, Transform: lock.Transform{Copy: true}, Output: lock.Output{Path: "Old/deep/ok.wav", Bytes: 4}},
		{Source: lock.Source{Location: "src", Path: "edited.wav", SHA256: "bb"}, Transform: lock.Transform{Copy: true}, Output: lock.Output{Path: "Old/edited.wav", Bytes: 4}},
		{Source: lock.Source{Location: "src", Path: "gone.wav", SHA256: "cc"}, Transform: lock.Transform{Copy: true}, Output: lock.Output{Path: "Old/gone.wav", Bytes: 4}},
	}}
	p := &plan.Plan{View: &view.View{Name: "v"}, Entries: []plan.Entry{
		{Location: "src", SourcePath: "ok.wav", SHA256: "aa", Copy: true, OutPath: "New/ok.wav"},
		{Location: "src", SourcePath: "edited.wav", SHA256: "bb", Copy: true, OutPath: "New/edited.wav"},
		{Location: "src", SourcePath: "gone.wav", SHA256: "cc", Copy: true, OutPath: "New/gone.wav"},
	}}
	out, err := Migrate(context.Background(), ws, l, p, lock.PlanMigration(l, p), target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Renamed != 1 || len(out.Skipped) != 2 {
		t.Fatalf("outcome: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(target, "New", "ok.wav")); err != nil {
		t.Error("ok.wav did not move")
	}
	if _, err := os.Stat(filepath.Join(target, "Old", "deep")); !os.IsNotExist(err) {
		t.Error("emptied Old/deep should be pruned")
	}
	if _, err := os.Stat(filepath.Join(target, "Old", "edited.wav")); err != nil {
		t.Error("size-drifted file must be left in place — it is not ours any more")
	}
	l2, err := lock.Read(out.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, e := range l2.Entries {
		paths[e.Source.Path] = e.Output.Path
	}
	if paths["ok.wav"] != "New/ok.wav" || paths["edited.wav"] != "Old/edited.wav" || paths["gone.wav"] != "Old/gone.wav" {
		t.Errorf("lock paths: %+v (only the executed move may change)", paths)
	}
}

// End to end: materialize under one prefix, change the recipe's `as`,
// migrate — samples rename, the companion re-renders with refs following,
// the old copy and tree go, and the new lock diffs clean.
func TestMigrateEndToEnd(t *testing.T) {
	wav := make([]byte, 68+480*2*3)
	aif := []byte("aiff bytes")
	rack := ableton.Encode([]byte(rackXML))
	ws := testWorkspace(t, map[string]string{
		"Big Pack/Kicks/Kick 01.wav":   string(wav),
		"Big Pack/Snares/Snare 01.aif": string(aif),
		"Big Pack/Racks/Big Kit.adg":   string(rack),
	})
	meta := &audio.Meta{Format: "wav", Channels: 2, SampleRate: 48000, BitDepth: 24, Frames: 480, DurationS: 0.01}
	cat := map[string]catalog.Entry{}
	for p, e := range map[string]catalog.Entry{
		"Big Pack/Kicks/Kick 01.wav":   {Size: int64(len(wav)), SHA256: sha(wav), Audio: meta},
		"Big Pack/Snares/Snare 01.aif": {Size: int64(len(aif)), SHA256: sha(aif), Audio: meta},
		"Big Pack/Racks/Big Kit.adg":   {Size: int64(len(rack)), SHA256: sha(rack)},
	} {
		e.Path, e.ScannedAt = p, time.Now()
		cat[p] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	write := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(ws.Root, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("devices/live.toml", `name = "live"
[audio]
format = "wav"
bit_depth = 24
sample_rate = 48000
channels = "stereo"
[delivery]
mode = "staged"
[companions]
types = ["adg"]
`)
	write("storage/big.toml", "name = \"big\"\nkind = \"quota\"\ncapacity_bytes = 1073741824\n")
	recipe := `name = "samples"
device = "live"
storage = "big"
format_tree = "keep"
[[include]]
location = "src"
glob = "Big Pack/**"
as = "%s/Big Pack"
`
	write("views/samples.toml", strings.Replace(recipe, "%s", "SPLICE", 1))

	p, err := plan.Build(ws, "samples")
	if err != nil || len(p.Errors) > 0 {
		t.Fatalf("plan: %v %v", err, p.Errors)
	}
	target := t.TempDir()
	if _, err := Materialize(context.Background(), ws, p, target, nil); err != nil {
		t.Fatal(err)
	}

	write("views/samples.toml", strings.Replace(recipe, "%s", "NEW", 1))
	p2, err := plan.Build(ws, "samples")
	if err != nil || len(p2.Errors) > 0 {
		t.Fatalf("plan2: %v %v", err, p2.Errors)
	}
	lp, err := lock.Resolve(ws.Root, "samples")
	if err != nil {
		t.Fatal(err)
	}
	l, err := lock.Read(lp)
	if err != nil {
		t.Fatal(err)
	}
	m := lock.PlanMigration(l, p2)
	if len(m.Moves) != 2 || len(m.Companions) != 1 || m.Pending != 0 {
		t.Fatalf("migration: moves=%d companions=%d pending=%d", len(m.Moves), len(m.Companions), m.Pending)
	}
	out, err := Migrate(context.Background(), ws, l, p2, m, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Renamed != 2 || out.Rewritten != 1 || len(out.Skipped) != 0 {
		t.Fatalf("outcome: %+v", out)
	}

	if _, err := os.Stat(filepath.Join(target, "SPLICE")); !os.IsNotExist(err) {
		t.Error("old SPLICE tree should be fully pruned")
	}
	got, err := os.ReadFile(filepath.Join(target, "NEW", "Big Pack", "Racks", "Big Kit.adg"))
	if err != nil {
		t.Fatal("companion missing at new path:", err)
	}
	xmlBytes, err := ableton.Decode(got)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(xmlBytes); !strings.Contains(s, "NEW/Big Pack/Kicks/Kick 01.wav") || strings.Contains(s, "SPLICE/") {
		t.Errorf("companion refs must follow the move:\n%s", s)
	}

	l2, err := lock.Read(out.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	shas := map[string]map[string]string{"src": {
		"Big Pack/Kicks/Kick 01.wav": sha(wav), "Big Pack/Snares/Snare 01.aif": sha(aif), "Big Pack/Racks/Big Kit.adg": sha(rack),
	}}
	if d := lock.Compute(l2, p2, shas); !d.Clean() {
		t.Fatalf("post-migrate diff not clean: %+v", d)
	}
	if mg := lock.PlanMigration(l2, p2); mg.Work() != 0 {
		t.Fatalf("second migrate should find nothing: %+v", mg)
	}
}
