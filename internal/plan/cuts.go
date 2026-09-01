package plan

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// A pack that ships its library several ways sends every cut to the same
// output path once the format-tree level is stripped. Polyend is the clear
// case: every Palette pack holds one set of one-shots three times, under
// "<Pack> 24 bit stereo", "<Pack> 16 bit stereo" and "<Pack> 16 bit mono".
// That is not a collision between different samples — it is one sample the
// vendor cut for different machines, and picking among them is a decision
// the recipe should never have to spell out by hand.
//
// pickCuts makes it from what the device can actually take. Every cut is
// scored by what comes out the other side, not by what went in: the
// channels, rate and depth actually delivered, capped at the source's own
// (upsampling invents nothing). Most delivered wins; among cuts that
// deliver the same thing, the one needing no transcode wins, then the
// vendor's own tree order (canonical first). So a 24-bit stereo DAW
// library keeps the 24-bit stereo cut, and a 16-bit mono tracker keeps the
// 16-bit mono cut the vendor made for it — byte-for-byte, no ffmpeg.

// cutDelivered is what one cut actually arrives as on this device.
// Delivered rate and depth are capped at the source's: a 16-bit file
// rendered into a 24-bit container still carries 16 bits of record.
func cutDelivered(e Entry) (channels, rate, depth int, passthrough bool) {
	return e.OutChannels, min(e.InRate, e.OutRate), min(e.InDepth, e.OutDepth), e.Copy
}

// betterCut reports whether a delivers more of the recording than b, or
// delivers the same for less work. Total and deterministic — the final
// tiebreak is the source path, so the choice pins in a lockfile.
//
// Length comes first: when a vendor's renders of one hit disagree —
// re-export trees trimmed independently, or cuts the vendor rendered in
// separate passes (Polyend Thump) — the longer one is the safe keep: it
// is the one that cannot be missing a tail the other has. Cuts of a
// single render are the same length and fall through to quality.
func betterCut(a, b Entry) bool {
	ach, art, adp, acp := cutDelivered(a)
	bch, brt, bdp, bcp := cutDelivered(b)
	switch {
	case a.DurationS-b.DurationS > 1e-3 || b.DurationS-a.DurationS > 1e-3:
		return a.DurationS > b.DurationS
	case ach != bch:
		return ach > bch
	case art != brt:
		return art > brt
	case adp != bdp:
		return adp > bdp
	case acp != bcp:
		return acp // no transcode beats a transcode that changes nothing
	case a.treeRank != b.treeRank:
		return a.treeRank < b.treeRank // the vendor's own order, canonical first
	}
	return a.SourcePath < b.SourcePath
}

// isCutSet reports whether entries at idx are the same sample in different
// cuts rather than genuinely different files that happen to collide. The
// proof is structural, in two halves: each member comes out of a different
// format tree of the same pack — trees the vendor itself declared parallel,
// or that the pack's own dir naming does (see treeWords) — and each sits at
// the same relative path inside its tree. The relpath is the sample's
// coordinate; case and extension are not part of it (a re-render into
// another container is still the same recording). Two different files that
// merely land on one output path share neither, and stay a collision.
//
// Length is deliberately not part of the proof. It used to be, for cut
// vendors — one render delivered at several bit depths is the same length
// in every tree — but Polyend's Thump ships its three trees as three
// separately rendered zips whose durations drift, and Samples From Mars
// re-renders per sampler with independent trims. Where the structure says
// "same sample", a length that disagrees means the vendor trimmed or
// re-rendered, not that the files are strangers: betterCut leads with
// length, so the longest render is kept and pickCuts says out loud how
// many dropped cuts disagreed.
func isCutSet(entries []Entry, idx []int) bool {
	trees := make(map[string]bool, len(idx))
	r0 := cutRel(entries[idx[0]])
	for _, i := range idx {
		e := entries[i]
		if e.tree == "" || trees[e.tree] || cutRel(e) != r0 {
			return false
		}
		trees[e.tree] = true
	}
	return true
}

// cutRel is the sample's coordinate inside its format tree: the relative
// path with case and extension folded away — a re-render into another
// container or case is still the same recording.
func cutRel(e Entry) string {
	r := strings.TrimPrefix(e.SourcePath, e.pack+"/"+e.tree+"/")
	return strings.ToLower(strings.TrimSuffix(r, path.Ext(r)))
}

// pickCuts drops the redundant cuts, keeping one entry per output path.
// Runs on entries whose paths are final apart from the device's own
// renaming passes, so its key is the same one checkCollisions uses — what
// it leaves behind, checkCollisions still judges.
func (p *Plan) pickCuts(caseSensitive bool) {
	byOut := map[string][]int{}
	var order []string
	for i, e := range p.Entries {
		if e.tree == "" {
			continue
		}
		k := e.OutPath
		if !caseSensitive {
			k = strings.ToLower(k)
		}
		if _, seen := byOut[k]; !seen {
			order = append(order, k)
		}
		byOut[k] = append(byOut[k], i)
	}

	drop := map[int]bool{}
	kept := map[string]int{}    // format tree → samples kept from it
	dropped := map[string]int{} // format tree → cuts dropped
	sets := 0                   // samples that shipped in more than one cut
	trimmed := 0                // re-export sets whose renders disagreed on length
	var example string
	for _, k := range order {
		idx := byOut[k]
		if len(idx) < 2 || !isCutSet(p.Entries, idx) {
			continue
		}
		sets++
		best := idx[0]
		for _, i := range idx[1:] {
			if betterCut(p.Entries[i], p.Entries[best]) {
				best = i
			}
			if d := p.Entries[i].DurationS - p.Entries[idx[0]].DurationS; d > 1e-3 || d < -1e-3 {
				trimmed++
			}
		}
		kept[p.Entries[best].tree]++
		for _, i := range idx {
			if i != best {
				drop[i] = true
				dropped[p.Entries[i].tree]++
			}
		}
		p.CutsDropped += len(idx) - 1
		if example == "" {
			example = fmt.Sprintf("%s kept from %q", p.Entries[best].OutPath, p.Entries[best].tree)
		}
	}
	if len(drop) > 0 {
		surviving := p.Entries[:0]
		for i, e := range p.Entries {
			if !drop[i] {
				surviving = append(surviving, e)
			}
		}
		p.Entries = surviving

		trim := ""
		if trimmed > 0 {
			trim = fmt.Sprintf(" %d dropped %s trimmed to a different length than its siblings; the longest render was kept.",
				trimmed, plural(trimmed, "cut was", "cuts were"))
		}
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%d redundant format %s dropped — %d %s shipped in more than one cut, rendered once in the cut this device takes best: kept %s, dropped %s (e.g. %s).%s cuts = \"all\" keeps every cut.",
			p.CutsDropped, plural(p.CutsDropped, "cut", "cuts"),
			sets, plural(sets, "sample", "samples"),
			treeTally(kept), treeTally(dropped), example, trim))
	}

	p.splitCuts(caseSensitive)
}

// splitCuts finds the cut siblings pickCuts could never adjudicate: cuts
// of one sample whose trees disagree about its folder or its name, so each
// lands on an output path of its own and every one of them ships. The
// resolver groups by output path — landing together is its trigger — so a
// mono tree that files its toms under "Toms" while the stereo trees say
// "Tom" sails straight past it and the device gets the sample twice, in
// two folders, with no collision to say so. The grouping here is looser on
// purpose: same pack, same filename, one member per tree — and it only
// speaks when those members are headed to more than one output path.
// A name that repeats inside a single tree ("Kit A/Kick", "Kit B/Kick")
// disqualifies its group: that is two samples sharing a name, not one
// sample in cuts.
func (p *Plan) splitCuts(caseSensitive bool) {
	type group struct {
		idx     []int
		trees   map[string]bool
		treeDup bool
	}
	byName := map[string]*group{}
	var order []string
	for i, e := range p.Entries {
		if e.tree == "" || e.Companion {
			continue
		}
		base := path.Base(e.SourcePath)
		k := e.pack + "\x00" + strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
		g := byName[k]
		if g == nil {
			g = &group{trees: map[string]bool{}}
			byName[k] = g
			order = append(order, k)
		}
		if g.trees[e.tree] {
			g.treeDup = true
		}
		g.trees[e.tree] = true
		g.idx = append(g.idx, i)
	}
	var example string
	for _, k := range order {
		g := byName[k]
		if len(g.idx) < 2 || g.treeDup {
			continue
		}
		fold := func(o string) string {
			if caseSensitive {
				return o
			}
			return strings.ToLower(o)
		}
		o0 := fold(p.Entries[g.idx[0]].OutPath)
		split := false
		for _, i := range g.idx[1:] {
			if fold(p.Entries[i].OutPath) != o0 {
				split = true
				if example == "" {
					a, b := p.Entries[g.idx[0]], p.Entries[i]
					example = fmt.Sprintf("%q lands at %s while %q lands at %s",
						a.SourcePath, a.OutPath, b.SourcePath, b.OutPath)
				}
				break
			}
		}
		if split {
			p.CutsSplit++
		}
	}
	if p.CutsSplit == 0 {
		return
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf(
		"%d %s in more than one cut anyway — their format trees disagree about the file's folder or name, so the cuts land on different output paths and the redundancy is invisible to the cut resolver: %s. The pack's [[dir]] annotation can teach the trees to agree.",
		p.CutsSplit, plural(p.CutsSplit, "sample still ships", "samples still ship"), example))
}

// treeTally names format trees with their counts, busiest first — the
// answer to "which cut did I actually get".
func treeTally(n map[string]int) string {
	trees := make([]string, 0, len(n))
	for t := range n {
		trees = append(trees, t)
	}
	sort.Slice(trees, func(i, j int) bool {
		if n[trees[i]] != n[trees[j]] {
			return n[trees[i]] > n[trees[j]]
		}
		return trees[i] < trees[j]
	})
	parts := make([]string, len(trees))
	for i, t := range trees {
		parts[i] = fmt.Sprintf("%q (%d)", t, n[t])
	}
	return strings.Join(parts, ", ")
}
