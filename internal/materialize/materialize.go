// Package materialize renders plans into real files and lockfiles, and
// restores old lockfiles byte-for-byte(-best-effort). This is the only
// package that touches the target directory.
package materialize

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/jbarket/materialized-tunes/internal/cache"
	"github.com/jbarket/materialized-tunes/internal/location"
	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/transcode"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

const Version = "0.1.0-dev"

const CardMetaFile = ".mtunes-card.json"

type CardMeta struct {
	CardUUID string `json:"card_uuid"`
	View     string `json:"view"`
	Lock     string `json:"lock"` // lock file basename
	Mtunes   string `json:"mtunes"`
}

type Outcome struct {
	LockPath string
	Written  int
	Bytes    int64
	Warnings []string
	Skipped  []Skip // failed after retries; NOT in the lock — diff reports the gap
}

// Skip is one entry that failed rendering after retries. The run continues
// without it (capped — see maxSkips): one bad file must not kill a 47k-file
// materialization, but the lock only ever pins bytes that were written.
type Skip struct {
	OutRel string
	Err    string
}

// maxSkips is where per-file resilience flips to systemic failure — past
// this, the card or the link is gone and continuing is denial.
const maxSkips = 50

type job struct {
	loc      string
	srcPath  string
	srcSHA   string
	srcBytes int64
	outRel   string
	args     []string
	planned  int64
}

type done struct {
	job
	outSHA   string
	outBytes int64
}

// Materialize renders every plan entry into target and writes the lock.
func Materialize(ctx context.Context, ws *workspace.Workspace, p *plan.Plan, target string, progress func(int, int)) (*Outcome, error) {
	jobs := make([]job, len(p.Entries))
	for i, e := range p.Entries {
		jobs[i] = job{
			loc: e.Location, srcPath: e.SourcePath, srcSHA: e.SHA256, srcBytes: e.SourceSize,
			outRel: e.OutPath,
			args: transcode.BuildArgs(e.InChannels, p.Device.Audio.Channels,
				p.Device.Audio.Downmix, e.InRate, e.OutRate, e.OutDepth),
			planned: e.OutBytes,
		}
	}

	results, skips, err := runJobs(ctx, ws, jobs, target, progress)
	if err != nil {
		return nil, err
	}

	out := &Outcome{Skipped: skips}
	l := &lock.Lock{
		View:    p.View.Name,
		Created: time.Now().UTC(),
		Device:  *p.Device,
		Storage: *p.Storage,
		Tooling: map[string]string{"mtunes": Version, "ffmpeg": transcode.Version(ctx)},
	}
	if recipe, err := os.ReadFile(filepath.Join(ws.Root, "views", p.View.Name+".toml")); err == nil {
		sum := sha256.Sum256(recipe)
		l.RecipeSHA256 = hex.EncodeToString(sum[:])
	}
	for _, d := range results {
		l.Entries = append(l.Entries, lock.Entry{
			Source:    lock.Source{Location: d.loc, Path: d.srcPath, SHA256: d.srcSHA, Bytes: d.srcBytes},
			Transform: lock.Transform{FFmpegArgs: d.args},
			Output:    lock.Output{Path: d.outRel, SHA256: d.outSHA, Bytes: d.outBytes},
		})
		l.Totals.Files++
		l.Totals.Bytes += d.outBytes
		out.Written++
		out.Bytes += d.outBytes
		if d.outBytes != d.planned {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s: actual %d bytes vs %d planned (resampler boundary — fit math was %+d bytes off)",
				d.outRel, d.outBytes, d.planned, d.outBytes-d.planned))
		}
	}

	lockPath, err := lock.Write(ws.Root, l)
	if err != nil {
		return nil, err
	}
	out.LockPath = lockPath

	if p.Device.Delivery.Mode == "card" {
		if err := writeCardMeta(target, l.View, filepath.Base(lockPath)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Restore replays a lockfile into target using the recorded transforms.
// Output hashes are compared against the lock; drift (a different ffmpeg
// producing different bytes from identical args) is a warning, not a
// failure — the recorded tooling versions make it diagnosable.
func Restore(ctx context.Context, ws *workspace.Workspace, l *lock.Lock, lockPath, target string, progress func(int, int)) (*Outcome, error) {
	jobs := make([]job, len(l.Entries))
	for i, e := range l.Entries {
		jobs[i] = job{
			loc: e.Source.Location, srcPath: e.Source.Path, srcSHA: e.Source.SHA256,
			srcBytes: e.Source.Bytes, outRel: e.Output.Path, args: e.Transform.FFmpegArgs,
			planned: e.Output.Bytes,
		}
	}
	results, skips, err := runJobs(ctx, ws, jobs, target, progress)
	if err != nil {
		return nil, err
	}

	out := &Outcome{LockPath: lockPath, Skipped: skips}
	wantSHA := map[string]string{}
	for _, e := range l.Entries {
		wantSHA[e.Output.Path] = e.Output.SHA256
	}
	for _, d := range results {
		out.Written++
		out.Bytes += d.outBytes
		if want := wantSHA[d.outRel]; want != d.outSHA {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s: output differs from lock (locked with ffmpeg %s, now %s) — audio is equivalent, bytes are not",
				d.outRel, l.Tooling["ffmpeg"], transcode.Version(ctx)))
		}
	}
	if l.Device.Delivery.Mode == "card" {
		if err := writeCardMeta(target, l.View, filepath.Base(lockPath)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func runJobs(ctx context.Context, ws *workspace.Workspace, jobs []job, target string, progress func(int, int)) ([]done, []Skip, error) {
	locs := map[string]location.Location{}
	for _, j := range jobs {
		if _, ok := locs[j.loc]; ok {
			continue
		}
		lc, ok := ws.Location(j.loc)
		if !ok {
			return nil, nil, fmt.Errorf("location %q is not configured in this workspace", j.loc)
		}
		l, err := location.New(lc)
		if err != nil {
			return nil, nil, err
		}
		locs[j.loc] = l
	}
	cacheDir := filepath.Join(ws.Root, "cache", "objects")

	workers := runtime.NumCPU() / 2
	if workers < 2 {
		workers = 2
	}
	if workers > 6 {
		workers = 6
	}

	var (
		mu       sync.Mutex
		results  []done
		skips    []Skip
		firstErr error
		count    int
		wg       sync.WaitGroup
	)
	ch := make(chan job)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				d, err := runOne(cctx, locs[j.loc], j, cacheDir, target)
				mu.Lock()
				switch {
				case err != nil && cctx.Err() != nil:
					// aborting anyway; in-flight cancellation noise, not a skip
				case err != nil:
					skips = append(skips, Skip{OutRel: j.outRel, Err: err.Error()})
					if len(skips) > maxSkips && firstErr == nil {
						firstErr = fmt.Errorf("%d entries failed (last: %s: %v) — this is systemic, not per-file; aborting", len(skips), j.outRel, err)
						cancel()
					}
				default:
					results = append(results, d)
					count++
					if progress != nil {
						progress(count, len(jobs))
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-cctx.Done():
		}
	}
	close(ch)
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err // interrupted (^C): partial results must not write a lock
	}
	sort.Slice(skips, func(i, j int) bool { return skips[i].OutRel < skips[j].OutRel })
	return results, skips, nil
}

func runOne(ctx context.Context, loc location.Location, j job, cacheDir, target string) (done, error) {
	outPath := filepath.Join(target, filepath.FromSlash(j.outRel))

	// Resume: a deterministic transcode hitting its exact planned byte size
	// is almost surely a previous run's output — hash it into the lock
	// instead of re-rendering. An interrupted write won't match (truncated),
	// and entries whose first run warned actual≠planned re-render, which is
	// only wasteful, never wrong. In Restore, planned is the locked byte
	// count, so resume applies there too and the lock-hash check still runs.
	if info, err := os.Stat(outPath); err == nil && j.planned > 0 && info.Size() == j.planned {
		if sum, err := cache.HashFile(outPath); err == nil {
			return done{job: j, outSHA: sum, outBytes: info.Size()}, nil
		}
	}

	src, err := cache.Ensure(ctx, loc, j.srcPath, j.srcSHA, cacheDir)
	if err != nil {
		return done{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return done{}, err
	}
	if err := transcode.Run(ctx, src, j.args, outPath); err != nil {
		return done{}, err
	}
	sum, err := cache.HashFile(outPath)
	if err != nil {
		return done{}, err
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return done{}, err
	}
	return done{job: j, outSHA: sum, outBytes: info.Size()}, nil
}

// writeCardMeta stamps the card root, preserving an existing card UUID so
// a card keeps its identity across re-materializations.
func writeCardMeta(target, view, lockName string) error {
	path := filepath.Join(target, CardMetaFile)
	meta := CardMeta{View: view, Lock: lockName, Mtunes: Version}
	if data, err := os.ReadFile(path); err == nil {
		var prev CardMeta
		if json.Unmarshal(data, &prev) == nil && prev.CardUUID != "" {
			meta.CardUUID = prev.CardUUID
		}
	}
	if meta.CardUUID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		meta.CardUUID = hex.EncodeToString(b[:])
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
