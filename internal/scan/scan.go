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

		for i, f := range toHash {
			e := catalog.Entry{
				Path:      f.Path,
				Size:      f.Size,
				MTime:     f.MTime,
				SHA256:    sums[f.Path],
				ScannedAt: now,
			}
			if audio.IsAudioPath(f.Path) {
				prefix, err := loc.ReadPrefix(ctx, f.Path, headerPrefixBytes)
				if err != nil {
					e.AudioErr = err.Error()
				} else if meta, err := audio.Parse(bytes.NewReader(prefix), f.Path); err != nil {
					e.AudioErr = err.Error()
				} else {
					e.Audio = meta
				}
				if e.AudioErr != "" {
					res.AudioErrs++
				}
			}
			entries[f.Path] = e
			if progress != nil {
				progress("meta", i+1, len(toHash))
			}
		}
	}

	if err := catalog.Write(catalogPath, entries); err != nil {
		return nil, err
	}
	return res, nil
}
