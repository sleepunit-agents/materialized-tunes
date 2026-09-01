package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/location"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

const rackXML = `<?xml version="1.0" encoding="UTF-8"?>
<Ableton MajorVersion="5" MinorVersion="12.0_12049" Creator="Ableton Live 12.1">
	<GroupDevicePreset>
		<FileRef>
			<RelativePathType Value="3" />
			<RelativePath Value="../WAV/SuperPulse/SuperPulse C0.wav" />
			<Path Value="C:/SFM/MS10 From Mars/WAV/SuperPulse/SuperPulse C0.wav" />
		</FileRef>
	</GroupDevicePreset>
</Ableton>
`

// TestScanRecordsDocRefs: a Live document's sample refs are read at scan
// time into the catalog; an unreadable one carries the error instead; a
// catalog written before the field is backfilled by the next scan even
// though the file is unchanged.
func TestScanRecordsDocRefs(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, b []byte) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("MS10/Presets/SuperPulse.adg", ableton.Encode([]byte(rackXML)))
	write("MS10/Presets/Broken.adg", []byte("not gzip"))
	write("MS10/notes.txt", []byte("hi"))
	loc, err := location.New(workspace.LocationConfig{Name: "src", Type: "local", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	catPath := filepath.Join(t.TempDir(), "src.jsonl")
	res, err := Run(context.Background(), loc, catPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Docs != 1 {
		t.Errorf("docs = %d, want 1", res.Docs)
	}
	cat, err := catalog.Load(catPath)
	if err != nil {
		t.Fatal(err)
	}
	e := cat["MS10/Presets/SuperPulse.adg"]
	if e.Doc == nil || len(e.Doc.Refs) != 1 || e.Doc.Refs[0].Rel != "../WAV/SuperPulse/SuperPulse C0.wav" || e.Doc.Refs[0].Name != "SuperPulse C0.wav" {
		t.Errorf("doc = %+v", e.Doc)
	}
	if b := cat["MS10/Presets/Broken.adg"]; b.Doc != nil || b.DocErr == "" {
		t.Errorf("broken: doc=%+v err=%q", b.Doc, b.DocErr)
	}
	if n := cat["MS10/notes.txt"]; n.Doc != nil || n.DocErr != "" {
		t.Errorf("txt got a doc: %+v %q", n.Doc, n.DocErr)
	}

	// strip the field as an older catalog would have it; a rescan reads it back
	e.Doc = nil
	cat["MS10/Presets/SuperPulse.adg"] = e
	if err := catalog.Write(catPath, cat); err != nil {
		t.Fatal(err)
	}
	res, err = Run(context.Background(), loc, catPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Docs != 1 {
		t.Errorf("backfill docs = %d, want 1", res.Docs)
	}
	cat, _ = catalog.Load(catPath)
	if e := cat["MS10/Presets/SuperPulse.adg"]; e.Doc == nil || len(e.Doc.Refs) != 1 {
		t.Errorf("backfilled doc = %+v", e.Doc)
	}
	if b := cat["MS10/Presets/Broken.adg"]; b.DocErr == "" {
		t.Errorf("broken lost its error on rescan: %+v", b)
	}
}
