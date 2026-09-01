package plan

import (
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
)

// TestPickCutsPackDirMapShape: the annotation shape the synthetic
// fixtures above simplify away — a pack's own [[dir]] map naming every
// tree, vendor globs with extra parallel entries, and a layout template
// placing the result. Pinned from Fractured (2026-09-01), whose three
// drifted tom renders must resolve exactly like Thump's kicks do.
func TestPickCutsPackDirMapShape(t *testing.T) {
	ann := map[string]string{
		"annotations/vendors/polyend/vendor.toml": "[vendor]\nname=\"Polyend\"\nslug=\"polyend\"\n" +
			"[formats]\ncanonical_dir=\"* 24 bit stereo\"\nparallel_dirs=[\"* 16 bit mono\", \"* 16 bit stereo\", \"*Step Kits*\", \"Tracker PTI kits\"]\n",
		"annotations/vendors/polyend/packs/fractured.toml": "[pack]\nname=\"Fractured\"\nslug=\"fractured\"\ndir=\"Fractured\"\n" +
			"[[dir]]\npath=\"Fractured 16 bit mono\"\nrole=\"format-tree\"\n" +
			"[[dir]]\npath=\"Fractured 16 bit stereo\"\nrole=\"format-tree\"\n" +
			"[[dir]]\npath=\"Fractured 24 bit stereo\"\nrole=\"canonical-audio\"\ncategory=\"one-shots\"\n" +
			"[[dir]]\npath=\"Step Kits\"\nrole=\"format-tree\"\ncategory=\"one-shots\"\n",
	}
	entries := []catalog.Entry{
		wavEntry("Polyend/Fractured/Fractured 24 bit stereo/Tom/Perc_Tom_Bloat.wav", 2, 44100, 24, 44100),
		wavEntry("Polyend/Fractured/Fractured 16 bit stereo/Tom/Perc_Tom_Bloat.wav", 2, 44100, 16, 44000), // drifted trim
		wavEntry("Polyend/Fractured/Fractured 16 bit mono/Tom/Perc_Tom_Bloat.wav", 1, 44100, 16, 44010),
	}
	var meta []string
	for _, e := range entries {
		meta = append(meta, `{"sha":"`+e.SHA256+`"}`)
	}
	ann["annotations-cache/meta/src.jsonl"] = strings.Join(meta, "\n") + "\n"
	ws := testWorkspace(t, entries, ann)
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
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if len(p.Entries) != 1 || p.CutsDropped != 2 {
		t.Fatalf("%d entries, %d dropped — want 1 and 2", len(p.Entries), p.CutsDropped)
	}
	if e := p.Entries[0]; !strings.Contains(e.SourcePath, "24 bit stereo") {
		t.Errorf("kept %q, want the longest render", e.SourcePath)
	}
}
