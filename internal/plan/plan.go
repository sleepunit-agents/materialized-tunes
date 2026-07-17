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

	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/view"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

const wavHeaderBytes = 44 // canonical PCM header; materialize uses -bitexact to keep this honest

// Entry is one source file that will materialize into one output file.
type Entry struct {
	Location   string
	SourcePath string
	SHA256     string
	SourceSize int64

	OutPath     string
	OutChannels int
	OutRate     int
	OutDepth    int
	OutFrames   int64
	OutBytes    int64

	InChannels int
	InRate     int
	InDepth    int
	InFormat   string
	DurationS  float64
}

type Skip struct {
	Location string
	Path     string
	Reason   string
}

type Collision struct {
	OutPath string
	Sources []string // "location:path"
}

type Plan struct {
	View    *view.View
	Device  *profile.Device
	Storage *profile.Storage

	Entries []Entry

	Matched          int // selected by includes before any exclusion
	ExcludedByGlob   int
	SkippedNonAudio  []Skip
	SkippedDuration  []Skip
	UnparseableAudio []Skip

	Collisions []Collision
	Errors     []string // any error ⇒ materialize refuses
	Warnings   []string

	TotalBytes   int64 // sum of exact output bytes
	TotalOnDisk  int64 // after cluster rounding (filesystem kind; else == TotalBytes)
	UsableBytes  int64
	Fits         bool
	SlotsUsed    int // quota kind
	SlotsAllowed int // quota kind, 0 = unlimited
}

// Build computes the plan for a view against the current catalogs.
func Build(ws *workspace.Workspace, viewName string) (*Plan, error) {
	v, err := view.Load(ws.Root, viewName)
	if err != nil {
		return nil, err
	}
	dev, err := profile.LoadDevice(ws.Root, v.Device)
	if err != nil {
		return nil, err
	}
	sto, err := profile.LoadStorage(ws.Root, v.Storage)
	if err != nil {
		return nil, err
	}

	p := &Plan{View: v, Device: dev, Storage: sto}

	catalogs := map[string]map[string]catalog.Entry{}
	for _, inc := range v.Include {
		if _, done := catalogs[inc.Location]; done {
			continue
		}
		if _, ok := ws.Location(inc.Location); !ok {
			return nil, fmt.Errorf("view %s: unknown location %q", viewName, inc.Location)
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
		switch {
		case ce.Audio == nil && ce.AudioErr != "":
			p.UnparseableAudio = append(p.UnparseableAudio, Skip{loc, ce.Path, ce.AudioErr})
			continue
		case ce.Audio == nil:
			p.SkippedNonAudio = append(p.SkippedNonAudio, Skip{loc, ce.Path, "not an audio file"})
			continue
		}
		if ce.Audio.Channels > 2 {
			p.UnparseableAudio = append(p.UnparseableAudio,
				Skip{loc, ce.Path, fmt.Sprintf("%d channels — samplers speak mono/stereo", ce.Audio.Channels)})
			continue
		}
		if max := dev.Audio.MaxDurationSeconds; max > 0 && ce.Audio.DurationS > max+1e-9 {
			p.SkippedDuration = append(p.SkippedDuration,
				Skip{loc, ce.Path, fmt.Sprintf("%.1fs > %.1fs limit", ce.Audio.DurationS, max)})
			continue
		}

		outCh := ce.Audio.Channels
		if dev.Audio.Channels == "mono" {
			outCh = 1
		}
		outFrames := int64(math.Round(float64(ce.Audio.Frames) *
			float64(dev.Audio.SampleRate) / float64(ce.Audio.SampleRate)))
		dataBytes := outFrames * int64(outCh) * int64(dev.Audio.BitDepth) / 8
		outBytes := wavHeaderBytes + dataBytes + dataBytes%2

		p.Entries = append(p.Entries, Entry{
			Location:    loc,
			SourcePath:  ce.Path,
			SHA256:      ce.SHA256,
			SourceSize:  ce.Size,
			OutPath:     outputPath(pk.inc, ce.Path),
			OutChannels: outCh,
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

	p.checkCollisions()
	p.checkNaming()
	p.checkFit()
	return p, nil
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
