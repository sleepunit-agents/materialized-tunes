// Package catalog stores the scanned index of a source location as a JSONL
// file: one entry per source file, sorted by path. Flat and greppable on
// purpose — the catalog should still be readable years from now with no
// tooling at all.
package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jbarket/materialized-tunes/internal/audio"
)

type Entry struct {
	Path      string      `json:"path"` // relative to the location root
	Size      int64       `json:"size"`
	MTime     int64       `json:"mtime"` // unix seconds
	SHA256    string      `json:"sha256"`
	Audio     *audio.Meta `json:"audio,omitempty"`
	AudioErr  string      `json:"audio_err,omitempty"` // why Audio is absent
	ScannedAt time.Time   `json:"scanned_at"`
}

// Load reads a catalog file into a path-keyed map. A missing file is an
// empty catalog, not an error.
func Load(path string) (map[string]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	entries := map[string]Entry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		entries[e.Path] = e
	}
	return entries, sc.Err()
}

// Write persists entries sorted by path, atomically.
func Write(path string, entries map[string]Entry) error {
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, p := range paths {
		if err := enc.Encode(entries[p]); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
