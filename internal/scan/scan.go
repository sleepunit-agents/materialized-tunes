// Package scan builds and refreshes catalogs from locations. Rescans are
// incremental: a file whose size and mtime are unchanged keeps its recorded
// hash and audio metadata, so only new or touched files cost anything.
package scan

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/location"
)

// headerPrefixBytes is how much of a file we fetch for audio header
// parsing. Vendor WAVs occasionally stack LIST/INFO chunks before data;
// 256 KiB clears every real-world case we care to chase.
const headerPrefixBytes = 256 * 1024

// batchHeaderBytes is the smaller per-file read used by the single-stream
// batch path — nearly every header fits; the few that don't retry
// individually at headerPrefixBytes.
const batchHeaderBytes = 64 * 1024

// batchPrefixReader is implemented by locations that can stream many file
// headers in one remote invocation (the SSH backend).
type batchPrefixReader interface {
	ReadPrefixes(ctx context.Context, paths []string, n int64, fn func(path string, data []byte)) error
}

// batchPrefixes fetches headers for every audio file in one pass when the
// location supports it. Returns nil when it doesn't (local disks don't
// need batching).
func batchPrefixes(ctx context.Context, loc location.Location, files []location.File, progress Progress) map[string][]byte {
	br, ok := loc.(batchPrefixReader)
	if !ok {
		return nil
	}
	var audioPaths []string
	for _, f := range files {
		if audio.IsAudioPath(f.Path) {
			audioPaths = append(audioPaths, f.Path)
		}
	}
	if len(audioPaths) == 0 {
		return nil
	}
	prefixes := make(map[string][]byte, len(audioPaths))
	err := br.ReadPrefixes(ctx, audioPaths, batchHeaderBytes, func(p string, data []byte) {
		prefixes[p] = data
		if progress != nil {
			progress("headers", len(prefixes), len(audioPaths))
		}
	})
	if err != nil {
		return nil // fall back to per-file reads
	}
	return prefixes
}

// parseWithFallback parses audio metadata from a batch-fetched prefix,
// retrying with a larger individual read when the batch slice was too
// short (or missing) for this file's header layout.
func parseWithFallback(ctx context.Context, loc location.Location, path string, prefix []byte) (*audio.Meta, error) {
	if len(prefix) > 0 {
		if meta, err := audio.Parse(bytes.NewReader(prefix), path); err == nil {
			return meta, nil
		}
	}
	full, err := loc.ReadPrefix(ctx, path, headerPrefixBytes)
	if err != nil {
		return nil, err
	}
	return audio.Parse(bytes.NewReader(full), path)
}

type Result struct {
	Total     int
	Added     int
	Changed   int
	Removed   int
	Unchanged int
	AudioErrs int
}

type Progress func(stage string, done, total int)

// Run scans loc and rewrites its catalog at catalogPath.
func Run(ctx context.Context, loc location.Location, catalogPath string, progress Progress) (*Result, error) {
	old, err := catalog.Load(catalogPath)
	if err != nil {
		return nil, err
	}
	files, err := loc.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", loc.Name(), err)
	}

	res := &Result{Total: len(files)}
	now := time.Now().UTC()
	entries := make(map[string]catalog.Entry, len(files))
	var toHash []location.File

	for _, f := range files {
		if prev, ok := old[f.Path]; ok && prev.Size == f.Size && prev.MTime == f.MTime {
			entries[f.Path] = prev
			res.Unchanged++
			continue
		}
		if _, existed := old[f.Path]; existed {
			res.Changed++
		} else {
			res.Added++
		}
		toHash = append(toHash, f)
	}
	res.Removed = len(old) - res.Unchanged - res.Changed

	if len(toHash) > 0 {
		paths := make([]string, len(toHash))
		for i, f := range toHash {
			paths[i] = f.Path
		}
		done := 0
		sums, err := loc.HashAll(ctx, paths, func() {
			done++
			if progress != nil {
				progress("hash", done, len(paths))
			}
		})
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", loc.Name(), err)
		}

		prefixes := batchPrefixes(ctx, loc, toHash, progress)

		metaDone := 0
		for _, f := range toHash {
			e := catalog.Entry{
				Path:      f.Path,
				Size:      f.Size,
				MTime:     f.MTime,
				SHA256:    sums[f.Path],
				ScannedAt: now,
			}
			if audio.IsAudioPath(f.Path) {
				meta, err := parseWithFallback(ctx, loc, f.Path, prefixes[f.Path])
				if err != nil {
					e.AudioErr = err.Error()
					res.AudioErrs++
				} else {
					e.Audio = meta
				}
			}
			entries[f.Path] = e
			metaDone++
			if progress != nil {
				progress("meta", metaDone, len(toHash))
			}
		}
	}

	if err := catalog.Write(catalogPath, entries); err != nil {
		return nil, err
	}
	return res, nil
}
