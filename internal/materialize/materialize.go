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
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
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

// entryArgs is the ffmpeg argument list for one plan entry — the thing the
// lock records verbatim. Channel handling comes from the entry (dual-mono
// sources fold by taking one channel), everything else from the device.
func entryArgs(p *plan.Plan, e plan.Entry) []string {
	if e.Copy {
		return nil
	}
	ch, downmix := e.FoldSpec(p.Device.Audio.Downmix)
	return transcode.BuildArgs(e.InChannels, ch, downmix, e.InRate, e.OutRate, e.OutDepth)
}

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
	Resumed  int // of Written: reused in place via the size-match resume path
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
	copy     bool // passthrough: copy the source bytes, no ffmpeg
	planned  int64

	companion bool              // Ableton document: sample refs rewritten
	refs      map[string]string // Restore: recorded ref → sample OutPath; nil in Materialize (resolved live)
	cc        *companionCtx
}

type done struct {
	job
	outSHA   string
	outBytes int64
	reused   bool              // resume path: existing output reused, not rendered
	refs     map[string]string // companion: ref key → sample OutPath, as written
	warning  string            // companion: unresolved refs
}

// Materialize renders every plan entry into target and writes the lock.
func Materialize(ctx context.Context, ws *workspace.Workspace, p *plan.Plan, target string, progress func(int, int)) (*Outcome, error) {
	jobs := make([]job, len(p.Entries))
	var cc *companionCtx
	if p.Companions > 0 {
		cc = newCompanionCtx(p, absTarget(target))
	}
	for i, e := range p.Entries {
		jobs[i] = job{
			loc: e.Location, srcPath: e.SourcePath, srcSHA: e.SHA256, srcBytes: e.SourceSize,
			outRel:  e.OutPath,
			args:    entryArgs(p, e),
			copy:    e.Copy,
			planned: e.OutBytes,
		}
		if e.Companion {
			jobs[i].companion, jobs[i].cc, jobs[i].planned = true, cc, 0 // size unknown until rewritten
		}
	}

	results, skips, err := runJobs(ctx, ws, jobs, target, progress)
	if err != nil {
		return nil, err
	}

	out := &Outcome{Skipped: skips}
	l := &lock.Lock{
		View:    p.View.Name,
		Layout:  p.View.Layout,
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
			Transform: lock.Transform{FFmpegArgs: d.args, Copy: d.copy, Companion: d.companion, Refs: d.refs},
			Output:    lock.Output{Path: d.outRel, SHA256: d.outSHA, Bytes: d.outBytes},
		})
		l.Totals.Files++
		l.Totals.Bytes += d.outBytes
		out.Written++
		out.Bytes += d.outBytes
		if d.reused {
			out.Resumed++
		}
		if d.warning != "" {
			out.Warnings = append(out.Warnings, d.warning)
		}
		if !d.companion && d.outBytes != d.planned {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"%s: actual %d bytes vs %d planned (fit math was %+d bytes off — resampler boundary or an unexpected header layout)",
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
	var cc *companionCtx
	for i, e := range l.Entries {
		jobs[i] = job{
			loc: e.Source.Location, srcPath: e.Source.Path, srcSHA: e.Source.SHA256,
			srcBytes: e.Source.Bytes, outRel: e.Output.Path, args: e.Transform.FFmpegArgs,
			copy: e.Transform.Copy, planned: e.Output.Bytes,
		}
		if e.Transform.Companion {
			if cc == nil {
				cc = &companionCtx{dev: l.Device.Companions, target: strings.TrimSuffix(strings.ReplaceAll(absTarget(target), `\`, "/"), "/")}
			}
			refs := e.Transform.Refs
			if refs == nil {
				refs = map[string]string{}
			}
			jobs[i].companion, jobs[i].cc, jobs[i].refs = true, cc, refs
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
		if d.reused {
			out.Resumed++
		}
		if want := wantSHA[d.outRel]; want != d.outSHA {
			if d.companion {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"%s: output differs from lock — the absolute sample paths inside follow the target, so restoring elsewhere changes bytes (refs are the same)", d.outRel))
			} else {
				out.Warnings = append(out.Warnings, fmt.Sprintf(
					"%s: output differs from lock (locked with ffmpeg %s, now %s) — audio is equivalent, bytes are not",
					d.outRel, l.Tooling["ffmpeg"], transcode.Version(ctx)))
			}
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

	// Default is deliberately modest: the usual target is an SD/CF card that
	// hates concurrent writers. MTUNES_WORKERS overrides it for fast targets
	// (a DAW library on an internal NVMe wants far more than 6).
	workers := runtime.NumCPU() / 2
	if workers < 2 {
		workers = 2
	}
	if workers > 6 {
		workers = 6
	}
	if s := os.Getenv("MTUNES_WORKERS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			workers = n
		}
	}

	var (
		mu       sync.Mutex
		results  []done
		skips    []Skip
		firstErr error
		count    int
		wg       sync.WaitGroup
	)
	ch := make(chan []job)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for unit := range ch {
				ds, fails := runUnit(cctx, locs, unit, cacheDir, target)
				mu.Lock()
				results = append(results, ds...)
				count += len(ds)
				if progress != nil && len(ds) > 0 {
					progress(count, len(jobs))
				}
				if cctx.Err() == nil {
					// aborting anyway otherwise; in-flight cancellation noise, not a skip
					for _, f := range fails {
						skips = append(skips, f)
						if len(skips) > maxSkips && firstErr == nil {
							firstErr = fmt.Errorf("%d entries failed (last: %s: %s) — this is systemic, not per-file; aborting", len(skips), f.OutRel, f.Err)
							cancel()
						}
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, unit := range batchJobs(jobs) {
		select {
		case ch <- unit:
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

// batchJobs groups work into units for the worker pool. Copies run one per
// unit (they're already just I/O); transcodes are chunked so one ffmpeg
// process renders many outputs — on Windows the spawn (conhost + Defender
// rescan of ffmpeg.exe) costs more than transcoding a drum hit, so per-file
// ffmpeg was the long pole. Chunks respect the command-line length cap.
// MTUNES_BATCH overrides the chunk size (1 disables batching).
func batchJobs(jobs []job) [][]job {
	maxItems := transcode.MaxBatchItems
	if s := os.Getenv("MTUNES_BATCH"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			maxItems = n
		}
	}
	var units [][]job
	var cur []job
	chars := 0
	flush := func() {
		if len(cur) > 0 {
			units = append(units, cur)
			cur, chars = nil, 0
		}
	}
	for _, j := range jobs {
		if j.copy || j.companion {
			units = append(units, []job{j})
			continue
		}
		// outRel stands in for the absolute output path; target is added
		// later but the headroom in MaxBatchChars covers it.
		c := transcode.ItemChars(transcode.Item{In: j.srcPath, Args: j.args, Out: j.outRel}) + 64
		if len(cur) >= maxItems || chars+c > transcode.MaxBatchChars {
			flush()
		}
		cur = append(cur, j)
		chars += c
	}
	flush()
	return units
}

// runUnit renders one unit: a single copy job, or a batch of transcodes.
// Entries whose output already exists at the planned size resume (hash,
// no render); the rest go through one ffmpeg. If that batch fails, each
// entry is retried on its own so the failure lands on the file that
// caused it rather than on the whole chunk.
func runUnit(ctx context.Context, locs map[string]location.Location, unit []job, cacheDir, target string) ([]done, []Skip) {
	if len(unit) == 1 && (unit[0].copy || unit[0].companion) {
		d, err := runOne(ctx, locs[unit[0].loc], unit[0], cacheDir, target)
		if err != nil {
			return nil, []Skip{{OutRel: unit[0].outRel, Err: err.Error()}}
		}
		return []done{d}, nil
	}

	type pending struct {
		job
		src, outPath string
	}
	var (
		results []done
		fails   []Skip
		todo    []pending
	)
	for _, j := range unit {
		outPath := filepath.Join(target, filepath.FromSlash(j.outRel))
		if d, ok := resume(j, outPath); ok {
			results = append(results, d)
			continue
		}
		src, err := cache.Ensure(ctx, locs[j.loc], j.srcPath, j.srcSHA, cacheDir)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(outPath), 0o755)
		}
		if err != nil {
			fails = append(fails, Skip{OutRel: j.outRel, Err: err.Error()})
			continue
		}
		todo = append(todo, pending{job: j, src: src, outPath: outPath})
	}
	if len(todo) == 0 {
		return results, fails
	}

	items := make([]transcode.Item, len(todo))
	for i, p := range todo {
		items[i] = transcode.Item{In: p.src, Args: p.args, Out: p.outPath}
	}
	ok := make([]bool, len(todo))
	if err := transcode.RunBatch(ctx, items); err == nil {
		for i := range ok {
			ok[i] = true
		}
	} else if ctx.Err() != nil {
		return results, fails
	} else {
		for i, it := range items {
			if err := transcode.Run(ctx, it.In, it.Args, it.Out); err != nil {
				fails = append(fails, Skip{OutRel: todo[i].outRel, Err: err.Error()})
				continue
			}
			ok[i] = true
		}
	}
	for i, p := range todo {
		if !ok[i] {
			continue
		}
		d, err := finish(p.job, p.outPath)
		if err != nil {
			fails = append(fails, Skip{OutRel: p.outRel, Err: err.Error()})
			continue
		}
		results = append(results, d)
	}
	return results, fails
}

// resume reports an existing output that can be reused in place. A
// deterministic render hitting its exact planned byte size is almost
// surely a previous run's output — hash it into the lock instead of
// re-rendering. An interrupted write won't match (truncated), and entries
// whose first run warned actual≠planned re-render, which is only wasteful,
// never wrong. In Restore, planned is the locked byte count, so resume
// applies there too and the lock-hash check still runs.
func resume(j job, outPath string) (done, bool) {
	info, err := os.Stat(outPath)
	if err != nil || j.planned <= 0 || info.Size() != j.planned {
		return done{}, false
	}
	sum, err := cache.HashFile(outPath)
	if err != nil {
		return done{}, false
	}
	return done{job: j, outSHA: sum, outBytes: info.Size(), reused: true}, true
}

// finish hashes and sizes a rendered output into its done record.
func finish(j job, outPath string) (done, error) {
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

func runOne(ctx context.Context, loc location.Location, j job, cacheDir, target string) (done, error) {
	outPath := filepath.Join(target, filepath.FromSlash(j.outRel))
	if d, ok := resume(j, outPath); ok {
		return d, nil
	}

	src, err := cache.Ensure(ctx, loc, j.srcPath, j.srcSHA, cacheDir)
	if err != nil {
		return done{}, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return done{}, err
	}
	if j.companion {
		refs, st, err := rewriteCompanion(j.cc, j, src, outPath)
		if err != nil {
			return done{}, err
		}
		d, err := finish(j, outPath)
		d.refs, d.warning = refs, unresolvedWarning(j.outRel, st)
		return d, err
	} else if j.copy {
		if err := copyFile(src, outPath); err != nil {
			return done{}, err
		}
	} else if err := transcode.Run(ctx, src, j.args, outPath); err != nil {
		return done{}, err
	}
	return finish(j, outPath)
}

// absTarget makes the target absolute so the paths written into
// companions are usable by Live on this machine.
func absTarget(target string) string {
	if abs, err := filepath.Abs(target); err == nil {
		return abs
	}
	return target
}

// copyFile writes src's bytes to dst through a temp file so an interrupted
// copy never leaves a full-size (resume-matching) output behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".mtunes-part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
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
