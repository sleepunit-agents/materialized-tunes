// Package harvest derives per-file metadata (bpm, key, category, tags)
// from what a vendor already told us in the filename and folder grammar —
// "RZA_Bass_03_C3.wav", "Hat Loop 03 124 Bpm.wav", "Champion Sub - 10A",
// "Bass Lines 166.5/" — plus the annotation layer's category and [[dir]]
// maps. Output is the workspace's annotations-cache/meta/<location>.jsonl,
// keyed by source path, which the UI already reads. Deterministic and
// cheap (string ops over the catalog), so it reruns after every scan.
//
// Path, not SHA: the labels come from the path, so the path is the key.
// Vendors ship the same bytes at more than one path — a Step Kit holds
// copies of the pack's own drum hits — and under a content key whichever
// copy harvests last writes the classification for every one of them.
package harvest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/version"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Meta is one harvested record. Same shape the UI's meta cache reads.
type Meta struct {
	Path       string   `json:"path"`
	SHA        string   `json:"sha"`
	BPM        int      `json:"bpm,omitempty"`
	Key        string   `json:"key,omitempty"`
	Chord      string   `json:"chord,omitempty"`
	Category   string   `json:"category,omitempty"`
	Instrument string   `json:"instrument,omitempty"`
	Family     string   `json:"family,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	// Why says which tier answered each facet and what it fired on —
	// the evidence a correction targets (SPEC §19.2). Absent facets have
	// no entry: nothing spoke.
	Why *Why `json:"why,omitempty"`
}

// Why is the per-facet provenance of one record.
type Why struct {
	Category   *annotations.Source `json:"category,omitempty"`
	Instrument *annotations.Source `json:"instrument,omitempty"`
}

// explain attaches a facet's source, allocating Why on first use.
func (m *Meta) explain(facet string, src annotations.Source) {
	if src.Tier == "" {
		return
	}
	if m.Why == nil {
		m.Why = &Why{}
	}
	switch facet {
	case "category":
		m.Why.Category = &src
	case "instrument":
		m.Why.Instrument = &src
	}
}

// Result summarizes one location's harvest.
type Result struct {
	Files, WithBPM, WithKey, WithCategory, WithTags int
	WithInstrument                                  int
}

var (
	bpmTokenRe = regexp.MustCompile(`(?i)(?:^|[\s_\-(])(\d{2,3}(?:\.\d)?)\s*bpm(?:$|[\s_\-)])`)
	bpmDirRe   = regexp.MustCompile(`^(.*\S)\s+(\d{2,3}(?:\.\d)?)$`)
	noteRe     = regexp.MustCompile(`[_ \-]([A-Ga-g][#b]?)(-?\d)$`)        // ..._C#4 / ..._C3 / ... A-1
	camelotRe  = regexp.MustCompile(`\s-\s(1[0-2]|[1-9])([ABab])$`)        // BMT: "Champion Sub - 10A"
	keyWordRe  = regexp.MustCompile(`(?i)[_ \-]([A-G][#b]?)(maj|min|m)?$`) // "Pad Amin", "Stab F#m"
	dirOrderRe = regexp.MustCompile(`^\d+\.\s*`)
	// a note token anywhere in a stem, boundaries explicit on both sides
	// (RE2 has no lookarounds): "…_C#4", "…_C-2-NBQM", "…_D1_C2IQ" — the
	// C2 inside the random suffix doesn't hit, "kit_a_1" is lowercase and
	// doesn't either. Uppercase only: lowercase letter+digit is how packs
	// name takes ("kit_a_1.wav"), not pitches.
	msNoteRe = regexp.MustCompile(`(?:^|[ _\-(.])([A-G][#b]?)(-?\d)(?:[ _\-).]|$)`)
)

// camelot → musical key. A = minor, B = major.
var camelot = map[string]string{
	"1A": "Abm", "2A": "Ebm", "3A": "Bbm", "4A": "Fm", "5A": "Cm", "6A": "Gm",
	"7A": "Dm", "8A": "Am", "9A": "Em", "10A": "Bm", "11A": "F#m", "12A": "Dbm",
	"1B": "B", "2B": "F#", "3B": "Db", "4B": "Ab", "5B": "Eb", "6B": "Bb",
	"7B": "F", "8B": "C", "9B": "G", "10B": "D", "11B": "A", "12B": "E",
}

// harvester is one location's harvest context: the annotations as
// loaded, the vendor lookup the location's layout needs, and the
// directory shapes computed over the paths in play. Building one is the
// fixed cost; one is per path and pure, which is what lets Explain and a
// partial re-harvest (SPEC §19.4) answer for a prefix without the whole
// pass.
type harvester struct {
	fixed      *annotations.Vendor
	vendors    []annotations.Vendor
	byTop      map[string]*annotations.Vendor
	vendorDirs bool
	lex        *annotations.Lexicon
	cats       *annotations.CategoryLexicon
	msDirs     map[string]bool
	// docs is the document tier's index: audio path → the Live documents
	// (catalog paths, sorted) whose refs resolve to it. See documentIndex.
	docs map[string][]string
}

// newHarvester loads the annotations and sizes up the directories the
// given audio paths live in. paths must include every audio sibling of
// any path later handed to one: the multisample tier reads the shape of
// the whole directory. entries is the location's whole catalog: the
// document tier resolves every Live document's refs against it.
func newHarvester(ws *workspace.Workspace, lc workspace.LocationConfig, paths []string, entries map[string]catalog.Entry) (*harvester, error) {
	vendors, err := annotations.Load(ws.AnnotationRoots()...)
	if err != nil {
		return nil, err
	}
	return newHarvesterWith(ws, lc, paths, entries, vendors), nil
}

// newHarvesterWith is newHarvester over annotations the caller already
// holds — or has altered in memory to see what would change.
func newHarvesterWith(ws *workspace.Workspace, lc workspace.LocationConfig, paths []string, entries map[string]catalog.Entry, vendors []annotations.Vendor) *harvester {
	return &harvester{
		fixed:      annotations.BySlug(vendors)[lc.Vendor],
		vendors:    vendors,
		byTop:      map[string]*annotations.Vendor{},
		vendorDirs: lc.Layout == "vendor-dirs",
		lex:        annotations.LoadInstruments(filepath.Join(ws.Root, "annotations")),
		cats:       annotations.LoadCategories(filepath.Join(ws.Root, "annotations")),
		msDirs:     multisampleDirs(paths),
		docs:       documentIndex(entries),
	}
}

// documentIndex inverts the catalog's Live documents: for every audio
// path, the documents whose refs resolve to it (ableton.Resolver, the
// same order plan and materialize use). The document tier reads it — a
// rack the vendor filed under Presets/Leads/ is the vendor saying what
// the samples it points at are. Lists come out sorted so the tier's
// first-wins / conflict verdicts pin.
func documentIndex(entries map[string]catalog.Entry) map[string][]string {
	var docs, audio []string
	for p, e := range entries {
		switch {
		case e.Audio != nil:
			audio = append(audio, p)
		case e.Doc != nil && len(e.Doc.Refs) > 0:
			docs = append(docs, p)
		}
	}
	if len(docs) == 0 {
		return nil
	}
	sort.Strings(audio)
	sort.Strings(docs)
	rs := ableton.NewResolver(audio)
	idx := map[string][]string{}
	for _, d := range docs {
		seen := map[string]bool{}
		for _, r := range entries[d].Doc.Refs {
			src, ok := rs.Resolve(d, r)
			if !ok || seen[src] {
				continue
			}
			seen[src] = true
			idx[src] = append(idx[src], d)
		}
	}
	return idx
}

// site is where a catalog path sits under the location's layout: the
// vendor and pack it belongs to, the pack's directory name, and the path
// inside the pack (last element = the filename).
type site struct {
	vendor  *annotations.Vendor
	pack    *annotations.Pack
	packDir string
	inPack  []string
}

// locate resolves a catalog path to its site. ok is false when the path
// is too shallow to sit inside a pack under the layout.
func (h *harvester) locate(p string) (s site, ok bool) {
	segs := strings.Split(p, "/")
	s.vendor = h.fixed
	packIdx := 0
	if h.vendorDirs {
		if len(segs) < 3 {
			return s, false
		}
		packIdx = 1
		v, seen := h.byTop[segs[0]]
		if !seen {
			v = annotations.ByName(h.vendors, segs[0])
			h.byTop[segs[0]] = v
		}
		s.vendor = v
	} else if len(segs) < 2 {
		return s, false
	}
	if s.vendor != nil {
		s.pack = s.vendor.PackByDir(segs[packIdx])
	}
	s.packDir = segs[packIdx]
	s.inPack = segs[packIdx+1:]
	return s, true
}

// one harvests a single catalog path. ok is false when the path is too
// shallow to sit inside a pack under the location's layout — there is
// nothing to say about it.
func (h *harvester) one(p string, e catalog.Entry) (m Meta, ok bool) {
	s, ok := h.locate(p)
	if !ok {
		return Meta{}, false
	}
	vendor, pack, inPack := s.vendor, s.pack, s.inPack
	segs := strings.Split(p, "/")
	packIdx := len(segs) - len(inPack) - 1
	m = Meta{Path: p, SHA: e.SHA256}
	base := strings.TrimSuffix(inPack[len(inPack)-1], filepath.Ext(inPack[len(inPack)-1]))
	dirs := inPack[:len(inPack)-1]

	// labels are the dirs that can describe a sound; a dir that only
	// restates the pack's name is not one, and speaks last (see labelDirs)
	labels, echoes := labelDirs(dirs, segs[packIdx], pack)

	m.BPM = harvestBPM(base, dirs, vendor)
	m.Key = harvestKey(base, vendor)
	var pinned string
	var catSrc, pinSrc annotations.Source
	var defaults dirDefaults
	m.Category, m.Tags, pinned, catSrc, pinSrc, defaults = harvestCategory(dirs, labels, vendor, pack, segs[packIdx])
	if m.Category == "" {
		// vendor annotation said nothing (or there is none) — the shared
		// lexicon reads the same folder/filename grammar cross-vendor
		m.Category, catSrc = h.cats.ResolveSrc(base, labels)
	}
	// overrides, most local first: the pack's own [[instrument]] blocks
	// (Drumtrax's "Bass" is its kick), then the vendor's (SFM's "CH"),
	// then the shared lexicon inside Resolve
	var overrides []annotations.Instrument
	if pack != nil {
		overrides = append(overrides, pack.Instruments...)
	}
	if vendor != nil {
		overrides = append(overrides, vendor.Instruments...)
	}
	var docConflict annotations.Source
	if m.Category == "" && len(h.docs[p]) > 0 {
		// nothing on the file's own path said; a Live document the vendor
		// filed under a labelled folder (Presets/Kits/) and pointed at
		// this file is the vendor's word about it, one step removed
		v := h.docCategory(p)
		if v.conflict {
			docConflict = v.src
		} else if v.value != "" {
			m.Category, catSrc = v.value, v.src
		}
	}
	if m.Category == "" && len(echoes) > 0 {
		// nothing on the path said; the pack's own name may ("Silk
		// Vocals" holds vocals) — the echo is a fallback, not a label
		c, _, _, src, _, _ := harvestCategory(dirs, echoes, vendor, pack, segs[packIdx])
		if c == "" {
			c, src = h.cats.ResolveSrc("", echoes)
		}
		src.Echo = true
		m.Category, catSrc = c, src
	}
	if m.Category == "" && h.msDirs[path.Dir(p)] {
		// no label anywhere claimed the file, but its directory has the
		// multisample shape — chromatic note-suffixed siblings
		m.Category = "multisamples"
		catSrc = annotations.Source{Tier: annotations.TierMultisample, Segment: strings.Join(dirs, "/")}
	}
	if m.Category == "" && defaults.category != "" {
		// no word and no shape said anything; the folder's default_category
		// fills the silence (a default, not a pin — SPEC §19.5)
		m.Category, catSrc = defaults.category, defaults.categorySrc
	}
	if m.Category == "" && docConflict.Tier != "" {
		// still nothing: say that the documents disagreed, so the
		// silence has a reason on the Plan step
		catSrc = docConflict
	}
	m.explain("category", catSrc)
	var instSrc annotations.Source
	if pinned != "" {
		// the pack's [[dir]] map pinned the instrument — curated truth
		// beats whatever the filenames appear to say
		m.Instrument, m.Family = pinned, h.lex.FamilyOf(pinned, overrides)
		instSrc = pinSrc
	} else {
		// the category is known by now, and a word that implies a
		// different one (break = loops) is a title on this file, not
		// a label — a kit called "Beat" holds kicks, not breaks
		m.Instrument, m.Family, instSrc = h.lex.ResolveInSrc(m.Category, base, labels, overrides)
		var docConflict annotations.Source
		if m.Instrument == "" && m.Family == "" && len(h.docs[p]) > 0 {
			// the file's own path named nothing, not even a family; the
			// folder of a document that points at it may (Presets/Leads/)
			v := h.docInstrument(p, m.Category, overrides)
			if v.conflict {
				docConflict = v.src
			} else if v.value != "" {
				m.Instrument, m.Family, instSrc = v.value, v.family, v.src
			}
		}
		if m.Instrument == "" && len(echoes) > 0 {
			m.Instrument, m.Family, instSrc = h.lex.ResolveInSrc(m.Category, "", echoes, overrides)
			instSrc.Echo = true
		}
		if m.Instrument == "" && defaults.instrument != "" {
			m.Instrument, m.Family = defaults.instrument, h.lex.FamilyOf(defaults.instrument, overrides)
			instSrc = defaults.instrumentSrc
		}
		if m.Instrument == "" && m.Family == "" && docConflict.Tier != "" {
			instSrc = docConflict
		}
	}
	m.explain("instrument", instSrc)
	return m, true
}

// Run harvests one location's catalog and rewrites its meta cache file.
func Run(ws *workspace.Workspace, lc workspace.LocationConfig) (*Result, error) {
	entries, err := catalog.Load(ws.CatalogPath(lc.Name))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for p, e := range entries {
		if e.Audio != nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	h, err := newHarvester(ws, lc, paths, entries)
	if err != nil {
		return nil, err
	}
	res := &Result{}
	var out []Meta
	for _, p := range paths {
		m, ok := h.one(p, entries[p])
		if !ok {
			continue
		}
		if m.BPM == 0 && m.Key == "" && m.Category == "" && m.Instrument == "" && len(m.Tags) == 0 {
			continue
		}
		res.Files++
		if m.BPM > 0 {
			res.WithBPM++
		}
		if m.Key != "" {
			res.WithKey++
		}
		if m.Category != "" {
			res.WithCategory++
		}
		if m.Instrument != "" {
			res.WithInstrument++
		}
		if len(m.Tags) > 0 {
			res.WithTags++
		}
		out = append(out, m)
	}

	if err := writeMeta(ws, lc, out); err != nil {
		return nil, err
	}
	return res, nil
}

// writeMeta replaces a location's meta cache file atomically.
func writeMeta(ws *workspace.Workspace, lc workspace.LocationConfig, out []Meta) error {
	dir := filepath.Join(ws.Root, "annotations-cache", "meta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, lc.Name+".jsonl")
	// unique temp name: a scan's harvest and an annotations-update re-harvest
	// may run concurrently for the same location, and the rename is what must
	// stay atomic, not the scratch file
	f, err := os.CreateTemp(dir, lc.Name+".jsonl.tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, m := range out {
		if err := enc.Encode(m); err != nil {
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
		os.Remove(tmp)
		return err
	}
	os.WriteFile(filepath.Join(dir, ".format"), []byte(MetaStamp()), 0o644)
	return nil
}

// Explainer answers "why did this file land there" for one location:
// it harvests a path afresh from the annotations on disk and returns
// every facet with its source. The meta cache is not consulted — this is
// what the cache will say after the next harvest, so editing an
// annotation and asking again shows the effect. The catalog is loaded
// once; each Explain reads the path's audio siblings for the multisample
// tier.
type Explainer struct {
	ws      *workspace.Workspace
	lc      workspace.LocationConfig
	entries map[string]catalog.Entry
}

// NewExplainer loads the location's catalog.
func NewExplainer(ws *workspace.Workspace, lc workspace.LocationConfig) (*Explainer, error) {
	entries, err := catalog.Load(ws.CatalogPath(lc.Name))
	if err != nil {
		return nil, err
	}
	return &Explainer{ws: ws, lc: lc, entries: entries}, nil
}

// Has reports whether the location's catalog lists p.
func (x *Explainer) Has(p string) bool {
	_, ok := x.entries[p]
	return ok
}

// Explain harvests one path and returns its record with provenance.
func (x *Explainer) Explain(p string) (Meta, error) {
	e, ok := x.entries[p]
	if !ok {
		return Meta{}, fmt.Errorf("%s: not in location %q's catalog", p, x.lc.Name)
	}
	if e.Audio == nil {
		return Meta{}, fmt.Errorf("%s: not audio, nothing is harvested for it", p)
	}
	dir := path.Dir(p)
	var siblings []string
	for q, se := range x.entries {
		if se.Audio != nil && path.Dir(q) == dir {
			siblings = append(siblings, q)
		}
	}
	h, err := newHarvester(x.ws, x.lc, siblings, x.entries)
	if err != nil {
		return Meta{}, err
	}
	m, ok := h.one(p, e)
	if !ok {
		return Meta{}, fmt.Errorf("%s: not inside a pack under the %q layout", p, x.lc.Layout)
	}
	return m, nil
}

// ExplainPrefix harvests every audio path under prefix (the folder
// itself, or a glob over the location) against the given annotations —
// nil means the layers on disk — and returns each record, empty ones
// included. It is the partial re-harvest of SPEC §19.4: a correction
// re-plans only the files it covers.
func ExplainPrefix(ws *workspace.Workspace, lc workspace.LocationConfig, entries map[string]catalog.Entry, prefix string, vendors []annotations.Vendor) (map[string]Meta, error) {
	if vendors == nil {
		v, err := annotations.Load(ws.AnnotationRoots()...)
		if err != nil {
			return nil, err
		}
		vendors = v
	}
	isGlob := strings.ContainsAny(prefix, "*?[{")
	var paths []string
	for p, e := range entries {
		if e.Audio == nil {
			continue
		}
		if isGlob {
			if ok, _ := doublestar.Match(prefix, p); ok {
				paths = append(paths, p)
			}
		} else if p == prefix || strings.HasPrefix(p, prefix+"/") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	// the multisample tier reads whole directories: include every audio
	// sibling of every covered path, then answer only for the covered ones
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[path.Dir(p)] = true
	}
	var scope []string
	for p, e := range entries {
		if e.Audio != nil && dirs[path.Dir(p)] {
			scope = append(scope, p)
		}
	}
	h := newHarvesterWith(ws, lc, scope, entries, vendors)
	out := make(map[string]Meta, len(paths))
	for _, p := range paths {
		if m, ok := h.one(p, entries[p]); ok {
			out[p] = m
		}
	}
	return out, nil
}

// Patch rewrites part of a location's meta cache: each given record
// replaces the one at its path (or is added), and a record with nothing
// harvested removes it — the same rule Run applies. The file is rewritten
// atomically, as Run writes it.
func Patch(ws *workspace.Workspace, lc workspace.LocationConfig, updates map[string]Meta) error {
	current := LoadMeta(ws, lc.Name)
	for p, m := range updates {
		if m.BPM == 0 && m.Key == "" && m.Category == "" && m.Instrument == "" && len(m.Tags) == 0 {
			delete(current, p)
			continue
		}
		current[p] = m
	}
	paths := make([]string, 0, len(current))
	for p := range current {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]Meta, 0, len(paths))
	for _, p := range paths {
		out = append(out, current[p])
	}
	return writeMeta(ws, lc, out)
}

// Explain is a one-shot Explainer for a single path.
func Explain(ws *workspace.Workspace, lc workspace.LocationConfig, p string) (Meta, error) {
	x, err := NewExplainer(ws, lc)
	if err != nil {
		return Meta{}, err
	}
	return x.Explain(p)
}

// metaFormat versions the meta cache's shape. Bump it when a record's
// meaning changes — readers treat a cache written under another format as
// absent, and MetaFresh lets callers re-run harvest before trusting it.
// "2": records carry the source path and are keyed by it, not by SHA.
// "3": records carry per-facet provenance (why).
const metaFormat = "3"

// MetaFormat is the current cache format, for tests that write a cache by hand.
const MetaFormat = metaFormat

// MetaStamp is what a harvest writes to the cache's .format: the record
// format, then the build. Tests that write a cache by hand write this.
func MetaStamp() string { return metaFormat + "\n" + version.Version + "\n" }

// MetaFresh reports whether the meta cache on disk was written by THIS
// build — same record format and same version. False means harvest must
// run again before LoadMeta's answers mean anything: the cache doesn't
// exist yet, or a different build wrote it. The version half is what makes
// a self-update honest — the document tier (v0.9.24) changed what harvest
// derives without touching the record format, and the relaunch kept
// serving the old build's classifications until someone rescanned. Harvest
// is string ops over the catalog; re-running it on every new build is
// cheaper than a user wondering whether they're looking at stale answers.
func MetaFresh(ws *workspace.Workspace) bool {
	format, build := metaStamp(ws)
	return format == metaFormat && build == version.Version
}

// MetaBuild is the build whose harvest wrote the meta cache — "" when no
// harvest has run, or the cache predates the stamp (≤ v0.9.26).
func MetaBuild(ws *workspace.Workspace) string {
	_, build := metaStamp(ws)
	return build
}

// metaStamp reads the cache's .format: line 1 the record format, line 2
// the build that wrote it (absent on caches written before the stamp).
func metaStamp(ws *workspace.Workspace) (format, build string) {
	b, err := os.ReadFile(filepath.Join(ws.Root, "annotations-cache", "meta", ".format"))
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	format = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		build = strings.TrimSpace(lines[1])
	}
	return format, build
}

// docVote is what the Live documents referencing one file agree on for
// a facet: the value (and family, for instruments), its source, or the
// fact that they contradicted each other — in which case nothing spoke
// and src says why.
type docVote struct {
	value, family string
	src           annotations.Source
	conflict      bool
}

func (v *docVote) consider(value, family string, inner annotations.Source, doc string) {
	if v.conflict {
		return
	}
	src := annotations.Source{Tier: annotations.TierDocument, Segment: inner.Segment, Word: inner.Word, Doc: doc}
	if v.value == "" {
		v.value, v.family, v.src = value, family, src
		return
	}
	if value != v.value || family != v.family {
		v.conflict = true
		v.src = annotations.Source{Tier: annotations.TierDocumentConflict,
			Segment: path.Base(v.src.Doc) + ": " + v.value + " ≠ " + path.Base(doc) + ": " + value}
		v.value, v.family = "", ""
	}
}

// docLabels is what a Live document's own folders say, read the way a
// sample's are: the format tree it lives under ("Ableton Live") names a
// format and is dropped, pack-name echoes are set aside. dirs is the
// full in-pack directory path ([[dir]] pins address it).
func (h *harvester) docLabels(docPath string) (dirs, labels []string, s site, ok bool) {
	s, ok = h.locate(docPath)
	if !ok || len(s.inPack) < 2 {
		return nil, nil, s, false
	}
	dirs = s.inPack[:len(s.inPack)-1]
	read := dirs
	if s.vendor != nil && s.vendor.IsFormatTree(s.pack, dirs[0]) {
		read = dirs[1:]
	}
	labels, _ = labelDirs(read, s.packDir, s.pack)
	return dirs, labels, s, true
}

// docCategory is the document tier for the category facet: each
// document referencing p is read through the same pack / vendor /
// lexicon rules a sample's folders get; the documents must agree.
func (h *harvester) docCategory(p string) docVote {
	var v docVote
	for _, d := range h.docs[p] {
		dirs, labels, s, ok := h.docLabels(d)
		if !ok {
			continue
		}
		c, _, _, src, _, _ := harvestCategory(dirs, labels, s.vendor, s.pack, s.packDir)
		if c == "" {
			c, src = h.cats.ResolveSrc("", labels)
		}
		if c == "" {
			continue
		}
		v.consider(c, "", src, d)
	}
	return v
}

// docInstrument is the document tier for the instrument facet. category
// is the file's category as known by now, so a word whose category
// disagrees demotes the same way it would on the file's own path.
func (h *harvester) docInstrument(p, category string, overrides []annotations.Instrument) docVote {
	var v docVote
	for _, d := range h.docs[p] {
		dirs, labels, s, ok := h.docLabels(d)
		if !ok {
			continue
		}
		_, _, pinned, _, pinSrc, _ := harvestCategory(dirs, labels, s.vendor, s.pack, s.packDir)
		var inst, fam string
		var src annotations.Source
		if pinned != "" {
			inst, fam, src = pinned, h.lex.FamilyOf(pinned, overrides), pinSrc
		} else {
			inst, fam, src = h.lex.ResolveInSrc(category, "", labels, overrides)
		}
		if inst == "" && fam == "" {
			continue
		}
		v.consider(inst, fam, src, d)
	}
	return v
}

// multisampleDirs finds the one structural signature vendors never
// label: a directory holding an instrument sampled across the keyboard,
// sibling stems differing by a note token ("…_C-2", "…_C#-2", …,
// "…_B-2"). SFM's synth packs spread these over instrument-named dirs
// ("Leads", "Bass", "05. Synth") with no category word anywhere, so the
// shape of the directory is the only signal there is. Deliberately
// conservative — at least 6 files carrying notes, 6 distinct notes, and
// noted files in the majority — so a stray "Sub C1.wav" beside twenty
// kicks claims nothing. Used as the last category tier, after every
// explicit label has had its say.
func multisampleDirs(paths []string) map[string]bool {
	type stat struct {
		total, noted int
		notes        map[string]bool
	}
	stats := map[string]*stat{}
	for _, p := range paths {
		dir := path.Dir(p)
		st := stats[dir]
		if st == nil {
			st = &stat{notes: map[string]bool{}}
			stats[dir] = st
		}
		st.total++
		base := path.Base(p)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		ms := msNoteRe.FindAllStringSubmatch(stem, -1)
		if len(ms) == 0 {
			continue
		}
		last := ms[len(ms)-1] // the pitch is the trailing token by convention
		st.noted++
		st.notes[last[1]+last[2]] = true
	}
	out := map[string]bool{}
	for dir, st := range stats {
		if st.noted >= 6 && len(st.notes) >= 6 && st.noted*2 >= st.total {
			out[dir] = true
		}
	}
	return out
}

func harvestBPM(base string, dirs []string, v *annotations.Vendor) int {
	if m := bpmTokenRe.FindStringSubmatch(base); m != nil {
		return roundBPM(m[1])
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if m := bpmTokenRe.FindStringSubmatch(dirs[i]); m != nil {
			return roundBPM(m[1])
		}
	}
	if v != nil && v.Naming.BPMDirSuffix {
		for i := len(dirs) - 1; i >= 0; i-- {
			if m := bpmDirRe.FindStringSubmatch(dirs[i]); m != nil {
				if b := roundBPM(m[2]); b > 0 {
					return b
				}
			}
		}
	}
	return 0
}

func roundBPM(s string) int {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 40 || f > 300 {
		return 0
	}
	return int(f + 0.5)
}

func harvestKey(base string, v *annotations.Vendor) string {
	if v != nil && strings.Contains(v.Naming.KeySuffix, "camelot") {
		if m := camelotRe.FindStringSubmatch(base); m != nil {
			return camelot[m[1]+strings.ToUpper(m[2])]
		}
	}
	// note+octave suffix (SFM's grammar, Loopmasters, most multisample vendors)
	if m := noteRe.FindStringSubmatch(base); m != nil {
		return strings.ToUpper(m[1][:1]) + m[1][1:] + m[2]
	}
	if m := keyWordRe.FindStringSubmatch(base); m != nil && m[2] != "" {
		note := strings.ToUpper(m[1][:1]) + m[1][1:]
		switch strings.ToLower(m[2]) {
		case "min", "m":
			return note + "m"
		default:
			return note
		}
	}
	return ""
}

// labelDirs splits a file's in-pack dirs into the ones that can describe
// a sound and the ones that only restate the pack's own name. The pack
// dir is no label — "Vocal Pop House" is a genre, "Drums That Knock" a
// brand, and neither says what any one file inside is — and a dir that
// echoes it says nothing more. Splice wraps nearly every pack in one
// (<pack>/<Label_-_Title_Audio>/<category>/…), and read as a label that
// wrapper outranks the file's own words: a kick in "Vocal Pop House"
// reads as a vocal, every one-shot in a Function Loops pack as a loop.
// Vendors' export dirs echo the same way ("Maschine/Dr Sample From Mars").
//
// Echoes are not thrown away: when nothing else on the path spoke, the
// pack's name is the only thing that did ("Silk Vocals/…/RNT_SV_01.wav"
// is a vocal), so callers consult them last. Matched as a whole phrase
// over normalized text, so "Loops", "DRUMS", "One_Shots" — the label dirs
// unwrapped Splice packs put at the same depth — stay labels.
func labelDirs(dirs []string, packDir string, p *annotations.Pack) (labels, echoes []string) {
	var names []string
	for _, n := range []string{packDir, func() string {
		if p != nil {
			return p.Name
		}
		return ""
	}()} {
		if nn := annotations.Normalize(n); nn != "" {
			names = append(names, " "+nn+" ")
		}
	}
	if len(names) == 0 {
		return dirs, nil
	}
	labels = make([]string, 0, len(dirs))
	for _, d := range dirs {
		echo := false
		if nd := annotations.Normalize(d); nd != "" {
			pad := " " + nd + " "
			for _, n := range names {
				if strings.Contains(pad, n) {
					echo = true
					break
				}
			}
		}
		if echo {
			echoes = append(echoes, d)
		} else {
			labels = append(labels, d)
		}
	}
	return labels, echoes
}

// harvestCategory resolves category + tags: the pack's [[dir]] map first
// (deepest match governs category; tags union along the prefix chain),
// then the vendor's [[category]] rules over directory names at any depth,
// then dedicated_packs. instrument is the pack [[dir]] map's instrument
// pin when the deepest matching entry carries one — "" otherwise.
// dirs is the full in-pack path ([[dir]] pins address it); labels is the
// same minus the pack-name echoes, and is what the [[category]] globs see.
// catSrc and instSrc say which entry or rule answered. defaults carries
// the deepest matching entry's default_category / default_instrument,
// which the caller applies only after every other tier stayed silent.
func harvestCategory(dirs, labels []string, v *annotations.Vendor, p *annotations.Pack, packDir string) (category string, tags []string, instrument string, catSrc, instSrc annotations.Source, defaults dirDefaults) {
	if v == nil {
		return "", nil, "", annotations.Source{}, annotations.Source{}, dirDefaults{}
	}
	seen := map[string]bool{}
	addTags := func(ts []string) {
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	rel := strings.Join(dirs, "/")
	if p != nil {
		best, bestInst := -1, -1
		bestDefCat, bestDefInst := -1, -1
		for _, d := range p.Dirs {
			dp := strings.Trim(d.Path, "/")
			match := false
			if strings.ContainsAny(dp, "*?[{") {
				match, _ = doublestar.Match(dp, rel)
				if !match {
					match, _ = doublestar.Match(dp+"/**", rel)
				}
			} else {
				match = rel == dp || strings.HasPrefix(rel, dp+"/")
			}
			if !match {
				continue
			}
			addTags(d.Tags)
			if d.Category != "" && len(dp) > best {
				best = len(dp)
				category = d.Category
				catSrc = annotations.Source{Tier: annotations.TierDir, Segment: rel, Word: d.Path}
			}
			if d.Instrument != "" && len(dp) > bestInst {
				bestInst = len(dp)
				instrument = d.Instrument
				instSrc = annotations.Source{Tier: annotations.TierDir, Segment: rel, Word: d.Path}
			}
			if d.DefaultCategory != "" && len(dp) > bestDefCat {
				bestDefCat = len(dp)
				defaults.category = d.DefaultCategory
				defaults.categorySrc = annotations.Source{Tier: annotations.TierDirDefault, Segment: rel, Word: d.Path}
			}
			if d.DefaultInstrument != "" && len(dp) > bestDefInst {
				bestDefInst = len(dp)
				defaults.instrument = d.DefaultInstrument
				defaults.instrumentSrc = annotations.Source{Tier: annotations.TierDirDefault, Segment: rel, Word: d.Path}
			}
		}
	}
	if category == "" {
		for _, c := range v.Categories {
			for _, dp := range c.DedicatedPacks {
				if ok, _ := doublestar.Match(dp, packDir); ok {
					category = c.ID
					catSrc = annotations.Source{Tier: annotations.TierDedicatedPack, Segment: packDir, Word: dp}
				}
			}
		}
	}
	if category == "" {
	outer:
		for _, c := range v.Categories {
			for _, g := range c.Match {
				for _, d := range labels {
					// case-insensitive: vendors write "One Shots", "one_shots",
					// "ONE-SHOTS" across labels and eras; the rule is one glob
					gl := strings.ToLower(g)
					name := strings.ToLower(dirOrderRe.ReplaceAllString(d, ""))
					if ok, _ := doublestar.Match(gl, name); ok {
						category = c.ID
						catSrc = annotations.Source{Tier: annotations.TierVendorCategory, Segment: d, Word: g}
						break outer
					}
					if ok, _ := doublestar.Match(gl, strings.ToLower(d)); ok {
						category = c.ID
						catSrc = annotations.Source{Tier: annotations.TierVendorCategory, Segment: d, Word: g}
						break outer
					}
				}
			}
		}
	}
	return category, tags, instrument, catSrc, instSrc, defaults
}

// dirDefaults is what a [[dir]] entry says when nothing else does.
type dirDefaults struct {
	category, instrument       string
	categorySrc, instrumentSrc annotations.Source
}

// LoadMeta reads a location's harvested metadata cache, keyed by source
// path. Missing cache is empty, not an error — harvest may not have run.
// Records without a path predate the current format and are dropped; run
// harvest again (MetaFresh says when) rather than guessing what they meant.
func LoadMeta(ws *workspace.Workspace, location string) map[string]Meta {
	out := map[string]Meta{}
	f, err := os.Open(filepath.Join(ws.Root, "annotations-cache", "meta", location+".jsonl"))
	if err != nil {
		return out
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			return out
		}
		if m.Path != "" {
			out[m.Path] = m
		}
	}
}
