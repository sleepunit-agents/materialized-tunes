package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/progress"
)

// The decode reports bytes under the location's name while it runs and
// leaves nothing behind when it is done — the launch wait is watchable.
func TestReadReportsBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.jsonl")
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString(`{"path":"p/` + strings.Repeat("x", 80) + `","size":1,"mtime":1,"sha256":"a"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []progress.Task
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, ts := progress.Snapshot()
			for _, x := range ts {
				if x.Kind == "catalog" {
					seen = append(seen, x)
				}
			}
			select {
			case <-done:
				return
			default:
			}
			if len(seen) > 200 {
				return
			}
		}
	}()
	entries, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if len(entries) != 1 { // every line has the same path — one entry, 20k decodes
		t.Fatalf("entries: %d", len(entries))
	}
	if len(seen) == 0 {
		t.Fatal("no catalog task observed during the decode")
	}
	x := seen[0]
	if x.Label != "reading catalog archive" || x.Unit != "bytes" || x.Total == 0 {
		t.Fatalf("task: %+v", x)
	}
	if _, ts := progress.Snapshot(); len(ts) != 0 {
		t.Fatalf("task left behind: %+v", ts)
	}
}
