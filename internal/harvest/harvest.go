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
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
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

// Run harvests one location's catalog and rewrites its meta cache file.
func Run(ws *workspace.Workspace, lc workspace.LocationConfig) (*Result, error) {
	entries, err := catalog.Load(ws.CatalogPath(lc.Name))
	if err != nil {
		return nil, err
	}
	vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
	if err != nil {
		return nil, err
	}
	lex := annotations.LoadInstruments(filepath.Join(ws.Root, "annotations"))
	cats := annotations.LoadCategories(filepath.Join(ws.Root, "annotations"))
	res := &Result{}
	var out []Meta
	byTop := map[string]*annotations.Vendor{}
	fixed := annotations.BySlug(vendors)[lc.Vendor]
	vendorDirs := lc.Layout == "vendor-dirs"

	paths := make([]string, 0, len(entries))
	for p, e := range entries {
		if e.Audio != nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	msDirs := multisampleDirs(paths)
	for _, p := range paths {
		e := entries[p]
		segs := strings.Split(p, "/")
		// vendor + pack for this path
		vendor := fixed
		packIdx := 0
		if vendorDirs {
			if len(segs) < 3 {
				continue
			}
			packIdx = 1
			v, seen := byTop[segs[0]]
			if !seen {
				v = annotations.ByName(vendors, segs[0])
				byTop[segs[0]] = v
			}
			vendor = v
		} else if len(segs) < 2 {
			continue
		}
		var pack *annotations.Pack
		if vendor != nil {
			pack = vendor.PackByDir(segs[packIdx])
		}
		inPack := segs[packIdx+1:] // path within the pack, last = filename
		m := Meta{Path: p, SHA: e.SHA256}
		base := strings.TrimSuffix(inPack[len(inPack)-1], filepath.Ext(inPack[len(inPack)-1]))
		dirs := inPack[:len(inPack)-1]

		m.BPM = harvestBPM(base, dirs, vendor)
		m.Key = harvestKey(base, vendor)
		var pinned string
		m.Category, m.Tags, pinned = harvestCategory(dirs, vendor, pack, segs[packIdx])
		if m.Category == "" {
			// vendor annotation said nothing (or there is none) — the shared
			// lexicon reads the same folder/filename grammar cross-vendor
			m.Category = cats.Resolve(base, dirs)
		}
		if m.Category == "" && msDirs[path.Dir(p)] {
			// no label anywhere claimed the file, but its directory has the
			// multisample shape — chromatic note-suffixed siblings
			m.Category = "multisamples"
		}
		var vendorInst []annotations.Instrument
		if vendor != nil {
			vendorInst = vendor.Instruments
		}
		if pinned != "" {
			// the pack's [[dir]] map pinned the instrument — curated truth
			// beats whatever the filenames appear to say
			m.Instrument, m.Family = pinned, lex.FamilyOf(pinned, vendorInst)
		} else {
			m.Instrument, m.Family = lex.Resolve(base, dirs, vendorInst)
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

	dir := filepath.Join(ws.Root, "annotations-cache", "meta")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, lc.Name+".jsonl")
	// unique temp name: a scan's harvest and an annotations-update re-harvest
	// may run concurrently for the same location, and the rename is what must
	// stay atomic, not the scratch file
	f, err := os.CreateTemp(dir, lc.Name+".jsonl.tmp*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, m := range out {
		if err := enc.Encode(m); err != nil {
			f.Close()
			os.Remove(tmp)
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	os.WriteFile(filepath.Join(dir, ".format"), []byte(metaFormat+"\n"), 0o644)
	return res, nil
}

// metaFormat versions the meta cache's shape. Bump it when a record's
// meaning changes — readers treat a cache written under another format as
// absent, and MetaFresh lets callers re-run harvest before trusting it.
// "2": records carry the source path and are keyed by it, not by SHA.
const metaFormat = "2"

// MetaFresh reports whether the meta cache on disk was written by this
// build's format. False means harvest must run again before LoadMeta's
// answers mean anything — including the cache simply not existing yet.
func MetaFresh(ws *workspace.Workspace) bool {
	b, err := os.ReadFile(filepath.Join(ws.Root, "annotations-cache", "meta", ".format"))
	return err == nil && strings.TrimSpace(string(b)) == metaFormat
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

// harvestCategory resolves category + tags: the pack's [[dir]] map first
// (deepest match governs category; tags union along the prefix chain),
// then the vendor's [[category]] rules over directory names at any depth,
// then dedicated_packs. instrument is the pack [[dir]] map's instrument
// pin when the deepest matching entry carries one — "" otherwise.
func harvestCategory(dirs []string, v *annotations.Vendor, p *annotations.Pack, packDir string) (category string, tags []string, instrument string) {
	if v == nil {
		return "", nil, ""
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
			}
			if d.Instrument != "" && len(dp) > bestInst {
				bestInst = len(dp)
				instrument = d.Instrument
			}
		}
	}
	if category == "" {
		for _, c := range v.Categories {
			for _, dp := range c.DedicatedPacks {
				if ok, _ := doublestar.Match(dp, packDir); ok {
					category = c.ID
				}
			}
		}
	}
	if category == "" {
	outer:
		for _, c := range v.Categories {
			for _, g := range c.Match {
				for _, d := range dirs {
					// case-insensitive: vendors write "One Shots", "one_shots",
					// "ONE-SHOTS" across labels and eras; the rule is one glob
					gl := strings.ToLower(g)
					name := strings.ToLower(dirOrderRe.ReplaceAllString(d, ""))
					if ok, _ := doublestar.Match(gl, name); ok {
						category = c.ID
						break outer
					}
					if ok, _ := doublestar.Match(gl, strings.ToLower(d)); ok {
						category = c.ID
						break outer
					}
				}
			}
		}
	}
	return category, tags, instrument
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
