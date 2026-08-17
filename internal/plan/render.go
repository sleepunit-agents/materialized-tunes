package plan

import (
	"fmt"
	"io"
)

// Render prints the human pre-flight report.
func (p *Plan) Render(w io.Writer, verbose bool) {
	d, s := p.Device, p.Storage
	ch := "stereo-preserving"
	if d.Audio.Channels == "mono" {
		ch = "mono (" + d.Audio.Downmix + ")"
	}
	fmt.Fprintf(w, "view %s → %s (%d-bit/%dHz %s wav) on %s\n\n",
		p.View.Name, d.Name, d.Audio.BitDepth, d.Audio.SampleRate, ch, s.Name)

	fmt.Fprintf(w, "  %d files selected", len(p.Entries))
	if p.LimitedFrom > 0 {
		fmt.Fprintf(w, "   (limit %d of %d eligible)", len(p.Entries), p.LimitedFrom)
	}
	if p.ExcludedByGlob > 0 {
		fmt.Fprintf(w, "   (%d excluded by pattern)", p.ExcludedByGlob)
	}
	fmt.Fprintln(w)

	switch s.Kind {
	case "filesystem":
		fmt.Fprintf(w, "  %s post-transform  (%s after %s clusters)\n",
			HumanBytes(p.TotalBytes), HumanBytes(p.TotalOnDisk), HumanBytes(s.ClusterBytes))
		if p.Fits {
			fmt.Fprintf(w, "  fits: yes — %s to spare (%s usable after reserve)\n",
				HumanBytes(p.UsableBytes-p.TotalOnDisk), HumanBytes(p.UsableBytes))
		} else {
			fmt.Fprintf(w, "  fits: NO — %s over (%s usable after reserve)\n",
				HumanBytes(p.TotalOnDisk-p.UsableBytes), HumanBytes(p.UsableBytes))
		}
	case "quota":
		slots := fmt.Sprintf("%d", p.SlotsUsed)
		if p.SlotsAllowed > 0 {
			slots = fmt.Sprintf("%d/%d", p.SlotsUsed, p.SlotsAllowed)
		}
		fmt.Fprintf(w, "  %s slots, %s / %s\n", slots, HumanBytes(p.TotalBytes), HumanBytes(s.CapacityBytes))
	}

	if n := len(p.SkippedDuration); n > 0 {
		fmt.Fprintf(w, "\n  ℹ %d %s excluded: exceed max duration %.1fs\n", n, plural(n, "source", "sources"), d.Audio.MaxDurationSeconds)
		for _, sk := range p.SkippedDuration {
			fmt.Fprintf(w, "      %s:%s (%s)\n", sk.Location, sk.Path, sk.Reason)
		}
	}
	if n := p.Deduped; n > 0 {
		fmt.Fprintf(w, "  ℹ %d duplicate sources (identical bytes) render once — dedup = \"content\"\n", n)
	}
	if n := p.DualMonoFolded; n > 0 {
		fmt.Fprintf(w, "  ℹ %d dual-mono sources (L≡R) render as one channel, no −3 dB pad\n", n)
	}
	if n := p.Renamed; n > 0 {
		fmt.Fprintf(w, "  ℹ %d names rewritten distinguishing-first for the %d-char display\n", n, d.Naming.DisplayLength)
	}
	if n := p.StrippedFormatTree; n > 0 {
		fmt.Fprintf(w, "  ℹ %d outputs lose their vendor format-tree level (WAV/, * 24 bit stereo/ …); format_tree = \"keep\" to mirror\n", n)
	}
	if n := len(p.SkippedNonAudio); n > 0 {
		fmt.Fprintf(w, "  ℹ %d non-audio files skipped\n", n)
		if verbose {
			for _, sk := range p.SkippedNonAudio {
				fmt.Fprintf(w, "      %s:%s\n", sk.Location, sk.Path)
			}
		}
	}
	if n := len(p.UnparseableAudio); n > 0 {
		fmt.Fprintf(w, "  ⚠ %d audio files skipped (unparseable headers)\n", n)
		for _, sk := range p.UnparseableAudio {
			fmt.Fprintf(w, "      %s:%s — %s\n", sk.Location, sk.Path, sk.Reason)
		}
	}
	for _, warn := range p.Warnings {
		fmt.Fprintf(w, "  ⚠ %s\n", warn)
	}
	for _, e := range p.Errors {
		fmt.Fprintf(w, "  ✗ %s\n", e)
	}

	if verbose && len(p.Entries) > 0 {
		fmt.Fprintln(w)
		for _, e := range p.Entries {
			fmt.Fprintf(w, "  %-50s %9s  ← %s:%s (%s %dch/%d/%d)\n",
				e.OutPath, HumanBytes(e.OutBytes), e.Location, e.SourcePath,
				e.InFormat, e.InChannels, e.InRate, e.InDepth)
		}
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
