package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Inputs are what a plan reads — catalogs, harvested metadata, the
// annotation layers — loaded once and shared across builds, so a second
// plan over the same library pays for placement and nothing else. On a
// 190k-file library the loads are most of a build, and the old preflight
// built once per rule and once more (SPEC §19.4).
//
// Stamp is a digest of the files those inputs come from (catalog and meta
// caches per location, every annotation TOML in both layers): equal
// stamps mean equal inputs, so it keys the cached plan artifact, and
// Fresh re-stats them to say whether this Inputs still describes the
// workspace.
type Inputs struct {
	ws    *workspace.Workspace
	stamp string

	mu       sync.Mutex
	catalogs map[string]map[string]catalog.Entry
	meta     map[string]map[string]harvest.Meta
	vendors  []annotations.Vendor
	loaded   bool
	lex      *annotations.Lexicon
}

// NewInputs stamps the workspace; nothing is loaded until asked for.
func NewInputs(ws *workspace.Workspace) *Inputs {
	return &Inputs{ws: ws, stamp: inputStamp(ws),
		catalogs: map[string]map[string]catalog.Entry{}, meta: map[string]map[string]harvest.Meta{}}
}

// Stamp identifies the workspace state these inputs were loaded from.
func (in *Inputs) Stamp() string { return in.stamp }

// Fresh reports whether the workspace's input files still match the stamp.
func (in *Inputs) Fresh() bool { return inputStamp(in.ws) == in.stamp }

// Reset drops everything loaded and re-stamps — after a harvest or a
// sync changed the files underneath.
func (in *Inputs) Reset() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.catalogs, in.meta = map[string]map[string]catalog.Entry{}, map[string]map[string]harvest.Meta{}
	in.vendors, in.loaded, in.lex = nil, false, nil
	in.stamp = inputStamp(in.ws)
}

// Catalog is one location's catalog.
func (in *Inputs) Catalog(location string) (map[string]catalog.Entry, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if c, ok := in.catalogs[location]; ok {
		return c, nil
	}
	c, err := catalog.Load(in.ws.CatalogPath(location))
	if err != nil {
		return nil, err
	}
	in.catalogs[location] = c
	return c, nil
}

// Meta is one location's harvested metadata (empty when never harvested).
func (in *Inputs) Meta(location string) map[string]harvest.Meta {
	in.mu.Lock()
	defer in.mu.Unlock()
	if m, ok := in.meta[location]; ok {
		return m
	}
	m := harvest.LoadMeta(in.ws, location)
	in.meta[location] = m
	return m
}

// Vendors are the merged annotation layers.
func (in *Inputs) Vendors() ([]annotations.Vendor, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.loaded {
		return in.vendors, nil
	}
	v, err := annotations.Load(in.ws.AnnotationRoots()...)
	if err != nil {
		return nil, err
	}
	in.vendors, in.loaded = v, true
	return v, nil
}

// Lexicon is the shared instrument lexicon (the checkout's; the local
// layer holds no lexicon — SPEC §19.3 rule 1).
func (in *Inputs) Lexicon() *annotations.Lexicon {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.lex == nil {
		in.lex = annotations.LoadInstruments(filepath.Join(in.ws.Root, "annotations"))
	}
	return in.lex
}

// inputStamp digests (path, size, mtime) of every file a plan reads.
func inputStamp(ws *workspace.Workspace) string {
	var lines []string
	stat := func(p string) {
		if st, err := os.Stat(p); err == nil {
			lines = append(lines, fmt.Sprintf("%s\x00%d\x00%d", p, st.Size(), st.ModTime().UnixNano()))
		}
	}
	for _, lc := range ws.Config.Locations {
		stat(ws.CatalogPath(lc.Name))
		stat(filepath.Join(ws.Root, "annotations-cache", "meta", lc.Name+".jsonl"))
	}
	stat(filepath.Join(ws.Root, "annotations-cache", "meta", ".format"))
	for _, root := range ws.AnnotationRoots() {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".toml") {
				stat(p)
			}
			return nil
		})
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:8])
}
