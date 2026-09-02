package ui

import (
	"os"
	"path/filepath"
	"testing"
)

// The Setup row says how many Live documents the catalog carries — the one
// number that tells the user whether racks are in play (document tier,
// companions) without opening a Plan why-line.
func TestLoadCatalogCountSeparatesDocuments(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sfm.jsonl")
	body := `{"path":"WAV/SuperPulse/12 SuperPulse MS10 C0.wav","size":1,"mtime":0,"sha256":"a","scanned_at":"2026-09-01T00:00:00Z"}
{"path":"Presets/Leads/SuperPulse.adg","size":1,"mtime":0,"sha256":"b","doc":{"refs":[]},"scanned_at":"2026-09-01T00:00:00Z"}
{"path":"Presets/Leads/Broken.adg","size":1,"mtime":0,"sha256":"c","doc_err":"not gzip","scanned_at":"2026-09-01T00:00:00Z"}
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	files, docs, err := loadCatalogCount(p)
	if err != nil {
		t.Fatal(err)
	}
	if files != 3 || docs != 1 {
		t.Fatalf("files=%d docs=%d, want 3/1 (a doc_err entry is not a read document)", files, docs)
	}
}
