package plan

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
)

// A Dump is the whole decision surface at once, for reading offline: every
// source folder the queues would show (acked folders included, marked),
// every file in it, and the why per facet. The queues endpoint is a picker
// — three hundred rows, four example names — because a human decides one
// folder at a time; the dump is the same grouping with nothing left out,
// so whoever maintains the lexicon can read every silence in one sitting
// instead of being handed folders one by one (SPEC §19.2, Jonathan
// 2026-09-02: "if you had the full list of what isn't being matched and
// why right now, you could hit a lot of these faster than me").
type Dump struct {
	View        string         `json:"view"`
	Built       time.Time      `json:"built"`
	App         string         `json:"app,omitempty"`
	Annotations string         `json:"annotations,omitempty"` // checkout head, when known
	Kinds       map[string]int `json:"kinds"`                 // files per placement failure, samples only
	Files       int            `json:"files"`                 // files needing a decision
	Folders     []DumpFolder   `json:"folders"`               // biggest first, then by path
}

// DumpFolder is one queue row with every file it holds.
type DumpFolder struct {
	Location   string         `json:"location"`
	Folder     string         `json:"folder"`
	PackPath   string         `json:"pack_path,omitempty"`
	Kind       string         `json:"kind"` // the folder's most common failure
	Kinds      map[string]int `json:"kinds"`
	Count      int            `json:"count"`
	Category   string         `json:"category,omitempty"` // the majority answer, as the queue shows it
	Instrument string         `json:"instrument,omitempty"`
	Family     string         `json:"family,omitempty"`
	Why        *harvest.Why   `json:"why,omitempty"` // of the first file carrying the majority answer
	Acked      bool           `json:"acked,omitempty"`
	Files      []DumpFile     `json:"files"`
}

// DumpFile is one file's own record — the queue's example names, all of
// them, each with the facets and why the row summarised.
type DumpFile struct {
	Name       string       `json:"name"`
	OutPath    string       `json:"out_path"`
	Kind       string       `json:"kind"`
	Category   string       `json:"category,omitempty"`
	Instrument string       `json:"instrument,omitempty"`
	Family     string       `json:"family,omitempty"`
	Why        *harvest.Why `json:"why,omitempty"`
}

// BuildDump groups the plan's placement failures exactly as the queues
// do — by source folder, companions left out, majority facets per folder,
// the row's why from the first file that carries the majority — and keeps
// every file. meta answers per location, acks are keyed location\x00folder
// as correct.Acks returns them.
func BuildDump(p *Plan, meta func(location string) map[string]harvest.Meta, acks map[string]bool) *Dump {
	d := &Dump{Kinds: map[string]int{}}
	if p == nil {
		return d
	}
	d.View = p.View.Name
	byKey := map[string]*DumpFolder{}
	type tally struct{ cats, insts, fams map[string]int }
	tallies := map[string]*tally{}
	for _, e := range p.Entries {
		if e.Kind == "" || e.Companion {
			continue
		}
		d.Kinds[e.Kind]++
		d.Files++
		folder := path.Dir(e.SourcePath)
		key := e.Location + "\x00" + folder
		f := byKey[key]
		if f == nil {
			f = &DumpFolder{Location: e.Location, Folder: folder, PackPath: e.PackPath, Kinds: map[string]int{}, Acked: acks[key]}
			byKey[key] = f
			tallies[key] = &tally{map[string]int{}, map[string]int{}, map[string]int{}}
		}
		m := meta(e.Location)[e.SourcePath]
		f.Count++
		f.Kinds[e.Kind]++
		t := tallies[key]
		t.cats[m.Category]++
		t.insts[m.Instrument]++
		t.fams[m.Family]++
		f.Files = append(f.Files, DumpFile{Name: path.Base(e.SourcePath), OutPath: e.OutPath, Kind: e.Kind,
			Category: m.Category, Instrument: m.Instrument, Family: m.Family, Why: m.Why})
	}
	top := func(m map[string]int) string {
		best, bestN := "", -1
		for k, n := range m {
			if n > bestN || (n == bestN && k < best) {
				best, bestN = k, n
			}
		}
		return best
	}
	for key, f := range byKey {
		t := tallies[key]
		f.Kind = top(f.Kinds)
		f.Category, f.Instrument, f.Family = top(t.cats), top(t.insts), top(t.fams)
		sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].Name < f.Files[j].Name })
		for _, df := range f.Files {
			if df.Category == f.Category && df.Instrument == f.Instrument {
				f.Why = df.Why
				break
			}
		}
		d.Folders = append(d.Folders, *f)
	}
	sort.Slice(d.Folders, func(i, j int) bool {
		a, b := d.Folders[i], d.Folders[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Location != b.Location {
			return a.Location < b.Location
		}
		return a.Folder < b.Folder
	})
	return d
}

// kindOrder is the queues' chip order: the fastest question first.
var kindOrder = []string{"uncategorized", "unsorted", "general"}

func kindsLine(kinds map[string]int) string {
	var parts []string
	for _, k := range kindOrder {
		if n := kinds[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return strings.Join(parts, " · ")
}

func describe(s *annotations.Source) string {
	if s == nil {
		return "nothing spoke"
	}
	return s.Describe()
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// WriteText renders the dump for a person: a header, then one block per
// folder — the folder, the majority answer with its why, every file with
// its own category · instrument and, where its why differs from the
// folder's, its own why. Greppable: folder blocks start with "## ",
// files with two spaces.
func (d *Dump) WriteText(w io.Writer) error {
	bw := &errWriter{w: w}
	built := ""
	if !d.Built.IsZero() {
		built = " · built " + d.Built.Local().Format("2006-01-02 15:04")
	}
	app := ""
	if d.App != "" {
		app = " · app " + d.App
	}
	ann := ""
	if d.Annotations != "" {
		ann = " · annotations " + short(d.Annotations)
	}
	bw.printf("# materialized-tunes plan dump · view %q%s%s%s\n", d.View, built, app, ann)
	bw.printf("# %d %s · %d %s need a decision: %s (acked folders included, marked)\n", len(d.Folders), plural(len(d.Folders), "folder", "folders"), d.Files, plural(d.Files, "file", "files"), dash(kindsLine(d.Kinds)))
	bw.printf("# One block per source folder, biggest first: the folder, the majority answer and its why, then every\n")
	bw.printf("# file with its own category · instrument (— = nothing spoke) and, where it differs from the folder's,\n")
	bw.printf("# its own why. Folder paths are relative to the location root; the arrow is where the file lands.\n")
	for _, f := range d.Folders {
		var rc, ri *annotations.Source
		if f.Why != nil {
			rc, ri = f.Why.Category, f.Why.Instrument
		}
		bw.printf("\n## %s · %s: %s · %d %s\n", f.Kind, f.Location, f.Folder, f.Count, plural(f.Count, "file", "files"))
		bw.printf("   pack        %s\n", dash(f.PackPath))
		bw.printf("   kinds       %s\n", kindsLine(f.Kinds))
		bw.printf("   category    %-14s %s\n", dash(f.Category), describe(rc))
		fam := dash(f.Instrument)
		if f.Family != "" && f.Family != f.Instrument {
			fam += " (" + f.Family + ")"
		}
		bw.printf("   instrument  %-14s %s\n", fam, describe(ri))
		if f.Acked {
			bw.printf("   acked       reviewed and left as-is\n")
		}
		for _, df := range f.Files {
			var fc, fi *annotations.Source
			if df.Why != nil {
				fc, fi = df.Why.Category, df.Why.Instrument
			}
			line := fmt.Sprintf("  %s  %s · %s", df.Name, dash(df.Category), dash(df.Instrument))
			if df.Kind != f.Kind {
				line += " [" + df.Kind + "]"
			}
			var own []string
			if df.Category != f.Category || describe(fc) != describe(rc) {
				own = append(own, "category: "+describe(fc))
			}
			if df.Instrument != f.Instrument || describe(fi) != describe(ri) {
				own = append(own, "instrument: "+describe(fi))
			}
			if len(own) > 0 {
				line += "  ← " + strings.Join(own, "; ")
			}
			bw.printf("%s  → %s\n", line, df.OutPath)
		}
	}
	return bw.err
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
