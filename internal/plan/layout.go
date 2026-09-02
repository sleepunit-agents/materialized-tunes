package plan

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/view"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// UnsortedDir is where a templated layout puts files it cannot place —
// no instrument label in the vendor's naming, so {family}/{instrument}
// has no value. Underneath, the mirror tree (vendor/pack/path), so
// nothing is lost and the folder is easy to find on the device.
const UnsortedDir = "_Unsorted"

// GeneralDir is the {instrument} folder for files whose label only goes
// as deep as the family — the lexicon's catch-all won (instrument id ==
// family id), and rendering both levels would double the name
// ("Drums/Drums", "Woodwind/Woodwind"). An explicit bucket keeps the
// instrument level uniform and the gap visible.
const GeneralDir = "_General"

// layouter resolves a template's tokens for one selected file: vendor and
// pack from the location layout (flat or vendor-dirs), family / instrument
// / category from the harvest cache (SHA-keyed), path and file from the
// source path after format-tree stripping.
type layouter struct {
	lay      *view.Layout
	fallback *view.Layout
	locs     map[string]workspace.LocationConfig
	vendors  []annotations.Vendor
	bySlug   map[string]*annotations.Vendor
	byTop    map[string]*annotations.Vendor // vendor-dirs: top dir → vendor (nil = unknown)
	meta     map[string]map[string]harvest.Meta
	lex      *annotations.Lexicon // flat-family knowledge; nil when the template reads no metadata

	in     *Inputs
	docIdx map[string]*ableton.Resolver // location → every audio path in its catalog; built on first document
}

func newLayouter(in *Inputs, v *view.View, lay *view.Layout, vendors []annotations.Vendor) (*layouter, error) {
	ws := in.ws
	fb, _ := view.ParseLayout(UnsortedDir + "/{vendor}/{pack}/{path}")
	ly := &layouter{
		lay: lay, fallback: fb, in: in, docIdx: map[string]*ableton.Resolver{},
		locs:    map[string]workspace.LocationConfig{},
		vendors: vendors, bySlug: annotations.BySlug(vendors),
		byTop: map[string]*annotations.Vendor{},
		meta:  map[string]map[string]harvest.Meta{},
	}
	if lay.NeedsMeta() {
		ly.lex = in.Lexicon()
	}
	for _, inc := range v.Include {
		if _, done := ly.locs[inc.Location]; done {
			continue
		}
		lc, _ := ws.Location(inc.Location)
		ly.locs[inc.Location] = lc
		if !lay.NeedsMeta() {
			continue
		}
		m := in.Meta(inc.Location)
		if len(m) == 0 {
			return nil, fmt.Errorf("layout %q needs harvested metadata, and location %q has none — run `mtunes catalog harvest %s` (a scan does it too)",
				lay.Template, inc.Location, inc.Location)
		}
		ly.meta[inc.Location] = m
	}
	return ly, nil
}

// placement is where one file goes under the template, plus what the
// {file} collision pass may prepend to keep names apart.
type placement struct {
	out           string
	parents       []string // intra-pack dirs, outermost first
	unsorted      bool
	uncategorized bool // placed, but {category} fell back to _Unsorted
	general       bool // placed, but {instrument} is the family catch-all — rendered as _General
	fx            bool // known FX — consolidated under FX/ regardless of instrument
}

// place decides where one file lands. srcPath is the path the output
// tokens read (a format-tree file arrives with its tree segment already
// stripped); catPath is the file's real catalog path, which is what the
// harvested metadata is keyed by.
func (ly *layouter) place(loc, srcPath, catPath string) placement {
	m, ok := ly.meta[loc][catPath]
	return ly.placeMeta(loc, srcPath, m, ok)
}

// docMeta is what a companion document inherits from the samples it
// references: a rack is what its pads are. Every ref is resolved against
// the location's whole catalog (not the selection — placement is a fact
// about the document, not about this recipe), and the referenced files'
// harvested facts are put to a vote. Family is the plurality; instrument
// and category must be near-unanimous (two thirds) to stand, else the
// level says so — instrument falls to the family's catch-all (_General)
// and category to _Unsorted — because a kit that spans kick, snare and
// hat is a drums thing, not a hat thing. Returns the inherited record and
// how many refs resolved out of how many the document carries.
func (ly *layouter) docMeta(loc string, ce catalog.Entry) (m harvest.Meta, resolved, total int) {
	if ce.Doc == nil || len(ce.Doc.Refs) == 0 || ly.in == nil {
		return m, 0, 0
	}
	total = len(ce.Doc.Refs)
	rs, ok := ly.docIdx[loc]
	if !ok {
		var paths []string
		if cat, err := ly.in.Catalog(loc); err == nil {
			for p, e := range cat {
				if e.Audio != nil {
					paths = append(paths, p)
				}
			}
		}
		sort.Strings(paths)
		rs = ableton.NewResolver(paths)
		ly.docIdx[loc] = rs
	}
	var metas []harvest.Meta
	for _, r := range ce.Doc.Refs {
		src, ok := rs.Resolve(ce.Path, r)
		if !ok {
			continue
		}
		resolved++
		if sm, ok := ly.meta[loc][src]; ok {
			metas = append(metas, sm)
		}
	}
	return inherit(metas), resolved, total
}

// inherit votes one record out of many. Plurality ties break by name so
// the answer pins.
func inherit(metas []harvest.Meta) harvest.Meta {
	var m harvest.Meta
	if len(metas) == 0 {
		return m
	}
	top := func(vals []string) (string, int) {
		n := map[string]int{}
		for _, v := range vals {
			if v != "" {
				n[v]++
			}
		}
		best, bestN := "", 0
		for v, c := range n {
			if c > bestN || (c == bestN && v < best) {
				best, bestN = v, c
			}
		}
		return best, bestN
	}
	var fams []string
	for _, sm := range metas {
		fams = append(fams, sm.Family)
	}
	m.Family, _ = top(fams)
	var insts, cats []string
	for _, sm := range metas {
		if sm.Family != m.Family {
			continue
		}
		insts = append(insts, sm.Instrument)
		cats = append(cats, sm.Category)
	}
	if inst, n := top(insts); inst != "" && n*3 >= len(insts)*2 {
		m.Instrument = inst
	} else if m.Family != "" {
		m.Instrument = m.Family // the catch-all: labeled only as deep as the family
	}
	if cat, n := top(cats); cat != "" && n*3 >= len(cats)*2 {
		m.Category = cat
	}
	return m
}

// placeMeta is place with the record given rather than looked up: a
// companion document brings the record it inherited from its samples.
func (ly *layouter) placeMeta(loc, srcPath string, meta harvest.Meta, hasMeta bool) placement {
	lc := ly.locs[loc]
	segs := strings.Split(srcPath, "/")
	vals := map[string]string{}
	var inPack []string
	if lc.Layout == "vendor-dirs" {
		switch {
		case len(segs) >= 3:
			vals[view.TokVendor], vals[view.TokPack], inPack = segs[0], segs[1], segs[2:]
		case len(segs) == 2:
			vals[view.TokVendor], inPack = segs[0], segs[1:]
		default:
			inPack = segs
		}
	} else {
		vals[view.TokVendor] = lc.Name
		if vd := ly.bySlug[lc.Vendor]; vd != nil && vd.Name != "" {
			vals[view.TokVendor] = vd.Name
		}
		if len(segs) >= 2 {
			vals[view.TokPack], inPack = segs[0], segs[1:]
		} else {
			inPack = segs
		}
	}
	vals[view.TokPath] = strings.Join(inPack, "/")
	vals[view.TokFile] = inPack[len(inPack)-1]
	catchAll, isFX := false, false
	if hasMeta {
		m := meta
		vals[view.TokFamily] = displayName(m.Family)
		vals[view.TokInstrument] = displayName(m.Instrument)
		if d := ly.lex.DisplayName(m.Instrument); d != "" {
			vals[view.TokInstrument] = d
		}
		vals[view.TokCategory] = displayName(m.Category)
		catchAll = m.Instrument != "" && m.Instrument == m.Family
		isFX = m.Family == "fx"
	}
	pl := placement{parents: inPack[:len(inPack)-1]}
	if isFX && ly.lay.NeedsMeta() {
		// FX is a function, not an instrument: a file known to be FX goes
		// in the FX tree, not in Woodwind/Flute/ where someone hunting a
		// flute finds it. Inside, the same flat-family rule as everywhere
		// else decides the split: fx marked flat in instruments.toml means
		// just loop vs one-shot vs multisample; otherwise {instrument} is
		// what the sound is when the label says (Riser, Foley, Flute;
		// _General when it only said "fx"). {category} is the kind of
		// recording either way, read exactly as for any other family —
		// "fx" stopped being a category on 2026-09-02 (it said what, not
		// how, and as a non-empty category it kept the multisample shape
		// from speaking on SFM's WAV/FX patches), so an FX file with no
		// kind is _Unsorted for the same reason a flute with none is.
		if ly.lay.Uses(view.TokFamily) {
			vals[view.TokFamily] = "FX"
			switch {
			case ly.lex.FlatFamily("fx"):
				vals[view.TokInstrument] = ""
			case meta.Instrument == "" || meta.Instrument == "fx":
				vals[view.TokInstrument] = GeneralDir
				pl.general = ly.lay.Uses(view.TokInstrument)
			}
			if meta.Category == "" {
				vals[view.TokCategory] = UnsortedDir
				pl.uncategorized = ly.lay.Uses(view.TokCategory)
			}
		} else {
			// No {family} level to consolidate under: the first taxonomy
			// token the template does use renders as FX, deeper ones drop.
			taxonomy := []string{view.TokInstrument, view.TokCategory}
			for _, tok := range taxonomy {
				vals[tok] = ""
			}
			for _, tok := range taxonomy {
				if ly.lay.Uses(tok) {
					vals[tok] = "FX"
					break
				}
			}
		}
		pl.out, pl.fx = ly.lay.Render(vals), true
		return pl
	}
	if ly.lay.NeedsInstrument() && vals[view.TokInstrument] == "" {
		pl.out, pl.unsorted = ly.fallback.Render(vals), true
		return pl
	}
	if ly.lex.FlatFamily(meta.Family) && !ly.lex.SplitsFlat(meta.Instrument) {
		// A flat family doesn't split by instrument: bass is bass, and the
		// sub/reese/wub taxonomy in vendor naming isn't reliable enough to
		// fight samples over. The {instrument} level drops; a template
		// with only {instrument} renders the family name there instead.
		//
		// One exception, marked per entry in the lexicon: an instrument the
		// vendor names outright rather than in jargon keeps its folder even
		// here (upright bass among the 808s). Everything else in the family
		// still goes flat around it.
		if ly.lay.Uses(view.TokFamily) {
			vals[view.TokInstrument] = ""
		} else {
			vals[view.TokInstrument] = displayName(meta.Family)
		}
	} else if catchAll && ly.lay.Uses(view.TokFamily) && ly.lay.Uses(view.TokInstrument) {
		// The label only goes as deep as the family; rendering it at both
		// levels doubles the folder name ("Drums/Drums").
		vals[view.TokInstrument] = GeneralDir
		pl.general = true
	}
	if ly.lay.Uses(view.TokCategory) && vals[view.TokCategory] == "" {
		// Don't silently drop the level: that puts pack folders beside the
		// category folders ("Vintage Breaks Vol 4/" next to "Loops/"). An
		// explicit bucket keeps the level uniform and the gap visible.
		vals[view.TokCategory] = UnsortedDir
		pl.uncategorized = true
	}
	pl.out = ly.lay.Render(vals)
	return pl
}

// displayName turns a harvest id into a folder name: "one-shots" →
// "One-Shots", "kick" → "Kick", "fx" → "FX".
func displayName(id string) string {
	if id == "" {
		return ""
	}
	if id == "fx" {
		return "FX"
	}
	words := strings.Split(id, "-")
	for i, w := range words {
		if w == "fx" {
			words[i] = "FX"
			continue
		}
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, "-")
}

// uniquify keeps file names apart within each output directory: names
// that collide (case-folded unless the device cares) get just enough of
// their parent dirs prepended ("KitA - Kick 01.wav"), one level at a
// time, only for the names that need it. dirs[i] is the directory the
// name lives in (kept as-is), parents[i] the candidate dirs to prepend,
// outermost first. Deterministic, so it pins in lockfiles. Names still
// colliding afterwards are left for checkCollisions to error on.
func uniquify(dirs, names []string, parents [][]string, caseSensitive bool) []string {
	out := append([]string{}, names...)
	depth := make([]int, len(names))
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	for range 64 { // bounded by deepest realistic tree
		groups := map[string][]int{}
		for i := range out {
			k := dirs[i] + "\x00" + fold(out[i])
			groups[k] = append(groups[k], i)
		}
		progressed := false
		for _, idxs := range groups {
			if len(idxs) < 2 {
				continue
			}
			for _, i := range idxs {
				if depth[i] < len(parents[i]) {
					depth[i]++
					ps := parents[i][len(parents[i])-depth[i]:]
					out[i] = strings.Join(ps, " - ") + " - " + names[i]
					progressed = true
				}
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

// disambiguate runs uniquify over entries placed with {file}: two files
// from one pack that shared a name across intra-pack folders now share a
// folder too, and the folder they came from is what tells them apart.
func disambiguate(entries []Entry, caseSensitive bool) {
	dirs := make([]string, len(entries))
	names := make([]string, len(entries))
	parents := make([][]string, len(entries))
	for i, e := range entries {
		dirs[i], names[i] = path.Split(e.OutPath)
		parents[i] = e.parents
	}
	for i, n := range uniquify(dirs, names, parents, caseSensitive) {
		entries[i].OutPath = dirs[i] + n
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].OutPath < entries[j].OutPath })
}
