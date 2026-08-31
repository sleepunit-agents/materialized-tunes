package plan

import (
	"fmt"
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
func betterCut(a, b Entry) bool {
	ach, art, adp, acp := cutDelivered(a)
	bch, brt, bdp, bcp := cutDelivered(b)
	switch {
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
// proof is twofold: each comes out of a different format tree of the same
// pack, and they are all the same length. Two files of different durations
// under one name are a real collision and stay one — pickCuts refuses to
// silently drop audio it cannot show is redundant.
func isCutSet(entries []Entry, idx []int) bool {
	trees := make(map[string]bool, len(idx))
	d0 := entries[idx[0]].DurationS
	for _, i := range idx {
		e := entries[i]
		if e.tree == "" || trees[e.tree] {
			return false
		}
		trees[e.tree] = true
		if diff := e.DurationS - d0; diff > 1e-3 || diff < -1e-3 {
			return false
		}
	}
	return true
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
	var example string
	for _, k := range order {
		idx := byOut[k]
		if len(idx) < 2 || !isCutSet(p.Entries, idx) {
			continue
		}
		best := idx[0]
		for _, i := range idx[1:] {
			if betterCut(p.Entries[i], p.Entries[best]) {
				best = i
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
	if len(drop) == 0 {
		return
	}

	surviving := p.Entries[:0]
	for i, e := range p.Entries {
		if !drop[i] {
			surviving = append(surviving, e)
		}
	}
	p.Entries = surviving

	p.Warnings = append(p.Warnings, fmt.Sprintf(
		"%d redundant format %s dropped — %d %s ship in more than one cut and render once, in the cut this device takes best: kept %s, dropped %s (e.g. %s). cuts = \"all\" keeps every cut.",
		p.CutsDropped, plural(p.CutsDropped, "cut", "cuts"),
		len(kept), plural(len(kept), "sample", "samples"),
		treeTally(kept), treeTally(dropped), example))
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
