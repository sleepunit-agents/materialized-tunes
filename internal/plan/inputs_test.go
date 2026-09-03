package plan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// A correction patches the loaded inputs in place: the covered records
// change, the stamp follows the files on disk, and the catalogs — the
// cost of a load — are the same maps as before.
func TestInputsPatched(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir}}
	ws.SaveConfig()
	in := NewInputs(ws)
	in.catalogs["src"] = nil // stands in for a loaded catalog: identity is what we check
	in.meta["src"] = map[string]harvest.Meta{"a/k.wav": {Path: "a/k.wav", Instrument: "kick"}, "a/x.wav": {Path: "a/x.wav", Instrument: "kick"}}
	in.loaded = true
	before := in.Stamp()

	// the files a correction writes: a local TOML and the meta cache
	os.MkdirAll(filepath.Join(ws.LocalAnnotations(), "vendors"), 0o755)
	os.WriteFile(filepath.Join(ws.LocalAnnotations(), "vendors", "a.toml"), []byte("x=1\n"), 0o644)
	in.Patched("src", map[string]harvest.Meta{"a/k.wav": {Path: "a/k.wav", Instrument: "kick", Category: "one-shots"}, "a/x.wav": {}})

	if _, kept := in.catalogs["src"]; !kept {
		t.Error("Patched must keep the catalogs")
	}
	if m := in.Meta("src"); m["a/k.wav"].Category != "one-shots" {
		t.Errorf("patched record not applied: %+v", m["a/k.wav"])
	} else if _, still := m["a/x.wav"]; still {
		t.Error("an empty record deletes, as harvest.Patch does")
	}
	if in.loaded {
		t.Error("the annotation layers must reload — the correction wrote one")
	}
	if in.Stamp() == before || !in.Fresh() {
		t.Errorf("the stamp must follow the files on disk: before %s after %s fresh %v", before, in.Stamp(), in.Fresh())
	}
}
