package plan

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/harvest"
	"github.com/jbarket/materialized-tunes/internal/view"
	"github.com/jbarket/materialized-tunes/internal/workspace"
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
}

func newLayouter(ws *workspace.Workspace, v *view.View, lay *view.Layout, vendors []annotations.Vendor) (*layouter, error) {
	fb, _ := view.ParseLayout(UnsortedDir + "/{vendor}/{pack}/{path}")
	ly := &layouter{
		lay: lay, fallback: fb,
		locs:    map[string]workspace.LocationConfig{},
		vendors: vendors, bySlug: annotations.BySlug(vendors),
		byTop: map[string]*annotations.Vendor{},
		meta:  map[string]map[string]harvest.Meta{},
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
		m := harvest.LoadMeta(ws, inc.Location)
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

func (ly *layouter) place(loc, srcPath, sha string) placement {
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
	if m, ok := ly.meta[loc][sha]; ok {
		vals[view.TokFamily] = displayName(m.Family)
		vals[view.TokInstrument] = displayName(m.Instrument)
		vals[view.TokCategory] = displayName(m.Category)
		catchAll = m.Instrument != "" && m.Instrument == m.Family
		isFX = m.Category == "fx" || m.Family == "fx"
	}
	pl := placement{parents: inPack[:len(inPack)-1]}
	if isFX && ly.lay.NeedsMeta() {
		// FX is a function, not an instrument: a file known to be FX goes
		// in the FX tree whole — a flute riser lives with the other risers,
		// not in Woodwind/Flute/ where someone hunting a flute finds it.
		// The first taxonomy level the template uses renders as FX and the
		// deeper ones drop, so the FX tree splits by pack only.
		taxonomy := []string{view.TokFamily, view.TokInstrument, view.TokCategory}
		for _, tok := range taxonomy {
			vals[tok] = ""
		}
		for _, tok := range taxonomy {
			if ly.lay.Uses(tok) {
				vals[tok] = "FX"
				break
			}
		}
		pl.out, pl.fx = ly.lay.Render(vals), true
		return pl
	}
	if ly.lay.NeedsInstrument() && vals[view.TokInstrument] == "" {
		pl.out, pl.unsorted = ly.fallback.Render(vals), true
		return pl
	}
	if catchAll && ly.lay.Uses(view.TokFamily) && ly.lay.Uses(view.TokInstrument) {
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
