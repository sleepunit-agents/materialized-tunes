// Package correct turns what a user saw in the plan into a fact the
// annotation schema can hold, writes it to the local layer, and shows the
// blast radius first (SPEC §19.3, §19.5). A correction is scoped — a
// [[dir]] entry or a pack [[instrument]] block on a folder or pack the
// user owns — never a change to the shared lexicons. Every correction
// previews as an overlay in memory over the loaded annotations, re-harvests
// only the files it covers, and reports what would move before anything is
// written. Applying writes the entry into <workspace>/annotations.local/ in
// the repo's own layout, logs the evidence to corrections.jsonl, and
// patches the meta cache for the covered files so the next plan sees it.
package correct

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Correction is one assertion about the user's own copy.
type Correction struct {
	Location string `json:"location"`
	// Path is the folder the assertion covers, as a catalog path within
	// the location — or a glob over it ("Samples From Mars/Dr Sample From
	// Mars/WAV/07. Textures/Chop *.wav"). The vendor and pack segments
	// must be literal: a correction is scoped to one pack.
	Path string `json:"path"`
	// Facet is what is asserted: category | instrument (a [[dir]] pin or
	// default), role (a [[dir]] role: format-tree | docs), or alias — Word
	// means Value inside this pack (a pack [[instrument]] block).
	Facet string `json:"facet"`
	Value string `json:"value"`
	// Mode is pin (the default: beats the filenames) or default (speaks
	// last, only where nothing else did). Only category and instrument
	// have a default form.
	Mode  string `json:"mode,omitempty"`
	Word  string `json:"word,omitempty"` // alias only
	Note  string `json:"note,omitempty"`
	Local bool   `json:"local,omitempty"` // keep out of any export — my opinion, not a fact for the repo
}

// Target is where a correction lands: the vendor and pack files in the
// local layer, and the path inside the pack the entry addresses.
type Target struct {
	VendorSlug string `json:"vendor"`
	VendorName string `json:"vendor_name"`
	PackSlug   string `json:"pack"`
	PackName   string `json:"pack_name"`
	PackDir    string `json:"pack_dir"`
	PackPath   string `json:"pack_path"` // location path through the pack dir
	InPack     string `json:"in_pack"`   // the [[dir]] path: a dir inside the pack, or a glob
	NewVendor  bool   `json:"new_vendor,omitempty"`
	NewPack    bool   `json:"new_pack,omitempty"`
	File       string `json:"file"` // the local pack file, relative to annotations.local
}

// Change is one before → after group in a blast radius.
type Change struct {
	From     string   `json:"from"` // "category · instrument" as it resolves now ("—" for nothing)
	To       string   `json:"to"`
	Count    int      `json:"count"`
	Examples []string `json:"examples"`
}

// Radius is what a correction would do — seeing before doing, for the fix.
type Radius struct {
	Target  Target   `json:"target"`
	Covered int      `json:"covered"` // audio files under the path
	Changed int      `json:"changed"` // whose category or instrument differs after
	Moved   int      `json:"moved"`   // of those, files that resolved to something else before — the ones to look at
	Filled  int      `json:"filled"`  // of those, files that had nothing on the facet before
	Changes []Change `json:"changes"`

	after map[string]harvest.Meta
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a repo-style slug from a directory name.
func Slugify(name string) string {
	return strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Resolve finds the vendor and pack a location path belongs to under the
// location's layout, and the path inside the pack. Unknown vendors and
// packs are named from their directories — the local layer will carry
// the identity.
func Resolve(lc workspace.LocationConfig, vendors []annotations.Vendor, p string) (Target, error) {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	segs := strings.Split(p, "/")
	var t Target
	var vendor *annotations.Vendor
	packIdx := 0
	if lc.Layout == "vendor-dirs" {
		if len(segs) < 2 {
			return t, fmt.Errorf("%s: a correction is scoped to a pack — the path must reach one (<vendor>/<pack>/…)", p)
		}
		packIdx = 1
		vendor = annotations.ByName(vendors, segs[0])
		t.VendorName = segs[0]
		t.VendorSlug = Slugify(segs[0])
	} else {
		if len(segs) < 1 || segs[0] == "" {
			return t, fmt.Errorf("%s: a correction is scoped to a pack — the path must reach one", p)
		}
		vendor = annotations.BySlug(vendors)[lc.Vendor]
		t.VendorSlug, t.VendorName = lc.Vendor, lc.Vendor
		if t.VendorSlug == "" {
			t.VendorSlug, t.VendorName = Slugify(lc.Name), lc.Name
		}
	}
	for _, seg := range segs[:packIdx+1] {
		if strings.ContainsAny(seg, "*?[{") {
			return t, fmt.Errorf("%s: the vendor and pack segments must be literal — one correction, one pack", p)
		}
	}
	if vendor != nil {
		t.VendorSlug, t.VendorName = vendor.Slug, vendor.Name
	} else {
		t.NewVendor = true
	}
	t.PackDir = segs[packIdx]
	t.PackPath = strings.Join(segs[:packIdx+1], "/")
	t.InPack = strings.Join(segs[packIdx+1:], "/")
	var pack *annotations.Pack
	if vendor != nil {
		pack = vendor.PackByDir(t.PackDir)
	}
	if pack != nil {
		t.PackSlug, t.PackName = pack.Slug, pack.Name
	} else {
		t.PackSlug, t.PackName, t.NewPack = Slugify(t.PackDir), t.PackDir, true
	}
	if t.PackSlug == "" {
		return t, fmt.Errorf("%s: cannot name a pack for this path", p)
	}
	t.File = path.Join("vendors", t.VendorSlug, "packs", t.PackSlug+".toml")
	return t, nil
}

// validate checks the correction says something the schema can hold.
func (c Correction) validate() error {
	if c.Mode == "" {
		c.Mode = "pin"
	}
	switch c.Facet {
	case "category", "instrument":
		if c.Value == "" {
			return fmt.Errorf("%s: a value is required", c.Facet)
		}
		if c.Mode != "pin" && c.Mode != "default" {
			return fmt.Errorf("mode %q: want pin or default", c.Mode)
		}
	case "role":
		if c.Value != "format-tree" && c.Value != "docs" {
			return fmt.Errorf("role %q: want format-tree or docs", c.Value)
		}
	case "alias":
		if c.Value == "" || strings.TrimSpace(c.Word) == "" {
			return fmt.Errorf("alias: both the word and the instrument it means are required")
		}
	default:
		return fmt.Errorf("facet %q: want category, instrument, role or alias", c.Facet)
	}
	if strings.TrimSpace(c.Path) == "" {
		return fmt.Errorf("a path is required")
	}
	return nil
}

// dirEntry is the [[dir]] entry a category / instrument / role correction writes.
func (c Correction) dirEntry(inPack string) annotations.Dir {
	d := annotations.Dir{Path: inPack}
	d.Observed, d.Note, d.Local = time.Now().Format("2006-01-02"), c.Note, c.Local
	switch {
	case c.Facet == "category" && c.Mode == "default":
		d.DefaultCategory = c.Value
	case c.Facet == "category":
		d.Category = c.Value
	case c.Facet == "instrument" && c.Mode == "default":
		d.DefaultInstrument = c.Value
	case c.Facet == "instrument":
		d.Instrument = c.Value
	case c.Facet == "role":
		d.Role = c.Value
	}
	return d
}

// instrumentEntry is the pack [[instrument]] block an alias correction writes.
func (c Correction) instrumentEntry() annotations.Instrument {
	ins := annotations.Instrument{ID: c.Value, Aliases: []string{strings.TrimSpace(c.Word)}, Scope: "pack"}
	ins.Observed, ins.Note, ins.Local = time.Now().Format("2006-01-02"), c.Note, c.Local
	return ins
}

// overlay is the correction as a one-vendor annotation layer.
func (c Correction) overlay(t Target) []annotations.Vendor {
	pk := annotations.Pack{Slug: t.PackSlug, Name: t.PackName, Dir: t.PackDir}
	if c.Facet == "alias" {
		pk.Instruments = []annotations.Instrument{c.instrumentEntry()}
	} else {
		pk.Dirs = []annotations.Dir{c.dirEntry(t.InPack)}
	}
	return []annotations.Vendor{{Slug: t.VendorSlug, Name: t.VendorName, Packs: []annotations.Pack{pk}}}
}

// prefix is the location path the correction covers, for the re-harvest.
func (c Correction) prefix() string {
	return strings.Trim(strings.ReplaceAll(c.Path, "\\", "/"), "/")
}

// Preview lays the correction over the loaded annotations in memory and
// re-harvests the files it covers. entries is the location's catalog,
// current its meta cache as it stands.
func Preview(ws *workspace.Workspace, lc workspace.LocationConfig, entries map[string]catalog.Entry, current map[string]harvest.Meta, vendors []annotations.Vendor, c Correction) (*Radius, error) {
	if c.Mode == "" {
		c.Mode = "pin"
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	t, err := Resolve(lc, vendors, c.Path)
	if err != nil {
		return nil, err
	}
	scope := c.prefix()
	if c.Facet == "alias" {
		// a word means something across the whole pack
		scope = t.PackPath
	}
	after, err := harvest.ExplainPrefix(ws, lc, entries, scope, annotations.Overlay(vendors, c.overlay(t)))
	if err != nil {
		return nil, err
	}
	r := &Radius{Target: t, Covered: len(after), after: after}
	groups := map[[2]string]*Change{}
	for _, p := range sortedKeys(after) {
		b, a := current[p], after[p]
		if b.Category == a.Category && b.Instrument == a.Instrument {
			continue
		}
		r.Changed++
		facetBefore := b.Category
		facetAfter := a.Category
		if c.Facet == "instrument" || c.Facet == "alias" {
			facetBefore, facetAfter = b.Instrument, a.Instrument
		}
		switch {
		case facetBefore == "" && facetAfter != "":
			r.Filled++
		case facetBefore != "" && facetBefore != facetAfter:
			r.Moved++
		}
		key := [2]string{label(b), label(a)}
		g := groups[key]
		if g == nil {
			g = &Change{From: key[0], To: key[1]}
			groups[key] = g
		}
		g.Count++
		if len(g.Examples) < 3 {
			g.Examples = append(g.Examples, p)
		}
	}
	for _, g := range groups {
		r.Changes = append(r.Changes, *g)
	}
	sort.Slice(r.Changes, func(i, j int) bool {
		if r.Changes[i].Count != r.Changes[j].Count {
			return r.Changes[i].Count > r.Changes[j].Count
		}
		return r.Changes[i].From+r.Changes[i].To < r.Changes[j].From+r.Changes[j].To
	})
	return r, nil
}

func label(m harvest.Meta) string {
	c, i := m.Category, m.Instrument
	if c == "" {
		c = "—"
	}
	if i == "" {
		i = "—"
	}
	return c + " · " + i
}

func sortedKeys(m map[string]harvest.Meta) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Provenance of a correction's log entry: what the app was and what it
// had resolved, so the annotator can triage a submission into
// annotation-gap, lexicon-bug or parser-bug without re-deriving it.
type Provenance struct {
	AppVersion     string `json:"app_version"`
	AnnotationsSHA string `json:"annotations_sha,omitempty"`
}

// Apply previews, then writes: the entry into the local layer, the
// evidence into corrections.jsonl, the covered files into the meta cache.
func Apply(ws *workspace.Workspace, lc workspace.LocationConfig, entries map[string]catalog.Entry, current map[string]harvest.Meta, vendors []annotations.Vendor, c Correction, prov Provenance) (*Radius, error) {
	if c.Mode == "" {
		c.Mode = "pin"
	}
	r, err := Preview(ws, lc, entries, current, vendors, c)
	if err != nil {
		return nil, err
	}
	if err := writeEntry(ws, r.Target, c); err != nil {
		return nil, err
	}
	before := majority(current, r.after)
	if err := appendLog(ws, logEntry{
		At: time.Now().UTC().Format(time.RFC3339), Provenance: prov, Correction: c, Target: r.Target,
		Before: before, Covered: r.Covered, Changed: r.Changed, Moved: r.Moved, Filled: r.Filled,
	}); err != nil {
		return nil, err
	}
	if err := harvest.Patch(ws, lc, r.after); err != nil {
		return nil, err
	}
	return r, nil
}

type logEntry struct {
	At string `json:"at"`
	Provenance
	Kind       string     `json:"kind,omitempty"` // "" (a correction) | report | ack
	Correction Correction `json:"correction"`
	Target     Target     `json:"target,omitempty"`
	Before     *facets    `json:"before,omitempty"`
	Covered    int        `json:"covered,omitempty"`
	Changed    int        `json:"changed,omitempty"`
	Moved      int        `json:"moved,omitempty"`
	Filled     int        `json:"filled,omitempty"`
}

// facets is the majority resolution over a set of files, with the tier
// that produced it — the "what the app had resolved, and via which tier"
// the log carries.
type facets struct {
	Category      string              `json:"category,omitempty"`
	CategoryWhy   *annotations.Source `json:"category_why,omitempty"`
	Instrument    string              `json:"instrument,omitempty"`
	InstrumentWhy *annotations.Source `json:"instrument_why,omitempty"`
}

func majority(current map[string]harvest.Meta, covered map[string]harvest.Meta) *facets {
	cat, inst := map[string]int{}, map[string]int{}
	catWhy, instWhy := map[string]*annotations.Source{}, map[string]*annotations.Source{}
	for p := range covered {
		m := current[p]
		cat[m.Category]++
		inst[m.Instrument]++
		if m.Why != nil {
			if m.Why.Category != nil && catWhy[m.Category] == nil {
				catWhy[m.Category] = m.Why.Category
			}
			if m.Why.Instrument != nil && instWhy[m.Instrument] == nil {
				instWhy[m.Instrument] = m.Why.Instrument
			}
		}
	}
	top := func(m map[string]int) string {
		best, bestN := "", -1
		for k, n := range m {
			if n > bestN || (n == bestN && k < best) {
				best, bestN = k, n
			}
		}
		return best
	}
	f := &facets{Category: top(cat), Instrument: top(inst)}
	f.CategoryWhy, f.InstrumentWhy = catWhy[f.Category], instWhy[f.Instrument]
	return f
}

// Report logs a kind-D "this is your parser, not my pack" with no TOML.
func Report(ws *workspace.Workspace, c Correction, current map[string]harvest.Meta, prov Provenance) error {
	var before *facets
	if m, ok := current[c.prefix()]; ok {
		before = majority(current, map[string]harvest.Meta{c.prefix(): m})
	}
	return appendLog(ws, logEntry{At: time.Now().UTC().Format(time.RFC3339), Provenance: prov, Kind: "report", Correction: c, Before: before})
}

// Ack records a folder reviewed and left as-is (kind C): it leaves the
// queue without a label being invented for it. Not an annotation —
// nothing to submit.
func Ack(ws *workspace.Workspace, location, folder, note string) error {
	f, err := os.OpenFile(filepath.Join(ws.LocalAnnotations(), "acks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		if err := os.MkdirAll(ws.LocalAnnotations(), 0o755); err != nil {
			return err
		}
		if f, err = os.OpenFile(filepath.Join(ws.LocalAnnotations(), "acks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return err
		}
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(map[string]string{
		"at": time.Now().UTC().Format(time.RFC3339), "location": location, "path": strings.Trim(strings.ReplaceAll(folder, "\\", "/"), "/"), "note": note,
	})
}

// Acks reads the ack list: location\x00folder → true.
func Acks(ws *workspace.Workspace) map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(filepath.Join(ws.LocalAnnotations(), "acks.jsonl"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var a struct{ Location, Path string }
		if json.Unmarshal(sc.Bytes(), &a) == nil && a.Path != "" {
			out[a.Location+"\x00"+a.Path] = true
		}
	}
	return out
}

func appendLog(ws *workspace.Workspace, e logEntry) error {
	if err := os.MkdirAll(ws.LocalAnnotations(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(ws.LocalAnnotations(), "corrections.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(e)
}

// writeEntry merges the correction into the local pack file (and creates
// the vendor file when the checkout knows no such vendor). Files are read
// as generic TOML and written back whole; they are the app's to keep.
func writeEntry(ws *workspace.Workspace, t Target, c Correction) error {
	root := ws.LocalAnnotations()
	if t.NewVendor {
		vf := filepath.Join(root, "vendors", t.VendorSlug, "vendor.toml")
		if _, err := os.Stat(vf); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(vf), 0o755); err != nil {
				return err
			}
			body := fmt.Sprintf("# written by materialized-tunes — a vendor the annotations repo does not know yet\n[vendor]\nname = %q\nslug = %q\n", t.VendorName, t.VendorSlug)
			if err := os.WriteFile(vf, []byte(body), 0o644); err != nil {
				return err
			}
		}
	}
	pf := filepath.Join(root, filepath.FromSlash(t.File))
	doc := map[string]any{}
	if data, err := os.ReadFile(pf); err == nil {
		if err := toml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", t.File, err)
		}
	}
	pack, _ := doc["pack"].(map[string]any)
	if pack == nil {
		pack = map[string]any{}
	}
	pack["slug"] = t.PackSlug
	if t.NewPack {
		pack["name"], pack["dir"] = t.PackName, t.PackDir
	}
	doc["pack"] = pack

	if c.Facet == "alias" {
		ins := c.instrumentEntry()
		list, _ := doc["instrument"].([]map[string]any)
		if list == nil {
			if raw, ok := doc["instrument"].([]any); ok {
				for _, x := range raw {
					if m, ok := x.(map[string]any); ok {
						list = append(list, m)
					}
				}
			}
		}
		merged := false
		for _, m := range list {
			if m["id"] == ins.ID {
				aliases, _ := m["aliases"].([]any)
				aliases = append(aliases, ins.Aliases[0])
				m["aliases"] = aliases
				m["observed"], m["note"] = ins.Observed, orKeep(m["note"], ins.Note)
				if ins.Local {
					m["local"] = true
				}
				merged = true
			}
		}
		if !merged {
			e := map[string]any{"id": ins.ID, "aliases": []any{ins.Aliases[0]}, "observed": ins.Observed}
			if ins.Note != "" {
				e["note"] = ins.Note
			}
			if ins.Local {
				e["local"] = true
			}
			list = append(list, e)
		}
		doc["instrument"] = list
	} else {
		d := c.dirEntry(t.InPack)
		list, _ := doc["dir"].([]map[string]any)
		if list == nil {
			if raw, ok := doc["dir"].([]any); ok {
				for _, x := range raw {
					if m, ok := x.(map[string]any); ok {
						list = append(list, m)
					}
				}
			}
		}
		var entry map[string]any
		for _, m := range list {
			if m["path"] == d.Path {
				entry = m
			}
		}
		if entry == nil {
			entry = map[string]any{"path": d.Path}
			list = append(list, entry)
		}
		// one facet, one form: a pin replaces a default and vice versa
		switch {
		case d.Category != "":
			entry["category"], entry["default_category"] = d.Category, nil
		case d.DefaultCategory != "":
			entry["default_category"], entry["category"] = d.DefaultCategory, nil
		case d.Instrument != "":
			entry["instrument"], entry["default_instrument"] = d.Instrument, nil
		case d.DefaultInstrument != "":
			entry["default_instrument"], entry["instrument"] = d.DefaultInstrument, nil
		case d.Role != "":
			entry["role"] = d.Role
		}
		for k, v := range entry {
			if v == nil {
				delete(entry, k)
			}
		}
		entry["observed"] = d.Observed
		if d.Note != "" {
			entry["note"] = d.Note
		}
		if d.Local {
			entry["local"] = true
		} else {
			delete(entry, "local")
		}
		doc["dir"] = list
	}
	if err := os.MkdirAll(filepath.Dir(pf), 0o755); err != nil {
		return err
	}
	return writeDoc(pf, doc)
}

// writeDoc writes a pack file the way the repo lays one out: [pack]
// first, anything else next, then the [[instrument]] and [[dir]] blocks.
func writeDoc(pf string, doc map[string]any) error {
	var buf strings.Builder
	buf.WriteString("# written by materialized-tunes — your corrections for this pack, in the annotations repo's own layout\n\n")
	part := func(keys ...string) error {
		m := map[string]any{}
		for _, k := range keys {
			if v, ok := doc[k]; ok {
				m[k] = v
			}
		}
		if len(m) == 0 {
			return nil
		}
		enc := toml.NewEncoder(&buf)
		enc.Indent = ""
		if err := enc.Encode(m); err != nil {
			return err
		}
		buf.WriteString("\n")
		return nil
	}
	if err := part("pack"); err != nil {
		return err
	}
	var rest []string
	for k := range doc {
		if k != "pack" && k != "instrument" && k != "dir" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	if err := part(rest...); err != nil {
		return err
	}
	if err := part("instrument"); err != nil {
		return err
	}
	if err := part("dir"); err != nil {
		return err
	}
	return os.WriteFile(pf, []byte(strings.TrimRight(buf.String(), "\n")+"\n"), 0o644)
}

func orKeep(have any, want string) any {
	if want != "" {
		return want
	}
	return have
}
