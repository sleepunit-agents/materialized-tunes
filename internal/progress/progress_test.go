package progress

import "testing"

func TestTasksComeAndGo(t *testing.T) {
	s0, before := Snapshot()
	a := Start("catalog", "reading catalog archive").Units("bytes").Set("", 0, 100)
	b := Start("plan", "planning card").Set("loading catalogs", 1, 7)
	seq, ts := Snapshot()
	if seq <= s0 || len(ts) != len(before)+2 {
		t.Fatalf("expected two more tasks and a moved seq: %d→%d, %d→%d", s0, seq, len(before), len(ts))
	}
	if ts[len(ts)-2].Label != "reading catalog archive" || ts[len(ts)-1].Label != "planning card" {
		t.Fatalf("snapshot not oldest-first: %+v", ts)
	}
	a.Set("", 50, 100)
	_, ts = Snapshot()
	if got := ts[len(ts)-2]; got.Done != 50 || got.Unit != "bytes" || got.Kind != "catalog" {
		t.Fatalf("Set/Units not reflected: %+v", got)
	}
	if got := ts[len(ts)-1]; got.Stage != "loading catalogs" || got.Total != 7 {
		t.Fatalf("stage not reflected: %+v", got)
	}
	a.End()
	b.End()
	b.End() // twice is fine
	_, ts = Snapshot()
	if len(ts) != len(before) {
		t.Fatalf("tasks not removed: %+v", ts)
	}
	var nilTask *Running
	nilTask.Set("x", 1, 2).Units("y").Relabel("z").End() // nil-safe chain
}
