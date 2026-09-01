// Package annotations reads vendor annotation data — the community-shaped
// facts about how sample vendors structure their libraries (pack grammar,
// format trees, category folder variants, content identity). The data
// lives in a checkout of sample-vendor-annotations at
// <workspace>/annotations; mtunes only ever reads it.
package annotations

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bmatcuk/doublestar/v4"
)

type Vendor struct {
	Slug     string   `toml:"slug" json:"slug"`
	Name     string   `toml:"name" json:"name"`
	Aliases  []string `toml:"aliases" json:"aliases,omitempty"`
	Homepage string   `toml:"homepage" json:"homepage,omitempty"`

	Grammar string `json:"grammar,omitempty"` // packs.grammar, e.g. "top-level-dirs"

	// Resolver: for marketplaces (Splice) whose pack list is unbounded, the
	// repo ships no per-pack files; consumers resolve a pack's identity from
	// the vendor's public API on demand and cache locally. Named strategy,
	// e.g. "splice-graphql". "" = packs are annotated in the repo.
	Resolver string `json:"resolver,omitempty"`

	// Formats: which per-pack dir holds the canonical audio and which are
	// parallel exports (Ableton/Kontakt/16-bit cuts). Globs over the dir
	// name directly under the pack dir. "" / "." = audio sits at pack root.
	CanonicalDir string   `json:"canonical_dir,omitempty"`
	ParallelDirs []string `json:"parallel_dirs,omitempty"`

	// ParallelRole says what the parallel trees hold. "cut" (the default)
	// is Polyend's case: the same rendered file at another bit depth or
	// channel count, so every cut of a sample is the same length. Vendors
	// that re-export the whole library per sampler — Samples From Mars
	// ships Battery, Maschine, Kontakt, MPC trees of the same hits —
	// declare "reexport": same recordings, re-rendered, so the trims
	// drift by a few frames and length can no longer be the proof that
	// two files are the same sample.
	ParallelRole string `json:"parallel_role,omitempty"`

	// Naming: filename grammar facts (see SCHEMA [naming]). Consumers use
	// them to harvest per-file metadata (key, bpm) and to rename safely.
	Naming Naming `json:"naming,omitempty"`

	// Install: where this vendor's library lives by default, per OS.
	// A fact about the vendor, same as its pack grammar — used to offer
	// "you have Splice installed, add it?" without scanning the disk.
	InstallMac   []string `json:"install_macos,omitempty"`
	InstallLinux []string `json:"install_linux,omitempty"`
	InstallWin   []string `json:"install_windows,omitempty"`
	InstallNote  string   `json:"install_note,omitempty"`

	Instruments []Instrument `json:"instruments,omitempty"` // vendor-local overrides, consulted before the shared lexicon
	Categories  []Category   `json:"categories,omitempty"`
	Packs       []Pack       `json:"packs,omitempty"`

	dir string // vendor directory on disk, for manifest resolution
}

// Naming mirrors SCHEMA [naming]: free-form grammar strings, plus the two
// flags consumers actually branch on.
type Naming struct {
	DirOrderPrefix string `toml:"dir_order_prefix" json:"dir_order_prefix,omitempty"`
	NoteSuffix     string `toml:"note_suffix" json:"note_suffix,omitempty"` // e.g. "_<note><octave>"
	KeySuffix      string `toml:"key_suffix" json:"key_suffix,omitempty"`   // e.g. " - <camelot>"
	TakeSuffix     string `toml:"take_suffix" json:"take_suffix,omitempty"`
	BPMDirSuffix   bool   `toml:"bpm_dir_suffix" json:"bpm_dir_suffix,omitempty"` // loop dirs end in their BPM ("Bass Lines 166.5")
}

type Category struct {
	ID             string   `toml:"id" json:"id"`
	Match          []string `toml:"match" json:"match,omitempty"`
	DedicatedPacks []string `toml:"dedicated_packs" json:"dedicated_packs,omitempty"`
}

type Pack struct {
	Name          string   `toml:"name" json:"name"`
	Slug          string   `toml:"slug" json:"slug"`
	Dir           string   `toml:"dir" json:"dir"`
	URL           string   `toml:"url" json:"url,omitempty"`
	Provider      string   `toml:"provider" json:"provider,omitempty"`             // distributor vendors: the label the pack is BY
	SamplesListed int      `toml:"samples_listed" json:"samples_listed,omitempty"` // vendor's own count; honest denominator for partial copies
	Tags          []string `toml:"tags" json:"tags,omitempty"`                     // canonical tags (see tags.toml in the annotations repo)
	Archives      []string `toml:"archives" json:"archives,omitempty"`
	Discontinued  bool     `toml:"discontinued" json:"discontinued,omitempty"` // out of print; [pack] url/[meta] image are archival pointers

	Meta        Meta         `json:"meta,omitempty"`
	Identity    Identity     `json:"identity,omitempty"`
	Acquisition *Acquisition `json:"acquisition,omitempty"`
	Relations   []Relation   `json:"relations,omitempty"`
	Dirs        []Dir        `json:"dirs,omitempty"`

	// Instruments are the pack's own [[instrument]] blocks — what a word
	// means inside THIS pack when it means something else everywhere
	// else. Drumtrax From Mars calls its kick "Bass" ("Bass Drumtrax 08",
	// under a Bass folder, and again inside the Kits copies), and no dir
	// pin reaches a filename. Consulted before the vendor's blocks, which
	// are consulted before the shared lexicon.
	Instruments []Instrument `json:"instruments,omitempty"`
}

// Acquisition mirrors SCHEMA [acquisition] — where someone who doesn't own
// the pack may go. The pointer is always a page, never a file (SPEC §11.6);
// orphans carry no pointer at all.
type Acquisition struct {
	Class   string `toml:"class" json:"class"`               // vendor-free | vendor-paid | distributor | orphan
	URL     string `toml:"url" json:"url,omitempty"`         // the page the vendor wants the customer on
	Via     string `toml:"via" json:"via,omitempty"`         // distributor vendor slug, iff class = "distributor"
	Gate    string `toml:"gate" json:"gate,omitempty"`       // none | email | account | purchase
	License string `toml:"license" json:"license,omitempty"` // ceiling on claims — never upgrade on display
}

// Relation mirrors SCHEMA [[relation]] — subsets, samplers, bundles.
type Relation struct {
	Type   string `toml:"type" json:"type"`             // subset-of | sampler-of | superseded-by | bundle-of | reissue-of
	Pack   string `toml:"pack" json:"pack"`             // "<vendor slug>/<pack slug>"
	Basis  string `toml:"basis" json:"basis,omitempty"` // sha | vendor-states | observed
	Source string `toml:"source" json:"source,omitempty"`
	Note   string `toml:"note" json:"note,omitempty"`
}

type Meta struct {
	Title       string `toml:"title" json:"title,omitempty"`
	Type        string `toml:"type" json:"type,omitempty"`
	Image       string `toml:"image" json:"image,omitempty"`
	Description string `toml:"description" json:"description,omitempty"`
}

type Identity struct {
	Algo       string   `toml:"algo" json:"algo,omitempty"`
	AudioFiles int      `toml:"audio_files" json:"audio_files,omitempty"`
	AudioBytes int64    `toml:"audio_bytes" json:"audio_bytes,omitempty"`
	Digest     string   `toml:"digest" json:"digest,omitempty"`
	Anchors    []string `toml:"anchors" json:"anchors,omitempty"`
	Manifest   string   `toml:"manifest" json:"manifest,omitempty"`
}

type Dir struct {
	Path     string   `toml:"path" json:"path"`
	Role     string   `toml:"role" json:"role,omitempty"`
	Category string   `toml:"category" json:"category,omitempty"`
	Tags     []string `toml:"tags" json:"tags,omitempty"`
	Desc     string   `toml:"desc" json:"desc,omitempty"`

	// Instrument pins every file under this dir to one lexicon id,
	// overriding whatever the filenames appear to say. For content whose
	// names carry no honest signal — jungle breaks named after their
	// sources ("Sub-Urban", "Clint Eastwood") read as anything but drums.
	Instrument string `toml:"instrument" json:"instrument,omitempty"`

	// DefaultCategory / DefaultInstrument speak last: they answer only
	// for a file no word on the path (and no directory shape) said
	// anything about. Where category / instrument are pins that beat the
	// filenames, these fill silence — a synth pack's Leads folder holding
	// one labelled kick loop keeps the loop a loop. (SPEC §19.5.)
	DefaultCategory   string `toml:"default_category" json:"default_category,omitempty"`
	DefaultInstrument string `toml:"default_instrument" json:"default_instrument,omitempty"`

	Provenance
}

// Provenance is what an entry asserted from a real copy carries: when it
// was seen and by what evidence. Local marks an entry the user keeps out
// of any export — their own opinion, never the repo's; upstream lint
// rejects it, so it cannot leak. Shared by [[dir]] and [[instrument]].
type Provenance struct {
	Observed string `toml:"observed" json:"observed,omitempty"` // YYYY-MM-DD
	Note     string `toml:"note" json:"note,omitempty"`
	Local    bool   `toml:"local" json:"local,omitempty"`
}

// Load reads every vendor under each root and merges them in order:
// a later root is the more local layer and its entries are consulted
// first — its [[dir]] entries, [[instrument]] and [[category]] blocks
// are prepended to the earlier root's for the same vendor or pack, its
// packs union in, and a vendor no earlier root knows is added whole.
// The usual call is (repo checkout, <workspace>/annotations.local): the
// local layer is a partial tree in the repo's own layout holding only
// what the user asserted (SPEC §19.5). No precedence rule is added by
// merging — pins are already deepest-match with the first entry winning
// a tie, and override blocks are already first-hit — so a local entry
// at the same or deeper path simply wins.
//
// A root may be a checkout of the annotations repo (vendors/<slug>/...)
// or a bare directory of vendor dirs. A missing root is not an error —
// annotations are optional.
func Load(roots ...string) ([]Vendor, error) {
	var merged []Vendor
	for _, root := range roots {
		layer, err := loadRoot(root)
		if err != nil {
			return nil, err
		}
		merged = mergeLayer(merged, layer)
	}
	return merged, nil
}

// mergeLayer lays over onto base: over's entries come first wherever
// the two describe the same vendor or pack; scalar facts about a vendor
// or pack stay base's unless base never said.
func mergeLayer(base, over []Vendor) []Vendor {
	if len(base) == 0 {
		return over
	}
	idx := map[string]int{}
	for i, v := range base {
		idx[v.Slug] = i
		idx[filepath.Base(v.dir)] = i // a local dir with no vendor.toml is named for the slug
	}
	for _, ov := range over {
		i, ok := idx[ov.Slug]
		if !ok {
			i, ok = idx[filepath.Base(ov.dir)]
		}
		if !ok {
			base = append(base, ov)
			idx[ov.Slug] = len(base) - 1
			continue
		}
		v := &base[i]
		v.Instruments = append(append([]Instrument(nil), ov.Instruments...), v.Instruments...)
		v.Categories = append(append([]Category(nil), ov.Categories...), v.Categories...)
		for _, op := range ov.Packs {
			bp := v.packBySlugOrDir(op.Slug, op.Dir)
			if bp == nil {
				v.Packs = append(v.Packs, op)
				continue
			}
			bp.Dirs = append(append([]Dir(nil), op.Dirs...), bp.Dirs...)
			bp.Instruments = append(append([]Instrument(nil), op.Instruments...), bp.Instruments...)
			if bp.Name == "" {
				bp.Name = op.Name
			}
			if bp.Dir == "" {
				bp.Dir = op.Dir
			}
		}
		if v.Name == "" {
			v.Name = ov.Name
		}
	}
	return base
}

func (v *Vendor) packBySlugOrDir(slug, dir string) *Pack {
	for i := range v.Packs {
		p := &v.Packs[i]
		if (slug != "" && p.Slug == slug) || (dir != "" && p.Dir == dir) {
			return p
		}
	}
	return nil
}

func loadRoot(root string) ([]Vendor, error) {
	base := root
	if _, err := os.Stat(filepath.Join(root, "vendors")); err == nil {
		base = filepath.Join(root, "vendors")
	}
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var vendors []Vendor
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		vdir := filepath.Join(base, e.Name())
		v, err := loadVendor(vdir)
		if err != nil {
			return nil, err
		}
		if v != nil {
			vendors = append(vendors, *v)
		}
	}
	return vendors, nil
}

// BySlug indexes a Load result.
func BySlug(vendors []Vendor) map[string]*Vendor {
	m := make(map[string]*Vendor, len(vendors))
	for i := range vendors {
		m[vendors[i].Slug] = &vendors[i]
	}
	return m
}

// ByName finds a vendor by the name a human would put on a folder: slug,
// display name, or any alias, case-insensitively. Used by the vendor-dirs
// location layout, where the top-level directory names the vendor.
func ByName(vendors []Vendor, name string) *Vendor {
	for i := range vendors {
		v := &vendors[i]
		if strings.EqualFold(v.Slug, name) || strings.EqualFold(v.Name, name) {
			return v
		}
		for _, a := range v.Aliases {
			if strings.EqualFold(a, name) {
				return v
			}
		}
	}
	return nil
}

func loadVendor(dir string) (*Vendor, error) {
	var vf struct {
		Vendor struct {
			Name     string   `toml:"name"`
			Slug     string   `toml:"slug"`
			Aliases  []string `toml:"aliases"`
			Homepage string   `toml:"homepage"`
		} `toml:"vendor"`
		Packs struct {
			Grammar  string `toml:"grammar"`
			Resolver string `toml:"resolver"`
		} `toml:"packs"`
		Formats struct {
			CanonicalDir string   `toml:"canonical_dir"`
			ParallelDirs []string `toml:"parallel_dirs"`
			ParallelRole string   `toml:"parallel_role"`
		} `toml:"formats"`
		Naming  Naming `toml:"naming"`
		Install struct {
			Macos   []string `toml:"macos"`
			Linux   []string `toml:"linux"`
			Windows []string `toml:"windows"`
			Note    string   `toml:"note"`
		} `toml:"install"`
		Category   []Category   `toml:"category"`
		Instrument []Instrument `toml:"instrument"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "vendor.toml"))
	if os.IsNotExist(err) {
		// a local layer may hold only packs/ for a vendor the checkout
		// already describes — the dir name is the slug then
		if st, serr := os.Stat(filepath.Join(dir, "packs")); serr != nil || !st.IsDir() {
			return nil, nil // not a vendor dir; skip
		}
		data = nil
	} else if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &vf); err != nil {
		return nil, err
	}
	v := &Vendor{
		Slug: vf.Vendor.Slug, Name: vf.Vendor.Name,
		Aliases: vf.Vendor.Aliases, Homepage: vf.Vendor.Homepage,
		Grammar: vf.Packs.Grammar, Resolver: vf.Packs.Resolver, Categories: vf.Category,
		Instruments:  vf.Instrument,
		CanonicalDir: vf.Formats.CanonicalDir, ParallelDirs: vf.Formats.ParallelDirs,
		ParallelRole: vf.Formats.ParallelRole,
		Naming:       vf.Naming,
		InstallMac:   vf.Install.Macos, InstallLinux: vf.Install.Linux,
		InstallWin: vf.Install.Windows, InstallNote: vf.Install.Note,
		dir: dir,
	}
	if v.Slug == "" {
		v.Slug = filepath.Base(dir)
	}
	for i := range v.Instruments {
		v.Instruments[i].Scope = "vendor"
	}

	packFiles, _ := filepath.Glob(filepath.Join(dir, "packs", "*.toml"))
	for _, pf := range packFiles {
		var f struct {
			Pack struct {
				Name          string   `toml:"name"`
				Slug          string   `toml:"slug"`
				Dir           string   `toml:"dir"`
				URL           string   `toml:"url"`
				Provider      string   `toml:"provider"`
				SamplesListed int      `toml:"samples_listed"`
				Tags          []string `toml:"tags"`
				Archives      []string `toml:"archives"`
				Discontinued  bool     `toml:"discontinued"`
			} `toml:"pack"`
			Meta        Meta         `toml:"meta"`
			Identity    Identity     `toml:"identity"`
			Acquisition *Acquisition `toml:"acquisition"`
			Relation    []Relation   `toml:"relation"`
			Dir         []Dir        `toml:"dir"`
			Instrument  []Instrument `toml:"instrument"`
		}
		data, err := os.ReadFile(pf)
		if err != nil {
			return nil, err
		}
		if err := toml.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		for i := range f.Instrument {
			f.Instrument[i].Scope = "pack"
		}
		v.Packs = append(v.Packs, Pack{
			Name: f.Pack.Name, Slug: f.Pack.Slug, Dir: f.Pack.Dir,
			URL: f.Pack.URL, Provider: f.Pack.Provider, SamplesListed: f.Pack.SamplesListed,
			Tags: f.Pack.Tags, Archives: f.Pack.Archives, Discontinued: f.Pack.Discontinued,
			Meta: f.Meta, Identity: f.Identity,
			Acquisition: f.Acquisition, Relations: f.Relation, Dirs: f.Dir,
			Instruments: f.Instrument,
		})
	}
	return v, nil
}

// IsFormatTree reports whether dir (a directory name directly under a pack
// dir) is a format-tree level — the vendor's canonical audio dir ("WAV",
// "* 24 bit stereo") or one of its parallel exports — i.e. a level that
// carries no musical information and can be dropped from output paths.
// A pack's own [[dir]] map wins where it speaks: an explicit
// role = "format-tree" is one; any other role on that exact dir (a
// category dir at pack root that happens to be called "FX") is not. A
// "." canonical dir means audio sits at pack root and nothing is a tree.
func (v *Vendor) IsFormatTree(p *Pack, dir string) bool {
	if role, claimed := PackDirRole(p, dir); claimed {
		if role == "format-tree" {
			return true
		}
		// canonical-audio on a single segment is only a tree when the
		// vendor has a format level at all and this dir IS that level
		if role == "canonical-audio" && v.CanonicalDir != "" && v.CanonicalDir != "." {
			ok, _ := doublestar.Match(v.CanonicalDir, dir)
			return ok
		}
		return false
	}
	if v.CanonicalDir != "" && v.CanonicalDir != "." {
		if ok, _ := doublestar.Match(v.CanonicalDir, dir); ok {
			return true
		}
	}
	for _, g := range v.ParallelDirs {
		if ok, _ := doublestar.Match(g, dir); ok {
			return true
		}
	}
	return false
}

// PackDirRole reports the role a pack's own [[dir]] map assigns to a
// single top-level segment, and whether the map speaks about it at all.
// An entry with no role (a category dir at pack root) still claims the
// segment — the annotation has said what the dir is, and it is not a
// format tree.
func PackDirRole(p *Pack, dir string) (role string, claimed bool) {
	if p == nil {
		return "", false
	}
	for _, d := range p.Dirs {
		dp := strings.Trim(d.Path, "/")
		if strings.Contains(dp, "/") || !strings.EqualFold(dp, dir) {
			continue
		}
		return d.Role, true
	}
	return "", false
}

// PackByDir finds the pack annotated for an on-disk directory name.
func (v *Vendor) PackByDir(dir string) *Pack {
	for i := range v.Packs {
		if v.Packs[i].Dir == dir {
			return &v.Packs[i]
		}
	}
	return nil
}

// PackByArchive finds the pack whose vendor download matches a zip name —
// the unzip-and-leave-it identity signal.
func (v *Vendor) PackByArchive(zip string) *Pack {
	for i := range v.Packs {
		for _, a := range v.Packs[i].Archives {
			if strings.EqualFold(a, zip) {
				return &v.Packs[i]
			}
		}
	}
	return nil
}

// MatchIdentity compares the content SHAs observed under one directory
// against a pack's manifest. kind is "exact" (same content set), "partial"
// (fraction of the manifest present), or "" (no identity data, unreadable
// manifest, or nothing matches).
func (v *Vendor) MatchIdentity(p *Pack, shas map[string]bool) (kind string, fraction float64) {
	if p.Identity.Manifest == "" {
		return "", 0
	}
	f, err := os.Open(filepath.Join(v.dir, filepath.FromSlash(p.Identity.Manifest)))
	if err != nil {
		return "", 0
	}
	defer f.Close()

	total, present := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		total++
		if shas[line] {
			present++
		}
	}
	if sc.Err() != nil || total == 0 || present == 0 {
		return "", 0
	}
	fraction = float64(present) / float64(total)
	if present == total && len(shas) == total {
		return "exact", 1
	}
	return "partial", fraction
}

// FormatTreeRank orders a pack's format trees the way the vendor itself
// does: 0 for the canonical audio dir, 1+ for the parallel exports in the
// order vendor.toml lists them, and a sentinel past the end for a tree a
// pack's own [[dir]] map declares that the vendor's globs don't name. ok
// is false when dir is not a format tree at all — same verdict as
// IsFormatTree. Consumers use the rank as a last-resort tiebreak when one
// pack ships the same sample under several trees.
func (v *Vendor) FormatTreeRank(p *Pack, dir string) (rank int, ok bool) {
	if !v.IsFormatTree(p, dir) {
		return 0, false
	}
	if v.CanonicalDir != "" && v.CanonicalDir != "." {
		if m, _ := doublestar.Match(v.CanonicalDir, dir); m {
			return 0, true
		}
	}
	for i, g := range v.ParallelDirs {
		if m, _ := doublestar.Match(g, dir); m {
			return i + 1, true
		}
	}
	return len(v.ParallelDirs) + 1, true
}
