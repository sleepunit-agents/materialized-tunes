package lock

import (
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
)

// Migration is a diff's "would MOVE" set turned into an executable plan:
// every locked file whose bytes are already exactly right and only the
// output path changed (a layout or `as` edit). Renaming those into place
// is what `mtunes migrate` does instead of a full re-render. Everything
// else — new selections, content drift, transform changes — stays a
// materialize job and is only counted here.
type Migration struct {
	Moves      []Move          // plain files: rename Old → New on the target
	Companions []CompanionMove // Ableton documents to re-rewrite (their own path moved, or a sample they point at did)
	Pending    int             // entries only a materialize can update: added, content drift, transform changes
	Stay       int             // locked files already at the path the recipe wants
	Orphans    int             // locked files the recipe no longer selects — left where they are
}

type Move struct {
	Index int // into Lock.Entries
	Old   string
	New   string
	Bytes int64 // locked output size — the cheap "is this still our file" check before renaming
}

// CompanionMove is a companion that must be re-rendered rather than
// renamed: the absolute and relative sample paths written inside it
// follow the layout, so a moved document — or a document whose samples
// moved out from under it — needs its refs rewritten from source.
type CompanionMove struct {
	Index int // into Lock.Entries
	Old   string
	New   string // == Old when only the refs moved
}

// Work reports whether the migration has anything to execute.
func (m *Migration) Work() int { return len(m.Moves) + len(m.Companions) }

// PlanMigration classifies every lock entry against the current plan the
// way Compute does, but keeps the indices and byte sizes a rename pass
// needs. The classification must stay in lockstep with Compute: a file
// migrate renames is exactly one diff would call Moved, and a companion
// it rewrites is one diff calls Moved or flags for its refs.
func PlanMigration(l *Lock, p *plan.Plan) *Migration {
	m := &Migration{}
	type cur struct {
		e    plan.Entry
		args []string
	}
	inPlan := map[string]cur{}
	outPaths := map[string]bool{}
	for _, e := range p.Entries {
		inPlan[e.Location+"\x00"+e.SourcePath] = cur{e: e, args: planArgs(p, e)}
		outPaths[e.OutPath] = true
	}
	inLock := map[string]bool{}
	for i, e := range l.Entries {
		key := e.Source.Location + "\x00" + e.Source.Path
		inLock[key] = true
		c, selected := inPlan[key]
		if !selected {
			m.Orphans++
			continue
		}
		switch {
		case c.e.SHA256 != e.Source.SHA256,
			c.e.Companion != e.Transform.Companion,
			c.e.Copy != e.Transform.Copy,
			!equalArgs(c.args, e.Transform.FFmpegArgs):
			m.Pending++
		case c.e.Companion && (c.e.OutPath != e.Output.Path || refsMoved(e.Transform.Refs, outPaths)):
			m.Companions = append(m.Companions, CompanionMove{Index: i, Old: e.Output.Path, New: c.e.OutPath})
		case c.e.OutPath != e.Output.Path:
			m.Moves = append(m.Moves, Move{Index: i, Old: e.Output.Path, New: c.e.OutPath, Bytes: e.Output.Bytes})
		default:
			m.Stay++
		}
	}
	for _, e := range p.Entries {
		if !inLock[e.Location+"\x00"+e.SourcePath] {
			m.Pending++
		}
	}
	return m
}
