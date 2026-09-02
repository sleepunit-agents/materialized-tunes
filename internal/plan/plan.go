// Package plan turns a view into a pre-flight report: exactly which
// sources are selected, exactly what each output will cost in bytes after
// transformation, and whether the whole thing fits the storage — all
// before a single file is read, transcoded, or copied. Output size is
// arithmetic (frames × rate ratio × channels × depth), not an estimate.
package plan

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
	"github.com/sleepunit-agents/materialized-tunes/internal/view"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// WAV header sizes ffmpeg's muxer produces under -bitexact: a canonical
// 16-byte fmt chunk for 16-bit PCM (44 bytes total), and a 40-byte
// WAVE_FORMAT_EXTENSIBLE fmt chunk for 24-bit (68 bytes total), regardless
// of channel count. Verified against ffmpeg 9.0 output.
const (
	wavHeaderBytes16 = 44
	wavHeaderBytes24 = 68
)

func wavHeaderBytes(bitDepth int) int64 {
	if bitDepth == 24 {
		return wavHeaderBytes24
	}
	return wavHeaderBytes16
}

// Entry is one source file that will materialize into one output file.
type Entry struct {
	Location   string `json:"location"`
	SourcePath string `json:"source_path"`
	Rule       int    `json:"rule"` // index of the include that picked it, in the view the plan was built from
	// PackPath is the source path through the pack directory ("Samples
	// From Mars/727 From Mars" under vendor-dirs, "727 From Mars" under a
	// flat location) — what a correction is scoped to (SPEC §19.3).
	PackPath string `json:"pack_path,omitempty"`
	// Kind names the placement failure a templated layout hit, if any:
	// "unsorted" (no instrument, mirror tree), "uncategorized" ({category}
	// fell back to _Unsorted), "general" ({instrument} is the family
	// catch-all) — the queues of SPEC §19.3, kinds B / A / C.
	Kind       string `json:"kind,omitempty"`
	SHA256     string `json:"sha256"`
	SourceSize int64  `json:"source_size"`

	OutPath     string `json:"out_path"`
	OutChannels int    `json:"out_channels"`
	DualMono    bool   `json:"dual_mono,omitempty"` // folded by taking one channel (L≡R), no −3 dB pad
	OutRate     int    `json:"out_rate"`
	OutDepth    int    `json:"out_depth"`
	OutFrames   int64  `json:"out_frames"`
	OutBytes    int64  `json:"out_bytes"`
	Copy        bool   `json:"copy,omitempty"`      // source already is the device format: copied byte-for-byte, no ffmpeg
	Companion   bool   `json:"companion,omitempty"` // Ableton document: sample refs rewritten to the materialized paths; OutBytes is the source size (estimate)

	InChannels int     `json:"in_channels"`
	InRate     int     `json:"in_rate"`
	InDepth    int     `json:"in_depth"`
	InFormat   string  `json:"in_format"`
	DurationS  float64 `json:"duration_s"`

	parents []string // intra-pack dirs a {file} layout may prepend to keep names apart

	// Which of the pack's format trees this file came out of, and the
	// vendor's own rank for it (0 = canonical audio dir). Set only when
	// the tree level was stripped from OutPath — which is exactly when
	// two cuts of one sample can land on the same output path. See cuts.go.
	tree     string
	treeRank int

	// reexport: the vendor re-renders its whole library per sampler
	// rather than cutting one render several ways, so cuts of one sample
	// need not be the same length ([formats] parallel_role).
	reexport bool

	// pack: the source path through the pack directory ("Samples From
	// Mars/727 From Mars"), set alongside tree. It is the scope every
	// question about parallel trees is asked in — a tree only replaces,
	// or is replaced by, another tree of the same pack.
	pack string

	placed placeFlags // what the layout template made of it; see recount
}

// placeFlags records the compromises a layout template made for one entry,
// so the plan's counters and warnings can be recomputed from the entries
// that actually survive — dropped format cuts, deduped bytes and the
// view's limit all cut the set down after placement.
type placeFlags uint8

const (
	placeUnsorted placeFlags = 1 << iota
	placeUncategorized
	placeGeneral
	placeFX
)

// kind names the worst placement failure the flags record — the queue
// a file belongs in. A file can be both general and uncategorized; the
// missing category is the faster question, so it goes there first.
func (f placeFlags) kind() string {
	switch {
	case f&placeUnsorted != 0:
		return "unsorted"
	case f&placeUncategorized != 0:
		return "uncategorized"
	case f&placeGeneral != 0:
		return "general"
	}
	return ""
}

// packPathOf is the source path through the pack directory under the
// location's layout, or "" for a path too shallow to sit in a pack.
func packPathOf(ws *workspace.Workspace, loc, srcPath string) string {
	lc, _ := ws.Location(loc)
	segs := strings.Split(srcPath, "/")
	if lc.Layout == "vendor-dirs" {
		if len(segs) < 3 {
			return ""
		}
		return segs[0] + "/" + segs[1]
	}
	if len(segs) < 2 {
		return ""
	}
	return segs[0]
}

type Skip struct {
	Location string `json:"location"`
	Path     string `json:"path"`
	Reason   string `json:"reason"`
}

type Collision struct {
	OutPath string   `json:"out_path"`
	Sources []string `json:"sources"` // "location:path"
}

// Overlap is a pair of includes that both pick the same sources but send
// them to different output prefixes, so every shared file lands twice.
// Almost always a recipe that grew a location-wide "**" rule on top of
// older per-pack rules; the narrower rule is the one to drop.
type Overlap struct {
	Location string `json:"location"`
	RuleA    int    `json:"rule_a"` // include index (0-based, in recipe order)
	GlobA    string `json:"glob_a"`
	AsA      string `json:"as_a"`
	RuleB    int    `json:"rule_b"`
	GlobB    string `json:"glob_b"`
	AsB      string `json:"as_b"`
	Files    int    `json:"files"` // sources picked by both
}

// applyRecipeOverrides folds the recipe's per-recipe say over device
// defaults into the loaded device — the one place it happens, so
// materialize (which reads p.Device), the lock (which records it) and
// migrate (which replays the lock) never each re-derive it. Today that is
// the [companions] block; [loudness] will land here too.
func applyRecipeOverrides(v *view.View, dev *profile.Device) {
	if v.Companions != nil {
		dev.Companions = *v.Companions
	}
}

type Plan struct {
	View    *view.View       `json:"view"`
	Device  *profile.Device  `json:"device"`
	Storage *profile.Storage `json:"storage"`

	Entries []Entry `json:"entries"`

	Matched            int    `json:"matched"`                        // selected by includes before any exclusion
	ExcludedByGlob     int    `json:"excluded_by_glob"`               //
	StrippedFormatTree int    `json:"stripped_format_tree,omitempty"` // outputs whose vendor format-tree level was dropped
	Renamed            int    `json:"renamed,omitempty"`              // outputs renamed distinguishing-first for the device display
	DualMonoFolded     int    `json:"dual_mono_folded,omitempty"`     // stereo sources with identical channels rendered as one channel, no pad
	Copied             int    `json:"copied,omitempty"`               // sources already in device format, copied without transcoding
	Companions         int    `json:"companions,omitempty"`           // Ableton documents riding along, sample refs rewritten at materialize
	Deduped            int    `json:"deduped,omitempty"`              // identical-content sources dropped by dedup = "content"
	CutsDropped        int    `json:"cuts_dropped,omitempty"`         // redundant format cuts of a sample the pack ships several ways
	CutsSplit          int    `json:"cuts_split,omitempty"`           // samples whose cuts land on different output paths, so every cut ships
	VendorPrepSkipped  int    `json:"vendor_prep_skipped,omitempty"`  // files under a re-export vendor's per-sampler trees, dropped by vendor_prep = "skip"
	Unsorted           int    `json:"unsorted,omitempty"`             // files a templated layout could not place (no instrument label) — under _Unsorted/
	Uncategorized      int    `json:"uncategorized,omitempty"`        // placed files whose {category} fell back to an _Unsorted folder
	General            int    `json:"general,omitempty"`              // placed files labeled only at family level — {instrument} rendered as _General
	FX                 int    `json:"fx,omitempty"`                   // known-FX files consolidated under FX/ regardless of instrument
	DisplayClashes     int    `json:"display_clashes,omitempty"`      // names still identical within naming.display_length
	LimitedFrom        int    `json:"limited_from,omitempty"`         // eligible count before the view's limit truncated it
	SkippedNonAudio    []Skip `json:"skipped_non_audio,omitempty"`
	SkippedDuration    []Skip `json:"skipped_duration,omitempty"`
	UnparseableAudio   []Skip `json:"unparseable_audio,omitempty"`

	Collisions []Collision `json:"collisions,omitempty"`
	Overlaps   []Overlap   `json:"overlaps,omitempty"` // sources landing twice via includes with different prefixes
	Errors     []string    `json:"errors"`             // any error ⇒ materialize refuses
	Warnings   []string    `json:"warnings"`

	// Aliases: sources that were selected but render under another
	// entry's output (content dedup). location\x00path → out_path, so a
	// companion referencing the dropped duplicate still resolves.
	Aliases map[string]string `json:"-"`

	TotalBytes   int64 `json:"total_bytes"`   // sum of exact output bytes
	TotalOnDisk  int64 `json:"total_on_disk"` // after cluster rounding (filesystem kind; else == TotalBytes)
	UsableBytes  int64 `json:"usable_bytes"`
	Fits         bool  `json:"fits"`
	SlotsUsed    int   `json:"slots_used,omitempty"`    // quota kind
	SlotsAllowed int   `json:"slots_allowed,omitempty"` // quota kind, 0 = unlimited
}

// Eligibility is the single definition of "can this cataloged file ride
// for this device" — shared by plan building and the device-lens catalog
// listing. Empty reason means eligible.
func Eligibility(dev *profile.Device, ce catalog.Entry) (reason string) {
	switch {
	case ce.Audio == nil && ce.DocErr != "" && dev.Companion(ce.Path):
		return "unreadable Live document: " + ce.DocErr
	case ce.Audio == nil && ce.AudioErr == "" && dev.Companion(ce.Path):
		return ""
	case ce.Audio == nil && ce.AudioErr != "":
		return "unparseable audio: " + ce.AudioErr
	case ce.Audio == nil:
		return "not an audio file"
	case ce.Audio.Channels > 2:
		return fmt.Sprintf("%d channels — samplers speak mono/stereo", ce.Audio.Channels)
	}
	if max := dev.Audio.MaxDurationSeconds; max > 0 && ce.Audio.DurationS > max+1e-9 {
		return fmt.Sprintf("%.1fs > %.1fs limit", ce.Audio.DurationS, max)
	}
	return ""
}

// FoldSpec is what the transcoder needs to know about channels for this
// entry: the effective channel mode ("mono" folds a 2-channel source) and
// the downmix to use — the device's, or "left" when the source is dual-mono
// (identical channels: taking one is lossless, and the −3 dB pad would
// only make it quieter than it was).
func (e Entry) FoldSpec(deviceDownmix string) (channels, downmix string) {
	if e.OutChannels == 1 && e.InChannels == 2 {
		if e.DualMono {
			return "mono", "left"
		}
		return "mono", deviceDownmix
	}
	return "stereo", deviceDownmix
}

// OutputChannels decides how many channels an eligible source renders with
// on a device, and whether it is being folded as dual-mono (both channels
// identical per the catalog): mono devices fold everything, but a
// dual-mono source folds by taking one channel — no −3 dB pad; stereo-
// preserving devices fold dual-mono only when audio.dual_mono = "fold".
func OutputChannels(dev *profile.Device, ce catalog.Entry) (channels int, dualMono bool) {
	channels = ce.Audio.Channels
	dualMono = ce.Audio.Channels == 2 && ce.Audio.DualMono != nil && *ce.Audio.DualMono
	switch {
	case dev.Audio.Channels == "mono":
		channels = 1
	case dualMono && dev.Audio.DualMono == "fold":
		channels = 1
	default:
		dualMono = false // stereo device keeping stereo: the verdict changes nothing
	}
	return channels, dualMono
}

// Passthrough reports whether a source already is what the device wants —
// a PCM WAV at the device's rate, depth and channel count — so
// materialize copies its bytes instead of rendering. A transcode of such a
// file would only strip metadata and rewrite the header; for a DAW-library
// device that is nearly every file, and an ffmpeg spawn per file is the
// whole cost. 16/24-bit WAV is assumed integer PCM (float WAV is 32-bit).
func Passthrough(dev *profile.Device, ce catalog.Entry) bool {
	a := ce.Audio
	if a == nil || a.Format != "wav" {
		return false
	}
	if a.BitDepth != dev.Audio.BitDepth || (a.BitDepth != 16 && a.BitDepth != 24) {
		return false
	}
	if a.SampleRate != dev.Audio.SampleRate {
		return false
	}
	outCh, _ := OutputChannels(dev, ce)
	return outCh == a.Channels
}

// ConvertedBytes predicts the post-transform output size of an eligible
// catalog entry on a device — the same math Build uses for fit, exported
// so pack summaries can be computed from the catalog alone.
func ConvertedBytes(dev *profile.Device, ce catalog.Entry) int64 {
	if Passthrough(dev, ce) {
		return ce.Size
	}
	outCh, _ := OutputChannels(dev, ce)
	outFrames := int64(math.Round(float64(ce.Audio.Frames) *
		float64(dev.Audio.SampleRate) / float64(ce.Audio.SampleRate)))
	dataBytes := outFrames * int64(outCh) * int64(dev.Audio.BitDepth) / 8
	return wavHeaderBytes(dev.Audio.BitDepth) + dataBytes + dataBytes%2
}

// Build computes the plan for a view against the current catalogs.
func Build(ws *workspace.Workspace, viewName string) (*Plan, error) {
	v, err := view.Load(ws.Root, viewName)
	if err != nil {
		return nil, err
	}
	return BuildView(ws, v)
}

// BuildView computes the plan for an in-memory view — the UI's Plan step
// builds these with rules toggled off, without touching the recipe file
// on disk.
func BuildView(ws *workspace.Workspace, v *view.View) (*Plan, error) {
	return BuildWith(ws, v, Options{})
}

// Options tune a build: Inputs shares loaded sources across builds (nil
// loads fresh), Progress hears each stage as it advances (nil is silent).
type Options struct {
	Inputs   *Inputs
	Progress func(stage string, done, total int)
}

// Build stages, in order, as Progress reports them.
const (
	StageLoad   = "loading catalogs"
	StageSelect = "selecting"
	StagePlace  = "placing"
	StageCuts   = "resolving cuts and duplicates"
	StageCheck  = "checking names and fit"
)

// BuildWith is BuildView with shared inputs and progress.
func BuildWith(ws *workspace.Workspace, v *view.View, opt Options) (*Plan, error) {
	in := opt.Inputs
	if in == nil {
		in = NewInputs(ws)
	}
	progress := opt.Progress
	if progress == nil {
		progress = func(string, int, int) {}
	}
	dev, err := profile.LoadDevice(ws.Root, v.Device)
	if err != nil {
		return nil, err
	}
	applyRecipeOverrides(v, dev)
	sto, err := profile.LoadStorage(ws.Root, v.Storage)
	if err != nil {
		return nil, err
	}

	p := &Plan{View: v, Device: dev, Storage: sto}

	lay, err := view.ParseLayout(v.Layout)
	if err != nil {
		return nil, fmt.Errorf("view %s: %w", v.Name, err)
	}

	// Format-tree stripping and layout templates are vendor knowledge:
	// load annotations once. Strippers are per location (nil when the
	// view keeps trees or the location's vendors are unknown).
	var vendors []annotations.Vendor
	if v.FormatTree != "keep" || lay != nil {
		if vendors, err = in.Vendors(); err != nil {
			return nil, err
		}
	}
	var strippers map[string]*treeStripper
	if v.FormatTree != "keep" {
		strippers = map[string]*treeStripper{}
		for _, inc := range v.Include {
			if _, done := strippers[inc.Location]; done {
				continue
			}
			lc, _ := ws.Location(inc.Location)
			strippers[inc.Location] = newTreeStripper(lc, vendors)
		}
	}

	catalogs := map[string]map[string]catalog.Entry{}
	for i, inc := range v.Include {
		if _, done := catalogs[inc.Location]; done {
			continue
		}
		if _, ok := ws.Location(inc.Location); !ok {
			return nil, fmt.Errorf("view %s: unknown location %q", v.Name, inc.Location)
		}
		progress(StageLoad, i, len(v.Include))
		entries, err := in.Catalog(inc.Location)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("location %q has no catalog — run `mtunes scan %s` first", inc.Location, inc.Location)
		}
		catalogs[inc.Location] = entries
	}

	var ly *layouter
	if lay != nil {
		if !harvest.MetaFresh(ws) {
			// the meta cache predates this build's format — re-derive it
			// from the catalogs so the layout reads current classifications
			for _, inc := range v.Include {
				if lc, ok := ws.Location(inc.Location); ok {
					harvest.Run(ws, lc)
				}
			}
			in.Reset()
		}
		if ly, err = newLayouter(in, v, lay, vendors); err != nil {
			return nil, err
		}
		asRules := 0
		for _, inc := range v.Include {
			if inc.As != "" {
				asRules++
			}
		}
		if asRules > 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("layout %q decides every output path — `as` on %d %s is ignored",
				lay.Template, asRules, plural(asRules, "rule", "rules")))
		}
	}

	type picked struct {
		inc  view.Include
		rule int
		ce   catalog.Entry
	}
	var selection []picked
	seen := map[string]bool{} // location:path:as — the same source through two
	// includes with the same prefix is one output, not a collision
	first := map[string]int{}   // location:path → index of the include that picked it first
	overlap := map[[2]int]int{} // {first include, later include with a different As} → shared sources
	sortedPaths := map[string][]string{}
	for ii, inc := range v.Include {
		progress(StageSelect, ii, len(v.Include))
		cat := catalogs[inc.Location]
		paths, ok := sortedPaths[inc.Location]
		if !ok {
			paths = make([]string, 0, len(cat))
			for p := range cat {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			sortedPaths[inc.Location] = paths
		}
		for _, sp := range paths {
			ok, _ := doublestar.Match(inc.Glob, sp)
			if !ok {
				continue
			}
			p.Matched++
			excluded := false
			for _, exc := range v.Exclude {
				if hit, _ := doublestar.Match(exc.Glob, sp); hit {
					excluded = true
					break
				}
			}
			if excluded {
				p.ExcludedByGlob++
				continue
			}
			asKey := inc.As
			if lay != nil { // the template decides the path; two rules picking one file is one output
				asKey = ""
			}
			key := inc.Location + "\x00" + sp + "\x00" + asKey
			if seen[key] {
				continue
			}
			seen[key] = true
			if fi, dup := first[inc.Location+"\x00"+sp]; dup {
				overlap[[2]int{fi, ii}]++
			} else {
				first[inc.Location+"\x00"+sp] = ii
			}
			selection = append(selection, picked{inc, ii, cat[sp]})
		}
	}

	for pair, n := range overlap {
		a, b := v.Include[pair[0]], v.Include[pair[1]]
		p.Overlaps = append(p.Overlaps, Overlap{Location: a.Location, RuleA: pair[0], GlobA: a.Glob, AsA: a.As, RuleB: pair[1], GlobB: b.Glob, AsB: b.As, Files: n})
	}
	sort.Slice(p.Overlaps, func(i, j int) bool {
		if p.Overlaps[i].RuleA != p.Overlaps[j].RuleA {
			return p.Overlaps[i].RuleA < p.Overlaps[j].RuleA
		}
		return p.Overlaps[i].RuleB < p.Overlaps[j].RuleB
	})
	for _, o := range p.Overlaps {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s files land twice: rule %d (%s → %s) and rule %d (%s → %s) send them to different folders — drop the narrower rule",
			o.Files, o.Location, o.RuleA+1, o.GlobA, prefixLabel(o.AsA), o.RuleB+1, o.GlobB, prefixLabel(o.AsB)))
	}

	var docsBlind []string // documents none of whose refs are in their location's catalog
	for si, pk := range selection {
		if si%2000 == 0 {
			progress(StagePlace, si, len(selection))
		}
		ce := pk.ce
		loc := pk.inc.Location
		if reason := Eligibility(dev, ce); reason != "" {
			skip := Skip{loc, ce.Path, reason}
			switch {
			case ce.Audio == nil && ce.AudioErr == "":
				p.SkippedNonAudio = append(p.SkippedNonAudio, skip)
			case ce.Audio != nil && ce.Audio.Channels <= 2:
				p.SkippedDuration = append(p.SkippedDuration, skip)
			default:
				p.UnparseableAudio = append(p.UnparseableAudio, skip)
			}
			continue
		}

		srcForOut, tree, treeRank, reexport, pack := ce.Path, "", 0, false, ""
		if st := strippers[loc]; st != nil && (lay != nil || !globCoversTree(pk.inc, st.vendorDirs)) {
			if stripped, t, ok := st.strip(ce.Path); ok {
				srcForOut, tree, treeRank, reexport, pack = stripped, t.name, t.rank, t.reexport, t.pack
			}
		}
		// Where it lands: the template when the recipe has one, else the
		// include's `as` over the mirrored path.
		var out string
		var parents []string
		var placed placeFlags
		packPath := packPathOf(ws, loc, ce.Path)
		if ly != nil {
			var pl placement
			if ce.Audio == nil { // a document lands beside the samples it points at
				m, resolved, _ := ly.docMeta(loc, ce)
				pl = ly.placeMeta(loc, srcForOut, m, resolved > 0)
				if resolved == 0 {
					docsBlind = append(docsBlind, path.Base(ce.Path))
				}
			} else {
				pl = ly.place(loc, srcForOut, ce.Path)
			}
			out, parents = pl.out, pl.parents
			if pl.unsorted {
				placed |= placeUnsorted
			}
			if pl.uncategorized {
				placed |= placeUncategorized
			}
			if pl.general {
				placed |= placeGeneral
			}
			if pl.fx {
				placed |= placeFX
			}
		} else {
			out = mirrorPath(pk.inc, srcForOut)
		}
		if ce.Audio == nil { // companion document
			p.Entries = append(p.Entries, Entry{
				Location:   loc,
				SourcePath: ce.Path,
				Rule:       pk.rule,
				PackPath:   packPath,
				Kind:       placed.kind(),
				SHA256:     ce.SHA256,
				SourceSize: ce.Size,
				OutPath:    out,
				OutBytes:   ce.Size,
				Companion:  true,
				InFormat:   strings.ToLower(strings.TrimPrefix(path.Ext(ce.Path), ".")),
				parents:    parents,
				tree:       tree,
				treeRank:   treeRank,
				reexport:   reexport,
				pack:       pack,
				placed:     placed,
			})
			continue
		}

		outCh, dualMono := OutputChannels(dev, ce)
		outFrames := int64(math.Round(float64(ce.Audio.Frames) *
			float64(dev.Audio.SampleRate) / float64(ce.Audio.SampleRate)))
		outBytes := ConvertedBytes(dev, ce)
		copyThrough := Passthrough(dev, ce)

		p.Entries = append(p.Entries, Entry{
			Location:    loc,
			SourcePath:  ce.Path,
			Rule:        pk.rule,
			PackPath:    packPath,
			Kind:        placed.kind(),
			SHA256:      ce.SHA256,
			SourceSize:  ce.Size,
			OutPath:     wavExt(out),
			OutChannels: outCh,
			DualMono:    dualMono,
			OutRate:     dev.Audio.SampleRate,
			OutDepth:    dev.Audio.BitDepth,
			OutFrames:   outFrames,
			OutBytes:    outBytes,
			Copy:        copyThrough,
			InChannels:  ce.Audio.Channels,
			InRate:      ce.Audio.SampleRate,
			InDepth:     ce.Audio.BitDepth,
			InFormat:    ce.Audio.Format,
			DurationS:   ce.Audio.DurationS,
			parents:     parents,
			tree:        tree,
			treeRank:    treeRank,
			reexport:    reexport,
			pack:        pack,
			placed:      placed,
		})
	}
	progress(StagePlace, len(selection), len(selection))
	sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].OutPath < p.Entries[j].OutPath })
	progress(StageCuts, 0, 1)

	// A re-export vendor's sampler trees leave first: they are not cuts to
	// choose between, they are the vendor's own device prep, and dropping
	// them here means the cut resolver never has to adjudicate a set that
	// was never a real choice.
	if v.VendorPrep != "keep" {
		p.skipVendorPrep()
	}

	if n := len(docsBlind); n > 0 {
		sort.Strings(docsBlind)
		ex := docsBlind
		if len(ex) > 3 {
			ex = append(ex[:3], "…")
		}
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d Ableton %s at no sample this location's catalog holds — placed under %s/ by its own path (%s); rescan the location if the catalog predates the doc field",
			n, plural(n, "document points", "documents point"), UnsortedDir, strings.Join(ex, ", ")))
	}

	// Redundant format cuts go before anything that looks at output paths
	// in aggregate: dedup, disambiguation and the fit are all answers about
	// what actually materializes.
	if v.Cuts != "all" {
		p.pickCuts(dev.Naming.CaseSensitive)
	}

	if v.Dedup == "content" {
		// Identical bytes render once — the first output path in sort order
		// wins, so the choice is deterministic and pins in the lock.
		seenSHA := map[string]string{}
		kept := p.Entries[:0]
		for _, e := range p.Entries {
			if out, dup := seenSHA[e.SHA256]; dup {
				p.Deduped++
				if p.Aliases == nil {
					p.Aliases = map[string]string{}
				}
				p.Aliases[e.Location+"\x00"+e.SourcePath] = out // kept entry's location\x00path; resolved to its OutPath below
				continue
			}
			seenSHA[e.SHA256] = e.Location + "\x00" + e.SourcePath
			kept = append(kept, e)
		}
		p.Entries = kept
	}

	if lay.Uses(view.TokFile) {
		disambiguate(p.Entries, dev.Naming.CaseSensitive)
	}

	if v.Limit > 0 && len(p.Entries) > v.Limit {
		p.LimitedFrom = len(p.Entries)
		p.Entries = p.Entries[:v.Limit]
	}

	p.recount()
	progress(StageCheck, 0, 1)

	if dev.Delivery.Layout == "flatten" {
		flatten(p.Entries, dev.Naming.CaseSensitive)
		sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].OutPath < p.Entries[j].OutPath })
	}

	if len(dev.Naming.Sanitize) > 0 {
		sanitizeNames(p.Entries, dev.Naming.Sanitize)
		sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].OutPath < p.Entries[j].OutPath })
	}

	if dev.Naming.Rename == "distinguishing-first" && dev.Naming.DisplayLength > 0 {
		p.Renamed = distinguishingFirst(p.Entries, dev.Naming.DisplayLength, dev.Naming.CaseSensitive)
		sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].OutPath < p.Entries[j].OutPath })
	}

	if len(p.Aliases) > 0 {
		outOf := map[string]string{}
		for _, e := range p.Entries {
			outOf[e.Location+"\x00"+e.SourcePath] = e.OutPath
		}
		for k, kept := range p.Aliases {
			p.Aliases[k] = outOf[kept]
		}
	}

	p.checkCollisions()
	p.checkDisplay()
	p.checkNaming()
	p.checkFit()
	return p, nil
}

// treeStripper drops a vendor's format-tree level from catalog paths: the
// segment right after the pack dir when annotations say it is the
// canonical audio dir or a parallel export ("808 From Mars/WAV/Kicks/x" →
// "808 From Mars/Kicks/x", "ASMR/ASMR 24 bit stereo/y" → "ASMR/y"), or a
// deeper segment the pack's own [[dir]] map gives a tree role ("Modular
// Creations/1. Modular Loops (120 BPM)/WAV/STO/x" → ".../1. Modular Loops
// (120 BPM)/STO/x"). Pack depth follows the location layout; the vendor
// comes from the location's slug, or from the top dir under vendor-dirs.
type treeStripper struct {
	vendorDirs bool
	fixed      *annotations.Vendor // single-vendor location
	vendors    []annotations.Vendor
	byTop      map[string]*annotations.Vendor // vendor-dirs: top dir → vendor (nil = unknown)
}

// newTreeStripper always returns a stripper: even a location whose vendors
// are wholly unknown gets the structural rule — a dir named like its pack
// plus format words ("Thump 16 bit mono") is a tree by naming alone.
func newTreeStripper(lc workspace.LocationConfig, vendors []annotations.Vendor) *treeStripper {
	st := &treeStripper{vendorDirs: lc.Layout == "vendor-dirs", vendors: vendors, byTop: map[string]*annotations.Vendor{}}
	if !st.vendorDirs {
		st.fixed = annotations.BySlug(vendors)[lc.Vendor]
	}
	return st
}

// globCoversTree reports whether an include's static glob root already
// reaches past the pack dir into the format tree ("<pack>/WAV/**"): its
// `as` (or the mirror) then already decides that level, so stripping would
// double up. Pack depth is 1 (flat) or 2 (vendor-dirs).
func globCoversTree(inc view.Include, vendorDirs bool) bool {
	root := strings.Trim(view.GlobRoot(inc.Glob), "/")
	if root == "" {
		return false
	}
	depth := len(strings.Split(root, "/"))
	packDepth := 1
	if vendorDirs {
		packDepth = 2
	}
	return depth > packDepth
}

// treeInfo is what strip learned about the format tree it removed: the
// segment itself, the vendor's own rank for it (0 = canonical audio dir),
// and whether this vendor's parallel trees are re-exports rather than
// cuts of one render.
type treeInfo struct {
	name     string
	rank     int
	reexport bool
	pack     string
}

// strip returns the path without its format-tree segment and what the
// vendor knows about that tree. ok is false when there was no tree to
// remove. Annotations speak first — the vendor's globs and the pack's own
// [[dir]] map; where both are silent, the structural rule reads the dir's
// name against the pack's ("Thump 16 bit mono" under "Thump"), so packs
// and vendors nobody has annotated still shed their format level.
func (st *treeStripper) strip(p string) (out string, t treeInfo, ok bool) {
	segs := strings.Split(p, "/")
	packIdx := 0 // index of the pack dir segment
	vendor := st.fixed
	if st.vendorDirs {
		if len(segs) < 4 { // vendor/pack/tree/file at minimum
			return p, treeInfo{}, false
		}
		packIdx = 1
		v, seen := st.byTop[segs[0]]
		if !seen {
			v = annotations.ByName(st.vendors, segs[0])
			st.byTop[segs[0]] = v
		}
		vendor = v
	} else if len(segs) < 3 { // pack/tree/file
		return p, treeInfo{}, false
	}
	tree := segs[packIdx+1]
	// stripAt removes the segment at index at and reports what it was.
	stripAt := func(at, rank int, reexport bool) (string, treeInfo, bool) {
		kept := append(append([]string{}, segs[:at]...), segs[at+1:]...)
		return strings.Join(kept, "/"), treeInfo{segs[at], rank, reexport, strings.Join(segs[:packIdx+1], "/")}, true
	}
	if vendor != nil {
		pack := vendor.PackByDir(segs[packIdx])
		if rank, ok := vendor.FormatTreeRank(pack, tree); ok {
			return stripAt(packIdx+1, rank, vendor.ParallelRole == "reexport")
		}
		// The format level can sit a dir or more DOWN — Modular Creations
		// From Mars has no WAV at pack root; its "1. Modular Loops (120
		// BPM)" holds WAV, Apple Loops and REX2 renders of the same loops
		// — and only the pack's own [[dir]] map can say so. A nested entry
		// with a tree role is a human statement and is honoured at any
		// depth; the vendor's globs are deliberately not read below the
		// top, where a "Presets" folder inside a content tree is content
		// until someone says otherwise. The dir keeps its place in the
		// output path: only the tree segment goes.
		for d := packIdx + 2; d < len(segs)-1; d++ {
			role, claimed := annotations.PackDirRoleAt(pack, strings.Join(segs[packIdx+1:d+1], "/"))
			if !claimed {
				continue
			}
			switch role {
			case "canonical-audio":
				return stripAt(d, 0, false)
			case "format-tree":
				// rank it where the vendor's globs would, else past them
				// all; never 0 — the entry said this is not the canonical
				// render, whatever the dir is called
				rank, ok := vendor.FormatTreeRank(nil, segs[d])
				if !ok || rank == 0 {
					rank = len(vendor.ParallelDirs) + 1
				}
				return stripAt(d, rank, vendor.ParallelRole == "reexport")
			}
		}
		// the pack's own [[dir]] map spoke and said this dir is not a
		// tree — a category dir at pack root stays what the human said
		if _, claimed := annotations.PackDirRole(pack, tree); claimed {
			return p, treeInfo{}, false
		}
	}
	if rank, ok := heuristicTreeRank(segs[packIdx], tree); ok {
		return stripAt(packIdx+1, rank, false)
	}
	return p, treeInfo{}, false
}

// prefixLabel names an include's output prefix for humans: the As value
// or "mirror" when the source tree is kept as-is.
func prefixLabel(as string) string {
	if as == "" {
		return "mirror"
	}
	return strings.TrimSuffix(as, "/") + "/"
}

// mirrorPath maps a source path to its output path under the mirror
// layout: the path as-is, or the include's As prefix replacing the glob's
// static root.
func mirrorPath(inc view.Include, srcPath string) string {
	if inc.As == "" {
		return srcPath
	}
	root := view.GlobRoot(inc.Glob)
	return strings.TrimSuffix(inc.As, "/") + "/" + strings.TrimPrefix(srcPath, root)
}

// wavExt gives an audio output its .wav extension — that is what a
// transcode means. Companion documents keep theirs.
func wavExt(out string) string {
	return strings.TrimSuffix(out, path.Ext(out)) + ".wav"
}

// flatten rewrites every OutPath to a bare filename for devices with no
// folder concept. Names that collide are disambiguated by prepending just
// enough trailing parent directories ("KitA - Kick 01.wav"), one level at
// a time, only for the names that need it — already-unique names stay
// clean. Deterministic, so it pins in lockfiles like everything else.
// Anything still colliding afterwards falls through to checkCollisions
// and errors there.
func flatten(entries []Entry, caseSensitive bool) {
	dirs := make([]string, len(entries))
	names := make([]string, len(entries))
	parents := make([][]string, len(entries))
	for i, e := range entries {
		dir, file := path.Split(e.OutPath)
		parents[i] = strings.FieldsFunc(dir, func(r rune) bool { return r == '/' })
		names[i] = file
	}
	for i, n := range uniquify(dirs, names, parents, caseSensitive) {
		entries[i].OutPath = n
	}
}

// displayKey is the part of a filename a cropped device browser shows: the
// directory plus the first n characters of the base name (extension
// dropped — devices don't show it), case-folded unless the device cares.
func displayKey(outPath string, n int, caseSensitive bool) string {
	dir, file := path.Split(outPath)
	base := strings.TrimSuffix(file, path.Ext(file))
	if r := []rune(base); len(r) > n {
		base = string(r[:n])
	}
	if !caseSensitive {
		base = strings.ToLower(base)
	}
	return dir + "\x00" + base
}

var nameTokenRe = regexp.MustCompile(`[^ _\-]+`)

// distinguishingFirst rewrites names that are indistinct within the first
// n characters of the display so that what differs comes first: within
// each clashing group (same dir, same n-prefix) the longest common token
// prefix is found and every member becomes "<its remaining tokens> <common
// prefix>". "BD A 808 Decay A 01".."06" → "01 BD A 808 Decay A".."06 …".
// One pass can create a fresh clash (moving "01" forward makes "01 BD A
// 808 Decay A" and "…Decay B" agree for 16 chars), so it iterates: each
// pass pulls the next differing token to the front, until nothing clashes
// or nothing can move ("A 01 BD A 808 Decay"). A member with nothing left
// over (it IS the common prefix) keeps its name. Only clashing names move;
// everything else is untouched. Returns the number of names changed.
func distinguishingFirst(entries []Entry, n int, caseSensitive bool) int {
	touched := map[int]bool{}
	for pass := 0; pass < 8; pass++ {
		moved := distinguishingPass(entries, n, caseSensitive, touched)
		if moved == 0 {
			break
		}
	}
	return len(touched)
}

func distinguishingPass(entries []Entry, n int, caseSensitive bool, touched map[int]bool) int {
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	groups := map[string][]int{}
	for i, e := range entries {
		k := displayKey(e.OutPath, n, caseSensitive)
		groups[k] = append(groups[k], i)
	}
	renamed := 0
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		toks := make([][]string, len(idxs))
		for j, i := range idxs {
			_, file := path.Split(entries[i].OutPath)
			toks[j] = nameTokenRe.FindAllString(strings.TrimSuffix(file, path.Ext(file)), -1)
		}
		// longest common token prefix across the group
		common := 0
		for {
			if common >= len(toks[0]) {
				break
			}
			t := fold(toks[0][common])
			same := true
			for _, tk := range toks[1:] {
				if common >= len(tk) || fold(tk[common]) != t {
					same = false
					break
				}
			}
			if !same {
				break
			}
			common++
		}
		if common == 0 {
			continue // nothing shared at token level; the clash is inside a token — leave it
		}
		for j, i := range idxs {
			rest := toks[j][common:]
			if len(rest) == 0 {
				continue
			}
			dir, file := path.Split(entries[i].OutPath)
			ext := path.Ext(file)
			newBase := strings.Join(rest, " ") + " " + strings.Join(toks[j][:common], " ")
			entries[i].OutPath = dir + newBase + ext
			touched[i] = true
			renamed++
		}
	}
	return renamed
}

// checkDisplay warns about names the device's browser cannot tell apart —
// identical within naming.display_length in the same folder. Runs after
// any rename policy, so it reports what is left.
func (p *Plan) checkDisplay() {
	n := p.Device.Naming.DisplayLength
	if n <= 0 {
		return
	}
	groups := map[string][]string{}
	for _, e := range p.Entries {
		k := displayKey(e.OutPath, n, p.Device.Naming.CaseSensitive)
		groups[k] = append(groups[k], e.OutPath)
	}
	keys := make([]string, 0, len(groups))
	for k, v := range groups {
		if len(v) > 1 {
			keys = append(keys, k)
			p.DisplayClashes += len(v)
		}
	}
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	ex := groups[keys[0]]
	sort.Strings(ex)
	msg := fmt.Sprintf("%d names look identical on the device (first %d chars) — e.g. %q and %q",
		p.DisplayClashes, n, ex[0], ex[1])
	if p.Device.Naming.Rename == "" {
		msg += `; set naming.rename = "distinguishing-first" to move what differs to the front`
	}
	p.Warnings = append(p.Warnings, msg)
}

// sanitizeNames applies the device's naming.sanitize map to every OutPath,
// directories and filenames alike — devices that reject a character reject
// it anywhere in the path. Runs before the collision and naming checks, so
// a rewrite that merges two names errors there, and a character the map
// doesn't cover still fails allowed_chars.
func sanitizeNames(entries []Entry, rules map[string]string) {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(rules)*2)
	for _, k := range keys {
		pairs = append(pairs, k, rules[k])
	}
	r := strings.NewReplacer(pairs...)
	for i := range entries {
		entries[i].OutPath = r.Replace(entries[i].OutPath)
	}
}

func (p *Plan) checkCollisions() {
	byOut := map[string][]string{}
	for _, e := range p.Entries {
		key := e.OutPath
		if !p.Device.Naming.CaseSensitive {
			key = strings.ToLower(key)
		}
		byOut[key] = append(byOut[key], e.Location+":"+e.SourcePath)
	}
	keys := make([]string, 0, len(byOut))
	for k := range byOut {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(byOut[k]) > 1 {
			p.Collisions = append(p.Collisions, Collision{OutPath: k, Sources: byOut[k]})
			p.Errors = append(p.Errors, fmt.Sprintf("collision: %d sources render to %s (%s)",
				len(byOut[k]), k, strings.Join(byOut[k], ", ")))
		}
	}
}

func (p *Plan) checkNaming() {
	n := p.Device.Naming
	perDir := map[string]int{}
	longNames, longPaths := 0, 0

	var disallowed *regexp.Regexp
	if n.AllowedChars != "" {
		disallowed, _ = n.DisallowedRe() // validated at device load
	}

	for _, e := range p.Entries {
		dir, file := path.Split(e.OutPath)
		perDir[dir]++
		if n.MaxFilenameLength > 0 && len(file) > n.MaxFilenameLength {
			longNames++
		}
		if n.MaxPathLength > 0 && len(e.OutPath) > n.MaxPathLength {
			longPaths++
		}
		if disallowed != nil && disallowed.MatchString(file) {
			p.Errors = append(p.Errors, fmt.Sprintf("filename %q contains characters outside [%s]", file, n.AllowedChars))
		}
	}
	if longNames > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d filenames longer than %d chars", longNames, n.MaxFilenameLength))
	}
	if longPaths > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d output paths longer than %d chars", longPaths, n.MaxPathLength))
	}
	if n.MaxFilesPerDir > 0 {
		dirs := make([]string, 0, len(perDir))
		for d := range perDir {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			if perDir[d] > n.MaxFilesPerDir {
				name := d
				if name == "" {
					name = "(root)"
				}
				p.Errors = append(p.Errors, fmt.Sprintf("directory %s has %d files (device limit %d per folder)",
					name, perDir[d], n.MaxFilesPerDir))
			}
		}
	}
}

func (p *Plan) checkFit() {
	for _, e := range p.Entries {
		p.TotalBytes += e.OutBytes
		if p.Storage.Kind == "filesystem" {
			c := p.Storage.ClusterBytes
			p.TotalOnDisk += (e.OutBytes + c - 1) / c * c
		}
	}
	if p.Storage.Kind != "filesystem" {
		p.TotalOnDisk = p.TotalBytes
	}
	p.UsableBytes = p.Storage.UsableBytes()
	p.Fits = p.TotalOnDisk <= p.UsableBytes

	if !p.Fits {
		p.Errors = append(p.Errors, fmt.Sprintf("does not fit: %s needed, %s usable (%s capacity)",
			HumanBytes(p.TotalOnDisk), HumanBytes(p.UsableBytes), HumanBytes(p.Storage.CapacityBytes)))
	}
	if p.Storage.Kind == "quota" {
		p.SlotsUsed = len(p.Entries)
		p.SlotsAllowed = p.Storage.MaxFiles
		if p.SlotsAllowed > 0 && p.SlotsUsed > p.SlotsAllowed {
			p.Errors = append(p.Errors, fmt.Sprintf("too many files: %d selected, device has %d slots",
				p.SlotsUsed, p.SlotsAllowed))
		}
	}
}

func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// recount derives every "how many, and what happened to them" counter from
// the entries that actually materialize, and issues the warnings about
// what the layout could not place. It runs once the entry set is final —
// after dropped format cuts, content dedup and the view's limit — so the
// numbers a human reads describe the output, not the selection that fed
// it. Counting during placement instead would triple the unsorted figure
// for any pack that ships its library three ways.
func (p *Plan) recount() {
	p.StrippedFormatTree, p.Copied, p.DualMonoFolded, p.Companions = 0, 0, 0, 0
	p.Unsorted, p.Uncategorized, p.General, p.FX = 0, 0, 0, 0
	var unsortedEx, uncatEx, generalEx string
	for _, e := range p.Entries {
		if e.tree != "" {
			p.StrippedFormatTree++
		}
		if e.Companion {
			p.Companions++
		}
		if e.Copy {
			p.Copied++
		}
		if e.DualMono {
			p.DualMonoFolded++
		}
		if e.Companion {
			// A document lands where its samples landed; it has no facts
			// of its own to correct. Where it fell to _Unsorted, the
			// decision is on the sample folder (already counted) or the
			// vote genuinely split — either way, not a question to ask
			// about the rack. Kind still says where it went.
			continue
		}
		if e.placed&placeUnsorted != 0 {
			p.Unsorted++
			if unsortedEx == "" {
				unsortedEx = e.SourcePath
			}
		}
		if e.placed&placeUncategorized != 0 {
			p.Uncategorized++
			if uncatEx == "" {
				uncatEx = e.SourcePath
			}
		}
		if e.placed&placeGeneral != 0 {
			p.General++
			if generalEx == "" {
				generalEx = e.SourcePath
			}
		}
		if e.placed&placeFX != 0 {
			p.FX++
		}
	}
	if p.Unsorted > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s carry no instrument label the layout can use and land under %s/ (mirror tree) — e.g. %s",
			p.Unsorted, plural(p.Unsorted, "file", "files"), UnsortedDir, unsortedEx))
	}
	if p.Uncategorized > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s carry no loop/one-shot signal in their naming and land in an %s/ category folder — e.g. %s (vendor annotation or the shared categories.toml can teach it)",
			p.Uncategorized, plural(p.Uncategorized, "file", "files"), UnsortedDir, uncatEx))
	}
	if p.General > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d %s are labeled only at family level (\"drums\", \"woodwind\", …) and land in a %s/ instrument folder — e.g. %s (instruments.toml can teach finer labels)",
			p.General, plural(p.General, "file", "files"), GeneralDir, generalEx))
	}
}
