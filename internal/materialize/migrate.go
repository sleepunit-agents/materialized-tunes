package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/cache"
	"github.com/sleepunit-agents/materialized-tunes/internal/lock"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

type MigrateOutcome struct {
	LockPath  string
	Renamed   int
	Rewritten int // companions re-rendered with refreshed refs
	Pending   int // entries only a materialize can update — carried from the Migration
	Orphans   int
	Warnings  []string
	Skipped   []Skip // couldn't be moved (missing, size drift, rename error) — left for materialize
}

// Migrate executes a lock.Migration against target: outputs whose only
// change is the path are renamed into place instead of re-rendered,
// companions are re-rewritten so the sample paths inside them follow, and
// a new lock records the target as it now stands. No audio is transcoded.
// The only files it will delete are ones the lock owns byte-for-byte: a
// companion's old copy after its replacement is written, and directories
// the renames emptied.
func Migrate(ctx context.Context, ws *workspace.Workspace, l *lock.Lock, p *plan.Plan, m *lock.Migration, target string, progress func(int, int)) (*MigrateOutcome, error) {
	out := &MigrateOutcome{Pending: m.Pending, Orphans: m.Orphans}
	entries := append([]lock.Entry(nil), l.Entries...)
	total := m.Work()
	count := 0
	tick := func() {
		count++
		if progress != nil {
			progress(count, total)
		}
	}
	vacated := map[string]bool{} // old rel paths whose file left — their dirs may now be empty

	// Plain moves run in two phases: everything renames to a temp name
	// beside its destination first, then the temps drop into place. That
	// makes chains (a→b while b→c), swaps, and case-only renames on
	// case-insensitive filesystems safe — no move can land on a file that
	// hasn't moved out yet. Interrupted runs resume: a matching temp or an
	// already-correct destination counts as that phase done.
	type staged struct {
		mv   lock.Move
		tmp  string
		dest string
	}
	var toFinish []staged
	for _, mv := range m.Moves {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		oldAbs := filepath.Join(target, filepath.FromSlash(mv.Old))
		newAbs := filepath.Join(target, filepath.FromSlash(mv.New))
		tmp := newAbs + ".mtunes-mig"
		if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
			out.Skipped = append(out.Skipped, Skip{OutRel: mv.New, Err: err.Error()})
			tick()
			continue
		}
		if info, err := os.Stat(tmp); err == nil && info.Size() == mv.Bytes {
			toFinish = append(toFinish, staged{mv, tmp, newAbs})
			continue
		}
		info, err := os.Stat(oldAbs)
		if err != nil {
			if ni, err2 := os.Stat(newAbs); err2 == nil && ni.Size() == mv.Bytes {
				// already where the recipe wants it — a resumed run, or a
				// materialize that duplicated it there
				entries[mv.Index].Output.Path = mv.New
				out.Renamed++
				tick()
				continue
			}
			out.Skipped = append(out.Skipped, Skip{OutRel: mv.New,
				Err: fmt.Sprintf("%s is not on the target — run materialize to render it", mv.Old)})
			tick()
			continue
		}
		if info.Size() != mv.Bytes {
			out.Skipped = append(out.Skipped, Skip{OutRel: mv.New,
				Err: fmt.Sprintf("%s is %d bytes, the lock says %d — not our file any more, left in place", mv.Old, info.Size(), mv.Bytes)})
			tick()
			continue
		}
		if err := os.Rename(oldAbs, tmp); err != nil {
			out.Skipped = append(out.Skipped, Skip{OutRel: mv.New, Err: err.Error()})
			tick()
			continue
		}
		toFinish = append(toFinish, staged{mv, tmp, newAbs})
	}
	for _, s := range toFinish {
		if err := os.Rename(s.tmp, s.dest); err != nil {
			out.Skipped = append(out.Skipped, Skip{OutRel: s.mv.New, Err: err.Error()})
			tick()
			continue
		}
		entries[s.mv.Index].Output.Path = s.mv.New
		out.Renamed++
		vacated[s.mv.Old] = true
		tick()
	}

	// Companions re-render from source: the paths written inside them are
	// the layout, so a rename can't fix them. Same code path as
	// materialize — decode, resolve every ref against the plan, re-encode.
	if len(m.Companions) > 0 {
		cc := newCompanionCtx(p, absTarget(target))
		inPlan := map[string]plan.Entry{}
		for _, e := range p.Entries {
			inPlan[e.Location+"\x00"+e.SourcePath] = e
		}
		var jobs []job
		byOut := map[string]lock.CompanionMove{}
		for _, cm := range m.Companions {
			le := entries[cm.Index]
			pe := inPlan[le.Source.Location+"\x00"+le.Source.Path]
			jobs = append(jobs, job{
				loc: pe.Location, srcPath: pe.SourcePath, srcSHA: pe.SHA256, srcBytes: pe.SourceSize,
				outRel: cm.New, args: entryArgs(p, pe), companion: true, cc: cc,
			})
			byOut[cm.New] = cm
		}
		results, skips, err := runJobs(ctx, ws, jobs, target, nil)
		if err != nil {
			return nil, err
		}
		out.Skipped = append(out.Skipped, skips...)
		for _, d := range results {
			cm := byOut[d.outRel]
			oldSHA := entries[cm.Index].Output.SHA256
			entries[cm.Index].Output = lock.Output{Path: cm.New, SHA256: d.outSHA, Bytes: d.outBytes}
			entries[cm.Index].Transform.FFmpegArgs = d.args
			entries[cm.Index].Transform.Refs = d.refs
			out.Rewritten++
			if d.warning != "" {
				out.Warnings = append(out.Warnings, d.warning)
			}
			tick()
			if cm.Old == cm.New {
				continue
			}
			// the old copy is deleted only if it is still byte-for-byte the
			// file the lock wrote — anything else isn't ours to remove
			oldAbs := filepath.Join(target, filepath.FromSlash(cm.Old))
			if sum, err := cache.HashFile(oldAbs); err == nil {
				if sum == oldSHA {
					if os.Remove(oldAbs) == nil {
						vacated[cm.Old] = true
					}
				} else {
					out.Warnings = append(out.Warnings, fmt.Sprintf(
						"%s: old copy differs from the lock — left in place, delete it yourself if it's stale", cm.Old))
				}
			}
		}
		for range skips {
			tick()
		}
	}

	pruneEmptied(target, vacated)

	if out.Renamed+out.Rewritten == 0 {
		return out, nil
	}
	l2 := *l
	l2.Entries = entries
	l2.Created = time.Now().UTC()
	l2.Layout = p.View.Layout
	l2.Tooling = map[string]string{}
	for k, v := range l.Tooling {
		l2.Tooling[k] = v // ffmpeg stays the version that rendered the audio — none ran here
	}
	l2.Tooling["mtunes"] = Version
	if recipe, err := os.ReadFile(filepath.Join(ws.Root, "views", p.View.Name+".toml")); err == nil {
		sum := sha256.Sum256(recipe)
		l2.RecipeSHA256 = hex.EncodeToString(sum[:])
	}
	l2.Totals = lock.Totals{}
	for _, e := range entries {
		l2.Totals.Files++
		l2.Totals.Bytes += e.Output.Bytes
	}
	lockPath, err := lock.Write(ws.Root, &l2)
	if err != nil {
		return nil, err
	}
	out.LockPath = lockPath
	if l.Device.Delivery.Mode == "card" {
		if err := writeCardMeta(target, l.View, filepath.Base(lockPath)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// pruneEmptied removes the directories the moves emptied: every parent
// chain of a vacated path, deepest first, with os.Remove — which refuses
// non-empty dirs, so anything still holding files (the lock's or the
// user's) keeps its home.
func pruneEmptied(target string, vacated map[string]bool) {
	dirs := map[string]bool{}
	for rel := range vacated {
		for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	var order []string
	for d := range dirs {
		order = append(order, d)
	}
	sort.Slice(order, func(i, j int) bool {
		return strings.Count(order[i], "/") > strings.Count(order[j], "/")
	})
	for _, d := range order {
		os.Remove(filepath.Join(target, filepath.FromSlash(d)))
	}
}
