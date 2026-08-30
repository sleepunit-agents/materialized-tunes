package lock

import (
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
)

// PlanMigration must classify exactly the way Compute does: renames are
// diff's Moved, companion rewrites are Moved-or-refs-flagged, everything
// else is a materialize job it only counts.
func TestPlanMigration(t *testing.T) {
	p := &plan.Plan{Device: &profile.Device{}, Entries: []plan.Entry{
		{Location: "src", SourcePath: "a.wav", SHA256: "aa", Copy: true, OutPath: "New/a.wav"},          // moved
		{Location: "src", SourcePath: "b.wav", SHA256: "bb", Copy: true, OutPath: "Old/b.wav"},          // stays
		{Location: "src", SourcePath: "c.wav", SHA256: "cc2", Copy: true, OutPath: "New/c.wav"},         // sha drift → pending
		{Location: "src", SourcePath: "new.wav", SHA256: "nn", Copy: true, OutPath: "New/n.wav"},        // added → pending
		{Location: "src", SourcePath: "kit.adg", SHA256: "kk", Companion: true, OutPath: "Old/kit.adg"}, // refs moved → rewrite
	}}
	l := &Lock{Entries: []Entry{
		{Source: Source{Location: "src", Path: "a.wav", SHA256: "aa"}, Transform: Transform{Copy: true}, Output: Output{Path: "Old/a.wav", Bytes: 7}},
		{Source: Source{Location: "src", Path: "b.wav", SHA256: "bb"}, Transform: Transform{Copy: true}, Output: Output{Path: "Old/b.wav"}},
		{Source: Source{Location: "src", Path: "c.wav", SHA256: "cc"}, Transform: Transform{Copy: true}, Output: Output{Path: "Old/c.wav"}},
		{Source: Source{Location: "src", Path: "gone.wav", SHA256: "gg"}, Transform: Transform{Copy: true}, Output: Output{Path: "Old/gone.wav"}}, // deselected → orphan
		// a real lock records the computed args for companions too (see
		// Materialize), so the fixture must as well or the args compare trips
		{Source: Source{Location: "src", Path: "kit.adg", SHA256: "kk"}, Transform: Transform{Companion: true, Refs: map[string]string{"ref": "Old/a.wav"}}, Output: Output{Path: "Old/kit.adg"}},
	}}
	l.Entries[4].Transform.FFmpegArgs = planArgs(p, p.Entries[4])
	m := PlanMigration(l, p)
	if len(m.Moves) != 1 || m.Moves[0].Old != "Old/a.wav" || m.Moves[0].New != "New/a.wav" || m.Moves[0].Bytes != 7 || m.Moves[0].Index != 0 {
		t.Errorf("moves = %+v", m.Moves)
	}
	if len(m.Companions) != 1 || m.Companions[0].Old != "Old/kit.adg" || m.Companions[0].New != "Old/kit.adg" || m.Companions[0].Index != 4 {
		t.Errorf("companions = %+v (a companion whose samples moved rewrites in place)", m.Companions)
	}
	if m.Pending != 2 || m.Stay != 1 || m.Orphans != 1 {
		t.Errorf("pending=%d stay=%d orphans=%d, want 2/1/1", m.Pending, m.Stay, m.Orphans)
	}
	if m.Work() != 2 {
		t.Errorf("Work() = %d", m.Work())
	}
}
