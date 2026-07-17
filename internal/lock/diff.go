package lock

import (
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/transcode"
)

// Diff is the staleness surface: what a re-run of the recipe today would
// do differently than the locked materialization.
type Diff struct {
	Added        []string // location:path — selected now, absent from lock
	Deselected   []string // in lock, source still exists, recipe no longer picks it
	GoneFromSrc  []string // in lock, source no longer in the catalog at all
	ContentDrift []string // same source path, different sha (source was touched)
	NewTransform []string // same source, same bytes, different ffmpeg args (profile changed)
}

func (d *Diff) Clean() bool {
	return len(d.Added)+len(d.Deselected)+len(d.GoneFromSrc)+
		len(d.ContentDrift)+len(d.NewTransform) == 0
}

// Compute compares a lock against a current plan for the same view.
// catalogSHAs maps location -> path -> current sha, used to tell
// "deselected" apart from "gone from source".
func Compute(l *Lock, p *plan.Plan, catalogSHAs map[string]map[string]string) *Diff {
	d := &Diff{}

	type current struct {
		sha  string
		args []string
	}
	inPlan := map[string]current{}
	for _, e := range p.Entries {
		key := e.Location + "\x00" + e.SourcePath
		inPlan[key] = current{sha: e.SHA256, args: planArgs(p, e)}
	}
	inLock := map[string]Entry{}
	for _, e := range l.Entries {
		inLock[e.Source.Location+"\x00"+e.Source.Path] = e
	}

	for _, e := range p.Entries {
		key := e.Location + "\x00" + e.SourcePath
		if _, ok := inLock[key]; !ok {
			d.Added = append(d.Added, e.Location+":"+e.SourcePath)
		}
	}
	for _, e := range l.Entries {
		key := e.Source.Location + "\x00" + e.Source.Path
		name := e.Source.Location + ":" + e.Source.Path
		cur, selected := inPlan[key]
		if !selected {
			if sha, exists := catalogSHAs[e.Source.Location][e.Source.Path]; !exists {
				d.GoneFromSrc = append(d.GoneFromSrc, name)
			} else if sha != e.Source.SHA256 {
				// still on disk but modified AND no longer selected —
				// content drift is the more important signal
				d.ContentDrift = append(d.ContentDrift, name)
			} else {
				d.Deselected = append(d.Deselected, name)
			}
			continue
		}
		switch {
		case cur.sha != e.Source.SHA256:
			d.ContentDrift = append(d.ContentDrift, name)
		case !equalArgs(cur.args, e.Transform.FFmpegArgs):
			d.NewTransform = append(d.NewTransform, name)
		}
	}
	return d
}

// planArgs rebuilds the ffmpeg args a plan entry would use today.
func planArgs(p *plan.Plan, e plan.Entry) []string {
	return transcode.BuildArgs(e.InChannels, p.Device.Audio.Channels,
		p.Device.Audio.Downmix, e.InRate, e.OutRate, e.OutDepth)
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
