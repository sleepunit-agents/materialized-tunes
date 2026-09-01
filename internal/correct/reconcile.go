package correct

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Reconciliation (SPEC §19.5): after a sync, a local entry the checkout
// now agrees with is a shadow — remove it and nothing moves. Without this
// the local layer becomes a permanent second source of truth. Reconcile
// judges every entry by taking it away in memory and re-harvesting the
// files it covers; Drop removes the ones the user lets go.

// Verdict is one local entry judged.
type Verdict struct {
	LocalEntry
	Location  string `json:"location,omitempty"`
	Prefix    string `json:"prefix,omitempty"` // the location path the entry covers
	Covered   int    `json:"covered"`
	Changed   int    `json:"changed"`   // files whose category or instrument would differ without it
	Redundant bool   `json:"redundant"` // covered, and nothing would change
	Unmatched bool   `json:"unmatched"` // no cataloged file under it anywhere — nothing to judge
}

// Sources are the location inputs Reconcile reads — the plan's Inputs,
// or fresh loads.
type Sources struct {
	Catalog func(location string) (map[string]catalog.Entry, error)
	Meta    func(location string) map[string]harvest.Meta
}

// Reconcile judges every entry in the local layer against the checkout.
func Reconcile(ws *workspace.Workspace, src Sources) ([]Verdict, error) {
	entries, err := List(ws)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	repo, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
	if err != nil {
		return nil, err
	}
	local, err := annotations.Load(ws.LocalAnnotations())
	if err != nil {
		return nil, err
	}
	full := annotations.Overlay(repo, local)
	// one pass per location: the pack directories its catalog holds
	packIdx := map[string]*packIndex{}
	for _, lc := range ws.Config.Locations {
		cat, err := src.Catalog(lc.Name)
		if err != nil {
			continue
		}
		packIdx[lc.Name] = indexPacks(lc, cat, full)
	}
	// entries are independent and the re-harvest is pure: judge them in
	// parallel, keep the listing's order
	out := make([]Verdict, len(entries))
	errs := make([]error, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e LocalEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = judge(ws, src, repo, local, full, packIdx, e)
		}(i, e)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// judge takes one entry away in memory and re-harvests what it covers.
func judge(ws *workspace.Workspace, src Sources, repo, local, full []annotations.Vendor, packIdx map[string]*packIndex, e LocalEntry) (Verdict, error) {
	v := Verdict{LocalEntry: e}
	vendor := annotations.BySlug(full)[e.Vendor]
	if vendor == nil {
		vendor = byDirName(full, e.Vendor)
	}
	var pack *annotations.Pack
	if vendor != nil {
		pack = packBySlug(vendor, e.Pack)
	}
	if vendor == nil || pack == nil || pack.Dir == "" {
		v.Unmatched = true
		return v, nil
	}
	for _, lc := range ws.Config.Locations {
		cat, err := src.Catalog(lc.Name)
		if err != nil {
			continue
		}
		packPath := packIdx[lc.Name].pathOf(vendor.Slug, pack.Dir)
		if packPath == "" {
			continue
		}
		prefix := packPath
		if e.Kind == "dir" {
			if p, _ := e.Entry["path"].(string); p != "" {
				prefix = packPath + "/" + strings.Trim(p, "/")
			}
		}
		without := annotations.Overlay(repo, minus(local, e))
		after, err := harvest.ExplainPrefix(ws, lc, cat, prefix, without)
		if err != nil {
			return v, err
		}
		if len(after) == 0 {
			continue
		}
		current := src.Meta(lc.Name)
		v.Location, v.Prefix, v.Covered = lc.Name, prefix, len(after)
		for p, a := range after {
			if b := current[p]; b.Category != a.Category || b.Instrument != a.Instrument {
				v.Changed++
			}
		}
		break
	}
	if v.Covered == 0 {
		v.Unmatched = true
	} else {
		v.Redundant = v.Changed == 0
	}
	return v, nil
}

func byDirName(vendors []annotations.Vendor, dir string) *annotations.Vendor {
	for i := range vendors {
		if Slugify(vendors[i].Slug) == dir || Slugify(vendors[i].Name) == dir {
			return &vendors[i]
		}
	}
	return nil
}

func packBySlug(v *annotations.Vendor, slug string) *annotations.Pack {
	for i := range v.Packs {
		if v.Packs[i].Slug == slug {
			return &v.Packs[i]
		}
	}
	return nil
}

// packIndex is one location's pack directories, keyed by the vendor
// slug the top dir resolves to (or the location's vendor when flat).
type packIndex struct {
	paths map[string]string // "<slug>\x00<pack dir>" → location path through the pack dir
}

func indexPacks(lc workspace.LocationConfig, cat map[string]catalog.Entry, vendors []annotations.Vendor) *packIndex {
	ix := &packIndex{paths: map[string]string{}}
	if lc.Layout != "vendor-dirs" {
		for p := range cat {
			if i := strings.IndexByte(p, '/'); i > 0 {
				ix.paths[lc.Vendor+"\x00"+p[:i]] = p[:i]
			}
		}
		return ix
	}
	slugOf := map[string]string{} // top dir → vendor slug ("" when unknown)
	for p := range cat {
		i := strings.IndexByte(p, '/')
		if i <= 0 {
			continue
		}
		top := p[:i]
		j := strings.IndexByte(p[i+1:], '/')
		if j <= 0 {
			continue
		}
		slug, seen := slugOf[top]
		if !seen {
			if v := annotations.ByName(vendors, top); v != nil {
				slug = v.Slug
			}
			slugOf[top] = slug
		}
		if slug == "" {
			continue
		}
		packDir := p[i+1 : i+1+j]
		ix.paths[slug+"\x00"+packDir] = top + "/" + packDir
	}
	return ix
}

func (ix *packIndex) pathOf(slug, packDir string) string {
	if ix == nil {
		return ""
	}
	return ix.paths[slug+"\x00"+packDir]
}

// minus is the local layer without one entry.
func minus(local []annotations.Vendor, e LocalEntry) []annotations.Vendor {
	out := make([]annotations.Vendor, len(local))
	for i, v := range local {
		out[i] = v
		if v.Slug != e.Vendor && filepath.Base(vendorDir(v)) != e.Vendor && Slugify(v.Name) != e.Vendor {
			continue
		}
		out[i].Packs = append([]annotations.Pack(nil), v.Packs...)
		for j, p := range out[i].Packs {
			if p.Slug != e.Pack {
				continue
			}
			switch e.Kind {
			case "dir":
				want, _ := e.Entry["path"].(string)
				var keep []annotations.Dir
				for _, d := range p.Dirs {
					if d.Path != want {
						keep = append(keep, d)
					}
				}
				out[i].Packs[j].Dirs = keep
			case "instrument":
				want, _ := e.Entry["id"].(string)
				var keep []annotations.Instrument
				for _, ins := range p.Instruments {
					if ins.ID != want || !sameAliases(ins.Aliases, e.Entry["aliases"]) {
						keep = append(keep, ins)
					}
				}
				out[i].Packs[j].Instruments = keep
			}
		}
	}
	return out
}

func vendorDir(v annotations.Vendor) string { return v.Slug }

func sameAliases(have []string, want any) bool {
	var w []string
	switch l := want.(type) {
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok {
				w = append(w, s)
			}
		}
	case []string:
		w = l
	}
	if len(w) != len(have) {
		return false
	}
	a, b := append([]string(nil), have...), append([]string(nil), w...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Drop removes one entry from the local layer: the file is rewritten
// without it, deleted when nothing but a borrowed identity is left, and
// the drop is logged. It does not touch the meta cache — a redundant
// entry changed nothing, and a kept-then-dropped one is the user's call
// that the next harvest will honour.
func Drop(ws *workspace.Workspace, e LocalEntry, reason string) error {
	pf := filepath.Join(ws.LocalAnnotations(), filepath.FromSlash(e.File))
	doc := map[string]any{}
	data, err := os.ReadFile(pf)
	if err != nil {
		return err
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", e.File, err)
	}
	var kept []map[string]any
	removed := false
	for _, m := range tableList(doc[e.Kind]) {
		match := false
		switch e.Kind {
		case "dir":
			match = m["path"] == e.Entry["path"]
		case "instrument":
			match = m["id"] == e.Entry["id"] && sameAliases(strs(m["aliases"]), e.Entry["aliases"])
		}
		if match && !removed {
			removed = true
			continue
		}
		kept = append(kept, m)
	}
	if !removed {
		return fmt.Errorf("%s: entry not found", e.File)
	}
	if len(kept) > 0 {
		doc[e.Kind] = kept
	} else {
		delete(doc, e.Kind)
	}
	left := len(tableList(doc["dir"])) + len(tableList(doc["instrument"]))
	newPack := false
	if pk, ok := doc["pack"].(map[string]any); ok {
		_, newPack = pk["name"]
	}
	if left == 0 && !newPack {
		if err := os.Remove(pf); err != nil {
			return err
		}
		os.Remove(filepath.Dir(pf)) // packs/, only if empty
	} else if err := writeDoc(pf, doc); err != nil {
		return err
	}
	return appendLog(ws, logEntry{At: time.Now().UTC().Format(time.RFC3339), Kind: "drop",
		Correction: Correction{Path: path.Join(e.Vendor, e.Pack, fmt.Sprint(e.Entry["path"])), Note: reason},
		Target:     Target{VendorSlug: e.Vendor, PackSlug: e.Pack, File: e.File}})
}

func strs(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
