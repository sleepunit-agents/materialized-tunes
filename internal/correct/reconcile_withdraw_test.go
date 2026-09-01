package correct

import (
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
)

// Withdraw is the undo: the entry goes, and the files it covered read the
// way they did before the correction.
func TestWithdraw(t *testing.T) {
	ws, lc, cat := fixture(t)
	vendors, _ := annotations.Load(ws.AnnotationRoots()...)
	before := harvest.LoadMeta(ws, "src")
	src := Sources{
		Catalog: func(string) (map[string]catalog.Entry, error) { return cat, nil },
		Meta:    func(string) map[string]harvest.Meta { return harvest.LoadMeta(ws, "src") },
	}
	c := Correction{Location: "src", Path: "Samples From Mars/Drumtrax From Mars/WAV/Fills", Facet: "category", Value: "loops"}
	rad, err := Apply(ws, lc, cat, before, vendors, c, Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	if rad.Changed == 0 {
		t.Fatalf("the fixture correction must move something: %+v", rad)
	}
	entries, _ := List(ws)
	if len(entries) != 1 {
		t.Fatalf("one local entry expected: %+v", entries)
	}
	v, err := Withdraw(ws, src, entries[0], "withdrawn")
	if err != nil {
		t.Fatal(err)
	}
	if v.Redundant || v.Changed != rad.Changed || v.Covered != rad.Covered {
		t.Errorf("withdraw must judge the entry as still needed with the correction's radius: %+v vs %+v", v, rad)
	}
	if rest, _ := List(ws); len(rest) != 0 {
		t.Errorf("entry must be gone: %+v", rest)
	}
	after := harvest.LoadMeta(ws, "src")
	for p, b := range before {
		if a := after[p]; a.Category != b.Category || a.Instrument != b.Instrument {
			t.Errorf("%s: %s/%s before, %s/%s after withdraw", p, b.Category, b.Instrument, a.Category, a.Instrument)
		}
	}
}
