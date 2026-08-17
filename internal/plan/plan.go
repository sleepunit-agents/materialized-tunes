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
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/view"
	"github.com/jbarket/materialized-tunes/internal/workspace"
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
	SHA256     string `json:"sha256"`
	SourceSize int64  `json:"source_size"`

	OutPath     string `json:"out_path"`
	OutChannels int    `json:"out_channels"`
	DualMono    bool   `json:"dual_mono,omitempty"` // folded by taking one channel (L≡R), no −3 dB pad
	OutRate     int    `json:"out_rate"`
	OutDepth    int    `json:"out_depth"`
	OutFrames   int64  `json:"out_frames"`
	OutBytes    int64  `json:"out_bytes"`

	InChannels int     `json:"in_channels"`
	InRate     int     `json:"in_rate"`
	InDepth    int     `json:"in_depth"`
	InFormat   string  `json:"in_format"`
	DurationS  float64 `json:"duration_s"`
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
	Deduped            int    `json:"deduped,omitempty"`              // identical-content sources dropped by dedup = "content"
	DisplayClashes     int    `json:"display_clashes,omitempty"`      // names still identical within naming.display_length
	LimitedFrom        int    `json:"limited_from,omitempty"`         // eligible count before the view's limit truncated it
	SkippedNonAudio    []Skip `json:"skipped_non_audio,omitempty"`
	SkippedDuration    []Skip `json:"skipped_duration,omitempty"`
	UnparseableAudio   []Skip `json:"unparseable_audio,omitempty"`

	Collisions []Collision `json:"collisions,omitempty"`
	Errors     []string    `json:"errors"` // any error ⇒ materialize refuses
	Warnings   []string    `json:"warnings"`

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

// ConvertedBytes predicts the post-transform output size of an eligible
// catalog entry on a device — the same math Build uses for fit, exported
// so pack summaries can be computed from the catalog alone.
func ConvertedBytes(dev *profile.Device, ce catalog.Entry) int64 {
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

// BuildView computes the plan for an in-memory view — the UI's preflight
// preview builds these with rules toggled off, without touching the recipe
// file on disk.
func BuildView(ws *workspace.Workspace, v *view.View) (*Plan, error) {
	dev, err := profile.LoadDevice(ws.Root, v.Device)
	if err != nil {
		return nil, err
	}
	sto, err := profile.LoadStorage(ws.Root, v.Storage)
	if err != nil {
		return nil, err
	}

	p := &Plan{View: v, Device: dev, Storage: sto}

	// Format-tree stripping is vendor knowledge: load annotations once and
	// build a stripper per location (nil when the view keeps trees or the
	// location's vendors are unknown).
	var strippers map[string]*treeStripper
	if v.FormatTree != "keep" {
		vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
		if err != nil {
			return nil, err
		}
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
	for _, inc := range v.Include {
		if _, done := catalogs[inc.Location]; done {
			continue
		}
		if _, ok := ws.Location(inc.Location); !ok {
			return nil, fmt.Errorf("view %s: unknown location %q", v.Name, inc.Location)
		}
		entries, err := catalog.Load(ws.CatalogPath(inc.Location))
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("location %q has no catalog — run `mtunes scan %s` first", inc.Location, inc.Location)
		}
		catalogs[inc.Location] = entries
	}

	type picked struct {
		inc view.Include
		ce  catalog.Entry
	}
	var selection []picked
	seen := map[string]bool{} // location:path:as — the same source through two
	// includes with the same prefix is one output, not a collision
	for _, inc := range v.Include {
		cat := catalogs[inc.Location]
		paths := make([]string, 0, len(cat))
		for p := range cat {
			paths = append(paths, p)
		}
		sort.Strings(paths)
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
			key := inc.Location + "\x00" + sp + "\x00" + inc.As
			if seen[key] {
				continue
			}
			seen[key] = true
			selection = append(selection, picked{inc, cat[sp]})
		}
	}

	for _, pk := range selection {
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

		outCh, dualMono := OutputChannels(dev, ce)
		if dualMono {
			p.DualMonoFolded++
		}
		outFrames := int64(math.Round(float64(ce.Audio.Frames) *
			float64(dev.Audio.SampleRate) / float64(ce.Audio.SampleRate)))
		outBytes := ConvertedBytes(dev, ce)

		srcForOut := ce.Path
		if st := strippers[loc]; st != nil && !globCoversTree(pk.inc, st.vendorDirs) {
			if stripped, ok := st.strip(ce.Path); ok {
				srcForOut = stripped
				p.StrippedFormatTree++
			}
		}
		p.Entries = append(p.Entries, Entry{
			Location:    loc,
			SourcePath:  ce.Path,
			SHA256:      ce.SHA256,
			SourceSize:  ce.Size,
			OutPath:     outputPath(pk.inc, srcForOut),
			OutChannels: outCh,
			DualMono:    dualMono,
			OutRate:     dev.Audio.SampleRate,
			OutDepth:    dev.Audio.BitDepth,
			OutFrames:   outFrames,
			OutBytes:    outBytes,
			InChannels:  ce.Audio.Channels,
			InRate:      ce.Audio.SampleRate,
			InDepth:     ce.Audio.BitDepth,
			InFormat:    ce.Audio.Format,
			DurationS:   ce.Audio.DurationS,
		})
	}
	sort.Slice(p.Entries, func(i, j int) bool { return p.Entries[i].OutPath < p.Entries[j].OutPath })

	if v.Dedup == "content" {
		// Identical bytes render once — the first output path in sort order
		// wins, so the choice is deterministic and pins in the lock.
		seenSHA := map[string]bool{}
		kept := p.Entries[:0]
		for _, e := range p.Entries {
			if seenSHA[e.SHA256] {
				p.Deduped++
				continue
			}
			seenSHA[e.SHA256] = true
			kept = append(kept, e)
		}
		p.Entries = kept
	}

	if v.Limit > 0 && len(p.Entries) > v.Limit {
		p.LimitedFrom = len(p.Entries)
		p.Entries = p.Entries[:v.Limit]
	}

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

	p.checkCollisions()
	p.checkDisplay()
	p.checkNaming()
	p.checkFit()
	return p, nil
}

// treeStripper drops a vendor's format-tree level from catalog paths: the
// segment right after the pack dir when annotations say it is the
// canonical audio dir or a parallel export ("808 From Mars/WAV/Kicks/x" →
// "808 From Mars/Kicks/x", "ASMR/ASMR 24 bit stereo/y" → "ASMR/y"). Pack
// depth follows the location layout; the vendor comes from the location's
// slug, or from the top dir under vendor-dirs.
type treeStripper struct {
	vendorDirs bool
	fixed      *annotations.Vendor // single-vendor location
	vendors    []annotations.Vendor
	byTop      map[string]*annotations.Vendor // vendor-dirs: top dir → vendor (nil = unknown)
}

func newTreeStripper(lc workspace.LocationConfig, vendors []annotations.Vendor) *treeStripper {
	if len(vendors) == 0 {
		return nil
	}
	st := &treeStripper{vendorDirs: lc.Layout == "vendor-dirs", vendors: vendors, byTop: map[string]*annotations.Vendor{}}
	if !st.vendorDirs {
		st.fixed = annotations.BySlug(vendors)[lc.Vendor]
		if st.fixed == nil {
			return nil
		}
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

// strip returns the path without its format-tree segment, and whether one
// was removed.
func (st *treeStripper) strip(p string) (string, bool) {
	segs := strings.Split(p, "/")
	packIdx := 0 // index of the pack dir segment
	vendor := st.fixed
	if st.vendorDirs {
		if len(segs) < 4 { // vendor/pack/tree/file at minimum
			return p, false
		}
		packIdx = 1
		v, seen := st.byTop[segs[0]]
		if !seen {
			v = annotations.ByName(st.vendors, segs[0])
			st.byTop[segs[0]] = v
		}
		vendor = v
	} else if len(segs) < 3 { // pack/tree/file
		return p, false
	}
	if vendor == nil {
		return p, false
	}
	pack := vendor.PackByDir(segs[packIdx])
	if !vendor.IsFormatTree(pack, segs[packIdx+1]) {
		return p, false
	}
	out := append(append([]string{}, segs[:packIdx+1]...), segs[packIdx+2:]...)
	return strings.Join(out, "/"), true
}

// outputPath maps a source path to its output path: mirror by default, or
// the include's As prefix replacing the glob's static root. The extension
// always becomes .wav (that is what a transcode means).
func outputPath(inc view.Include, srcPath string) string {
	out := srcPath
	if inc.As != "" {
		root := view.GlobRoot(inc.Glob)
		out = strings.TrimSuffix(inc.As, "/") + "/" + strings.TrimPrefix(srcPath, root)
	}
	ext := path.Ext(out)
	return strings.TrimSuffix(out, ext) + ".wav"
}

// flatten rewrites every OutPath to a bare filename for devices with no
// folder concept. Names that collide are disambiguated by prepending just
// enough trailing parent directories ("KitA - Kick 01.wav"), one level at
// a time, only for the names that need it — already-unique names stay
// clean. Deterministic, so it pins in lockfiles like everything else.
// Anything still colliding afterwards falls through to checkCollisions
// and errors there.
func flatten(entries []Entry, caseSensitive bool) {
	segs := make([][]string, len(entries))
	depth := make([]int, len(entries))
	names := make([]string, len(entries))
	for i, e := range entries {
		dir, file := path.Split(e.OutPath)
		segs[i] = strings.FieldsFunc(dir, func(r rune) bool { return r == '/' })
		names[i] = file
	}
	fold := func(s string) string {
		if caseSensitive {
			return s
		}
		return strings.ToLower(s)
	}
	for range 64 { // bounded by deepest realistic tree
		groups := map[string][]int{}
		for i := range entries {
			groups[fold(names[i])] = append(groups[fold(names[i])], i)
		}
		progressed := false
		for _, idxs := range groups {
			if len(idxs) < 2 {
				continue
			}
			for _, i := range idxs {
				if depth[i] < len(segs[i]) {
					depth[i]++
					parents := segs[i][len(segs[i])-depth[i]:]
					_, base := path.Split(entries[i].OutPath)
					names[i] = strings.Join(parents, " - ") + " - " + base
					progressed = true
				}
			}
		}
		if !progressed {
			break
		}
	}
	for i := range entries {
		entries[i].OutPath = names[i]
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
