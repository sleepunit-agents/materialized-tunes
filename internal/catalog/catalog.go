// Package catalog stores the scanned index of a source location as a JSONL
// file: one entry per source file, sorted by path. Flat and greppable on
// purpose — the catalog should still be readable years from now with no
// tooling at all.
package catalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
)

type Entry struct {
	Path      string       `json:"path"` // relative to the location root
	Size      int64        `json:"size"`
	MTime     int64        `json:"mtime"` // unix seconds
	SHA256    string       `json:"sha256"`
	Audio     *audio.Meta  `json:"audio,omitempty"`
	AudioErr  string       `json:"audio_err,omitempty"` // why Audio is absent
	Doc       *ableton.Doc `json:"doc,omitempty"`       // Ableton document (.adg/.adv/.als): the sample refs it carries
	DocErr    string       `json:"doc_err,omitempty"`   // why Doc is absent on a document-shaped file
	ScannedAt time.Time    `json:"scanned_at"`
}

// Load reads a catalog file into a path-keyed map. A missing file is an
// empty catalog, not an error.
//
// Lines are read with a streaming decoder, not a line scanner: a Live
// set's entry carries every sample it references, and on an archive
// drive that is one JSONL line that can run past a megabyte. A scanner's
// line cap turned one such document into "token too long" for the whole
// location — every reader of that catalog failed, and the app opened to
// an empty window (2026-09-02).
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
	dec := json.NewDecoder(bufio.NewReaderSize(f, 256*1024))
	line := 0
	for {
		var e Entry
		err := dec.Decode(&e)
		if err == io.EOF {
			return entries, nil
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		entries[e.Path] = e
	}
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
