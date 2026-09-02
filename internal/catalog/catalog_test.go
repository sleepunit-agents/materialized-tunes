package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
)

// A Live set on an archive drive can reference thousands of samples; its
// catalog entry is one JSONL line and it can run past a megabyte. The
// reader must not have a line cap — one such document must not take the
// whole location's catalog down with it (Jonathan, archive-drive rescan
// → blank app, 2026-09-02).
func TestLoadHugeDocumentLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.jsonl")
	doc := &ableton.Doc{}
	for i := 0; i < 6000; i++ {
		doc.Refs = append(doc.Refs, ableton.Ref{
			Rel:  strings.Repeat("Samples/Processed/Consolidated/", 3) + "Very Long Sample Name That Live Wrote " + strings.Repeat("x", 40) + ".wav",
			Abs:  "C:/Users/someone/Documents/Ableton/Projects/Some Project/Samples/Processed/Consolidated/Very Long Sample Name.wav",
			Name: "Very Long Sample Name.wav", Type: "3",
		})
	}
	in := map[string]Entry{
		"Sets/big.als":     {Path: "Sets/big.als", Size: 1, Doc: doc},
		"Kicks/kick01.wav": {Path: "Kicks/kick01.wav", Size: 2},
	}
	if err := Write(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 2 || out["Sets/big.als"].Doc == nil || len(out["Sets/big.als"].Doc.Refs) != 6000 {
		t.Fatalf("got %d entries, doc refs %v", len(out), out["Sets/big.als"].Doc)
	}
}

// A malformed line still names itself.
func TestLoadBadLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "catalog.jsonl")
	if err := writeRaw(p, "{\"path\":\"a.wav\",\"size\":1}\n{not json}\n"); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("err = %v", err)
	}
}

func writeRaw(p, s string) error { return os.WriteFile(p, []byte(s), 0o644) }
