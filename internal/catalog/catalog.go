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
	"sync"
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

// Load returns a catalog as a path-keyed map. A missing file is an empty
// catalog, not an error.
//
// Decoded catalogs are shared process-wide: one decode per catalog file
// version, however many readers ask. Every surface that touches a
// location — the summary, the library, a plan's inputs, a harvest, the
// sample lists — used to decode the JSONL from disk on every call, and an
// archive drive's catalog is hundreds of megabytes of Live-set refs; the
// Plan screen sat on "loading catalogs 4/7" every visit while the launch
// re-harvest was bumping the inputs stamp underneath it (2026-09-02). The
// map is validated against the file's size and mtime on each ask and
// dropped by Write, so a rescan or an outside rewrite is seen. Callers
// must treat the map as read-only — it is the same map everyone holds.
func Load(path string) (map[string]Entry, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Entry{}, nil
		}
		return nil, err
	}
	shared.mu.Lock()
	c := shared.m[path]
	if c == nil {
		c = &cached{}
		shared.m[path] = c
	}
	shared.mu.Unlock()

	// one decode at a time per path: concurrent readers of a cold catalog
	// wait for the first instead of each decoding it
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries != nil && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.entries, nil
	}
	entries, err := read(path)
	if err != nil {
		return nil, err
	}
	// the file may have been replaced while we read; only remember a
	// decode whose stamp still matches, so the next ask re-reads
	if after, err := os.Stat(path); err == nil && after.Size() == st.Size() && after.ModTime().Equal(st.ModTime()) {
		c.size, c.mtime, c.entries = st.Size(), st.ModTime(), entries
	}
	return entries, nil
}

type cached struct {
	mu      sync.Mutex
	size    int64
	mtime   time.Time
	entries map[string]Entry
}

var shared = struct {
	mu sync.Mutex
	m  map[string]*cached
}{m: map[string]*cached{}}

// Invalidate forgets the shared decode of path. Write calls it; a writer
// that bypasses Write (a test, an outside tool) needs only to change the
// file's size or mtime.
func Invalidate(path string) {
	shared.mu.Lock()
	delete(shared.m, path)
	shared.mu.Unlock()
}

// read decodes a catalog file.
//
// Lines are read with a streaming decoder, not a line scanner: a Live
// set's entry carries every sample it references, and on an archive
// drive that is one JSONL line that can run past a megabyte. A scanner's
// line cap turned one such document into "token too long" for the whole
// location — every reader of that catalog failed, and the app opened to
// an empty window (2026-09-02).
func read(path string) (map[string]Entry, error) {
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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	Invalidate(path)
	return nil
}
