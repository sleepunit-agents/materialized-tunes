package plan

import (
	"fmt"
	"path"
	"strings"
)

// Some vendors do not ship one library — they ship it once per sampler.
// Samples From Mars renders every pack again under "Battery/", "Maschine/",
// "MPC Live & X/", "Kontakt/", "Ableton Live/", each with the host's own
// patches beside the audio and the audio itself re-rendered and re-trimmed
// for that host. That is the vendor doing, ahead of time and for machines
// you may not own, exactly the job mtunes exists to do: prepare a library
// for a device. Their prep is not content. It is the same recordings in a
// folder shape nobody asked for, eight times over.
//
// So a re-export vendor's parallel trees are skipped outright rather than
// deduped against the canonical one. Deduping them is the cut resolver's
// answer (cuts.go), and it is the right answer for a vendor whose parallel
// trees really are cuts of one render — Polyend's 16-bit mono is content
// somebody's tracker wants. It is the wrong answer here: it costs a
// per-file adjudication to arrive at "keep the canonical one" for a set
// that was never a choice, and it leaves the survivor's provenance up to
// scoring luck.
//
// Two things hold this honest:
//
//   - It is scoped to the vendor's own declaration. Only [formats]
//     parallel_role = "reexport" reaches here.
//   - A tree is only skipped where the pack's canonical tree is actually
//     present to replace it. A pack that ships nothing but a Battery tree
//     — or one whose WAV tree the recipe's globs never selected — keeps
//     what it has. Skipping is a swap, never a subtraction.
//
// What it cannot prove is that every hit in a sampler tree also exists in
// the canonical one. Nothing cheap can: the trees are named differently
// ("Assorted 1 Samples" beside "01. Clean"), the bytes all differ by
// construction, and durations drift by frames. So it does not claim to —
// it says out loud how many skipped names have no same-named file under
// the canonical tree, and names a few. Zero is the answer that means the
// swap was clean, and the reader gets to see it rather than take it.

// skipVendorPrep drops files under a re-export vendor's per-sampler trees,
// per pack, where that pack's canonical tree survives to replace them.
func (p *Plan) skipVendorPrep() {
	// Group by pack: a tree only ever replaces another tree of its pack.
	byPack := map[string][]int{}
	var order []string
	for i, e := range p.Entries {
		if e.tree == "" || e.pack == "" {
			continue
		}
		k := e.Location + "\x00" + e.pack
		if _, seen := byPack[k]; !seen {
			order = append(order, k)
		}
		byPack[k] = append(byPack[k], i)
	}

	drop := map[int]bool{}
	skipped := map[string]int{} // sampler tree → files skipped
	var orphans []string        // skipped names with no canonical counterpart
	orphanCount := 0
	for _, k := range order {
		idx := byPack[k]
		canonical := map[string]bool{} // stem → present under the canonical tree
		prep := idx[:0:0]
		for _, i := range idx {
			e := p.Entries[i]
			switch {
			case e.treeRank == 0:
				canonical[stem(e.SourcePath)] = true
			case e.reexport:
				prep = append(prep, i)
			}
		}
		if len(canonical) == 0 || len(prep) == 0 {
			continue // nothing to swap in, or nothing to swap out
		}
		for _, i := range prep {
			drop[i] = true
			skipped[p.Entries[i].tree]++
			if !canonical[stem(p.Entries[i].SourcePath)] {
				orphanCount++
				if len(orphans) < 3 {
					orphans = append(orphans, p.Entries[i].SourcePath)
				}
			}
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
	p.VendorPrepSkipped = len(drop)

	unmatched := " Every skipped name has a same-named file under the canonical tree."
	if orphanCount > 0 {
		unmatched = fmt.Sprintf(" %d skipped %s no same-named file under the canonical tree (e.g. %s) — check before trusting the swap.",
			orphanCount, plural(orphanCount, "name has", "names have"), strings.Join(orphans, ", "))
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf(
		"%d %s skipped from the vendor's own sampler exports — %s hold the same library re-rendered for a host, and this device is prepared here instead.%s vendor_prep = \"keep\" renders them.",
		p.VendorPrepSkipped, plural(p.VendorPrepSkipped, "file", "files"),
		treeTally(skipped), unmatched))
}

// stem is a filename reduced to what survives a re-render: the base name
// without its extension, case-folded. A sampler tree's copy of a hit keeps
// the vendor's name for it and changes the container, the trim and every
// byte — the name is the only thing left to match on.
func stem(p string) string {
	base := path.Base(p)
	return strings.ToLower(strings.TrimSuffix(base, path.Ext(base)))
}
