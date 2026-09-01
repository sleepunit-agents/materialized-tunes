package correct

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

func fixture(t *testing.T) (*workspace.Workspace, workspace.LocationConfig, map[string]catalog.Entry) {
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
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n[[instrument]]\nid=\"hat\"\nfamily=\"drums\"\naliases=[\"hat\"]\n[[instrument]]\nid=\"bass\"\nfamily=\"bass\"\naliases=[\"bass\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"hit\",\"hits\"]\n")
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n")
	write("annotations/vendors/sfm/packs/dt.toml", "[pack]\nname=\"Drumtrax From Mars\"\nslug=\"drumtrax-from-mars\"\ndir=\"Drumtrax From Mars\"\n")
	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 01.wav", "1"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 02.wav", "2"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/Hits/Hat Drumtrax 01.wav", "3"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/Fills/Fill 07.wav", "4"),
		mk("Samples From Mars/Drumtrax From Mars/WAV/Fills/Kick Loop 07.wav", "5"),
		mk("My Drums/Field Kit/Snap 1.wav", "6"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	lc := ws.Config.Locations[0]
	if _, err := harvest.Run(ws, lc); err != nil {
		t.Fatal(err)
	}
	return ws, lc, cat
}

func TestResolve(t *testing.T) {
	ws, lc, _ := fixture(t)
	vendors, _ := annotations.Load(ws.AnnotationRoots()...)
	tg, err := Resolve(lc, vendors, "Samples From Mars/Drumtrax From Mars/WAV/Fills")
	if err != nil {
		t.Fatal(err)
	}
	if tg.VendorSlug != "samples-from-mars" || tg.PackSlug != "drumtrax-from-mars" || tg.InPack != "WAV/Fills" || tg.NewPack || tg.File != "vendors/samples-from-mars/packs/drumtrax-from-mars.toml" {
		t.Errorf("known pack: %+v", tg)
	}
	tg, err = Resolve(lc, vendors, "My Drums/Field Kit/")
	if err != nil {
		t.Fatal(err)
	}
	if !tg.NewVendor || !tg.NewPack || tg.VendorSlug != "my-drums" || tg.PackSlug != "field-kit" || tg.InPack != "" {
		t.Errorf("unknown vendor: %+v", tg)
	}
	if _, err := Resolve(lc, vendors, "Samples From Mars/*/WAV"); err == nil {
		t.Error("a glob over the pack segment must be refused")
	}
}

// A correction shows its blast radius before it is written, and the
// radius separates files it fills in from files it would move.
func TestPreviewAndApply(t *testing.T) {
	ws, lc, cat := fixture(t)
	vendors, _ := annotations.Load(ws.AnnotationRoots()...)
	current := harvest.LoadMeta(ws, "src")

	// kind A: the Fills folder holds loops; one file already says so
	r, err := Preview(ws, lc, cat, current, vendors, Correction{Location: "src", Path: "Samples From Mars/Drumtrax From Mars/WAV/Fills", Facet: "category", Value: "loops"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Covered != 2 || r.Changed != 1 || r.Filled != 1 || r.Moved != 0 {
		t.Errorf("fills radius: %+v", r)
	}
	if len(r.Changes) != 1 || r.Changes[0].From != "— · —" || r.Changes[0].To != "loops · —" {
		t.Errorf("changes: %+v", r.Changes)
	}
	// nothing was written by a preview
	if _, err := os.Stat(ws.LocalAnnotations()); !os.IsNotExist(err) {
		t.Error("preview must not write")
	}

	// kind D: inside this pack, "Bass" means kick — an alias, pack-wide
	r, err = Apply(ws, lc, cat, current, vendors, Correction{Location: "src", Path: "Samples From Mars/Drumtrax From Mars", Facet: "alias", Word: "bass", Value: "kick", Note: "Drumtrax calls its kick Bass"}, Provenance{AppVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Covered != 5 || r.Changed != 2 || r.Moved != 2 {
		t.Errorf("alias radius: %+v", r)
	}
	// the local pack file carries the block, the repo's layout, provenance
	data, err := os.ReadFile(filepath.Join(ws.LocalAnnotations(), "vendors", "samples-from-mars", "packs", "drumtrax-from-mars.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"[[instrument]]", "id = \"kick\"", "aliases = [\"bass\"]", "observed = \"" + time.Now().Format("2006-01-02") + "\"", "note = \"Drumtrax calls its kick Bass\"", "slug = \"drumtrax-from-mars\""} {
		if !strings.Contains(body, want) {
			t.Errorf("pack file lacks %q:\n%s", want, body)
		}
	}
	// the meta cache was patched for the covered files only
	after := harvest.LoadMeta(ws, "src")
	if after["Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 01.wav"].Instrument != "kick" {
		t.Errorf("meta not patched: %+v", after["Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 01.wav"])
	}
	if after["Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 01.wav"].Why.Instrument.Tier != annotations.TierPackInstrument {
		t.Errorf("why should name the pack block: %+v", after["Samples From Mars/Drumtrax From Mars/WAV/Hits/Bass Drumtrax 01.wav"].Why)
	}
	// and the loaded layers now agree with the cache
	vendors, _ = annotations.Load(ws.AnnotationRoots()...)
	if p := annotations.BySlug(vendors)["samples-from-mars"].PackByDir("Drumtrax From Mars"); p == nil || len(p.Instruments) != 1 || p.Instruments[0].Scope != "pack" {
		t.Errorf("local block not loaded: %+v", p)
	}

	// a second correction on the same pack merges into the same file; a
	// same-path [[dir]] entry is updated, not duplicated
	for _, c := range []Correction{
		{Location: "src", Path: "Samples From Mars/Drumtrax From Mars/WAV/Fills", Facet: "category", Value: "loops", Mode: "default"},
		{Location: "src", Path: "Samples From Mars/Drumtrax From Mars/WAV/Fills", Facet: "category", Value: "loops", Local: true},
	} {
		if _, err := Apply(ws, lc, cat, harvest.LoadMeta(ws, "src"), vendors, c, Provenance{}); err != nil {
			t.Fatal(err)
		}
		vendors, _ = annotations.Load(ws.AnnotationRoots()...)
	}
	data, _ = os.ReadFile(filepath.Join(ws.LocalAnnotations(), "vendors", "samples-from-mars", "packs", "drumtrax-from-mars.toml"))
	body = string(data)
	if strings.Count(body, "[[dir]]") != 1 || strings.Contains(body, "default_category") || !strings.Contains(body, "category = \"loops\"") || !strings.Contains(body, "local = true") {
		t.Errorf("same-path entry must merge, pin replacing default:\n%s", body)
	}

	// an unknown vendor gets a minimal identity file
	if _, err := Apply(ws, lc, cat, harvest.LoadMeta(ws, "src"), vendors, Correction{Location: "src", Path: "My Drums/Field Kit", Facet: "instrument", Value: "hat"}, Provenance{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.LocalAnnotations(), "vendors", "my-drums", "vendor.toml")); err != nil {
		t.Error("new vendor needs a vendor.toml in the local layer")
	}
	if m := harvest.LoadMeta(ws, "src")["My Drums/Field Kit/Snap 1.wav"]; m.Instrument != "hat" {
		t.Errorf("new vendor's pin did not take: %+v", m)
	}

	// the log carries the evidence
	log, _ := os.ReadFile(filepath.Join(ws.LocalAnnotations(), "corrections.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 4 {
		t.Fatalf("log lines = %d:\n%s", len(lines), log)
	}
	var first map[string]any
	json.Unmarshal([]byte(lines[0]), &first)
	if first["app_version"] != "test" || first["covered"].(float64) != 5 || first["target"].(map[string]any)["pack"] != "drumtrax-from-mars" {
		t.Errorf("log entry: %v", first)
	}

	// listing and export: local = true stays home, acks never leave
	Ack(ws, "src", "Samples From Mars/Drumtrax From Mars/WAV/Hits", "numbered takes")
	if !Acks(ws)["src\x00Samples From Mars/Drumtrax From Mars/WAV/Hits"] {
		t.Error("ack not read back")
	}
	entries, err := List(ws)
	if err != nil || len(entries) != 3 {
		t.Fatalf("list: %v %+v", err, entries)
	}
	zb, err := Export(ws)
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	names := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		names[f.Name] = b.String()
	}
	if _, ok := names["annotations.local/acks.jsonl"]; ok {
		t.Error("acks must not export")
	}
	dt := names["annotations.local/vendors/samples-from-mars/packs/drumtrax-from-mars.toml"]
	if strings.Contains(dt, "[[dir]]") || !strings.Contains(dt, "[[instrument]]") {
		t.Errorf("export must drop local = true entries and keep the rest:\n%s", dt)
	}
	if _, ok := names["annotations.local/vendors/my-drums/vendor.toml"]; !ok {
		t.Error("new vendor identity must export")
	}
	if _, ok := names["annotations.local/corrections.jsonl"]; !ok {
		t.Error("the log rides with the diff")
	}
}
