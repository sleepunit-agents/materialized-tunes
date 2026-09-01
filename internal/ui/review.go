package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/correct"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/version"
)

// The plan is the review surface (SPEC §19.2): two ways into one
// artifact — queues of placement failures grouped by source folder, and
// the destination tree as it will be written — sharing a why per file,
// and one action vocabulary that writes the local annotation layer.

// artifactFor is the last plan built for a view, entries and all.
func (s *Server) artifactFor(viewName string) (*planArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.plans[viewName]
	if a == nil || a.Plan == nil {
		return nil, fmt.Errorf("no plan built for %q yet — the Plan step builds it", viewName)
	}
	return a, nil
}

type fileRow struct {
	Name       string       `json:"name"`
	Location   string       `json:"location"`
	SourcePath string       `json:"source_path"`
	OutPath    string       `json:"out_path"`
	PackPath   string       `json:"pack_path,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	DurationS  float64      `json:"duration_s,omitempty"`
	Category   string       `json:"category,omitempty"`
	Instrument string       `json:"instrument,omitempty"`
	Family     string       `json:"family,omitempty"`
	Why        *harvest.Why `json:"why,omitempty"`
}

func (s *Server) fileRow(e plan.Entry) fileRow {
	m := s.inputs.Meta(e.Location)[e.SourcePath]
	return fileRow{Name: path.Base(e.SourcePath), Location: e.Location, SourcePath: e.SourcePath, OutPath: e.OutPath,
		PackPath: e.PackPath, Kind: e.Kind, DurationS: e.DurationS, Category: m.Category, Instrument: m.Instrument, Family: m.Family, Why: m.Why}
}

// queues groups the plan's placement failures by source folder, biggest
// first: one decision per folder, never per file.
func (s *Server) queues(w http.ResponseWriter, r *http.Request) {
	a, err := s.artifactFor(r.URL.Query().Get("view"))
	if err != nil {
		jsonErr(w, 409, err)
		return
	}
	kindFilter := r.URL.Query().Get("kind")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 300
	}
	type row struct {
		Location   string         `json:"location"`
		Folder     string         `json:"folder"`
		PackPath   string         `json:"pack_path"`
		Kind       string         `json:"kind"`
		Kinds      map[string]int `json:"kinds"`
		Count      int            `json:"count"`
		Examples   []string       `json:"examples"`
		Category   string         `json:"category,omitempty"`
		Instrument string         `json:"instrument,omitempty"`
		Family     string         `json:"family,omitempty"`
		Why        *harvest.Why   `json:"why,omitempty"`
		Acked      bool           `json:"acked,omitempty"`

		cats, insts, fams map[string]int
	}
	rows := map[string]*row{}
	acks := correct.Acks(s.ws)
	kindTotals := map[string]int{}
	for _, e := range a.Plan.Entries {
		if e.Kind == "" {
			continue
		}
		kindTotals[e.Kind]++
		folder := path.Dir(e.SourcePath)
		key := e.Location + "\x00" + folder
		rw := rows[key]
		if rw == nil {
			rw = &row{Location: e.Location, Folder: folder, PackPath: e.PackPath, Kinds: map[string]int{},
				cats: map[string]int{}, insts: map[string]int{}, fams: map[string]int{}, Acked: acks[key]}
			rows[key] = rw
		}
		rw.Count++
		rw.Kinds[e.Kind]++
		if len(rw.Examples) < 4 {
			rw.Examples = append(rw.Examples, path.Base(e.SourcePath))
		}
		m := s.inputs.Meta(e.Location)[e.SourcePath]
		rw.cats[m.Category]++
		rw.insts[m.Instrument]++
		rw.fams[m.Family]++
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
	out := []*row{}
	for _, rw := range rows {
		rw.Kind = top(rw.Kinds)
		rw.Category, rw.Instrument, rw.Family = top(rw.cats), top(rw.insts), top(rw.fams)
		if kindFilter != "" && rw.Kind != kindFilter {
			continue
		}
		if rw.Acked && r.URL.Query().Get("acked") == "" {
			continue
		}
		out = append(out, rw)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Folder < out[j].Folder
	})
	// a why for the row: the first file that carries the majority answer
	for _, rw := range out {
		if len(out) > limit {
			break
		}
		for _, e := range a.Plan.Entries {
			if e.Kind == "" || e.Location != rw.Location || path.Dir(e.SourcePath) != rw.Folder {
				continue
			}
			m := s.inputs.Meta(e.Location)[e.SourcePath]
			if m.Category == rw.Category && m.Instrument == rw.Instrument {
				rw.Why = m.Why
				break
			}
		}
	}
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	jsonOut(w, map[string]any{"view": a.View, "built": a.Built, "kinds": kindTotals, "rows": out, "total_rows": total})
}

// folder lists one source folder's files as the plan places them.
func (s *Server) folder(w http.ResponseWriter, r *http.Request) {
	a, err := s.artifactFor(r.URL.Query().Get("view"))
	if err != nil {
		jsonErr(w, 409, err)
		return
	}
	loc, folder := r.URL.Query().Get("location"), strings.Trim(strings.ReplaceAll(r.URL.Query().Get("folder"), "\\", "/"), "/")
	files := []fileRow{}
	for _, e := range a.Plan.Entries {
		if e.Location != loc || path.Dir(e.SourcePath) != folder {
			continue
		}
		files = append(files, s.fileRow(e))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	total := len(files)
	if len(files) > 500 {
		files = files[:500]
	}
	jsonOut(w, map[string]any{"location": loc, "folder": folder, "files": files, "total": total})
}

// tree walks the destination tree as it will be written, one level at a
// time: child folders with their file counts, and the files at this level.
func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	a, err := s.artifactFor(r.URL.Query().Get("view"))
	if err != nil {
		jsonErr(w, 409, err)
		return
	}
	prefix := strings.Trim(strings.ReplaceAll(r.URL.Query().Get("prefix"), "\\", "/"), "/")
	type dir struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	dirs := map[string]int{}
	files := []fileRow{}
	total := 0
	for _, e := range a.Plan.Entries {
		rel := e.OutPath
		if prefix != "" {
			if !strings.HasPrefix(rel, prefix+"/") {
				continue
			}
			rel = rel[len(prefix)+1:]
		}
		total++
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			dirs[rel[:i]]++
			continue
		}
		if len(files) < 300 {
			fr := s.fileRow(e)
			fr.Name = rel
			files = append(files, fr)
		}
	}
	ds := []dir{}
	for name, n := range dirs {
		ds = append(ds, dir{name, n})
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	jsonOut(w, map[string]any{"prefix": prefix, "dirs": ds, "files": files, "total": total})
}

func (s *Server) provenance() correct.Provenance {
	p := correct.Provenance{AppVersion: version.Version}
	if h := annotations.CheckoutHead(context.Background(), s.ws.Root); h != nil {
		p.AnnotationsSHA = h.SHA
	}
	return p
}

// correctEndpoint previews or applies one correction.
func (s *Server) correctEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		correct.Correction
		Preview bool `json:"preview"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	lc, ok := s.ws.Location(req.Location)
	if !ok {
		jsonErr(w, 404, fmt.Errorf("no location named %q", req.Location))
		return
	}
	s.mu.Lock()
	in := s.freshInputs()
	s.mu.Unlock()
	entries, err := in.Catalog(lc.Name)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	vendors, err := in.Vendors()
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	current := in.Meta(lc.Name)
	var rad *correct.Radius
	if req.Preview {
		rad, err = correct.Preview(s.ws, lc, entries, current, vendors, req.Correction)
	} else {
		rad, err = correct.Apply(s.ws, lc, entries, current, vendors, req.Correction, s.provenance())
		s.mu.Lock()
		s.meta = nil // per-file caches were just patched; the next plan re-stamps its inputs
		s.mu.Unlock()
	}
	if err != nil {
		jsonErr(w, 400, err)
		return
	}
	jsonOut(w, map[string]any{"preview": req.Preview, "radius": rad})
}

// ackEndpoint marks a folder reviewed and left as-is.
func (s *Server) ackEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct{ Location, Folder, Note string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if err := correct.Ack(s.ws, req.Location, req.Folder, req.Note); err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonOut(w, map[string]string{"status": "acked"})
}

// reportEndpoint logs "this is your parser, not my pack" — evidence, no TOML.
func (s *Server) reportEndpoint(w http.ResponseWriter, r *http.Request) {
	var req correct.Correction
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	req.Facet = "report"
	s.mu.Lock()
	in := s.freshInputs()
	s.mu.Unlock()
	if err := correct.Report(s.ws, req, in.Meta(req.Location), s.provenance()); err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonOut(w, map[string]string{"status": "reported"})
}

// lexicon serves the vocabularies the pickers offer — ids the schema can
// hold as facts, nothing else (SPEC §19.3 rule 3).
func (s *Server) lexicon(w http.ResponseWriter, _ *http.Request) {
	root := filepath.Join(s.ws.Root, "annotations")
	lx := annotations.LoadInstruments(root)
	cx := annotations.LoadCategories(root)
	type ins struct {
		ID      string `json:"id"`
		Family  string `json:"family"`
		Display string `json:"display,omitempty"`
	}
	var instruments []ins
	for _, i := range lx.Instruments {
		instruments = append(instruments, ins{i.ID, i.Family, i.Display})
	}
	var cats []string
	seen := map[string]bool{}
	for _, c := range cx.Categories { // one id, several grammar entries
		if !seen[c.ID] {
			seen[c.ID] = true
			cats = append(cats, c.ID)
		}
	}
	jsonOut(w, map[string]any{"instruments": instruments, "families": lx.Families, "categories": cats})
}

// local lists the local layer; localExport hands it over as a zip.
func (s *Server) local(w http.ResponseWriter, _ *http.Request) {
	entries, err := correct.List(s.ws)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	if entries == nil {
		entries = []correct.LocalEntry{}
	}
	jsonOut(w, map[string]any{"entries": entries, "acks": len(correct.Acks(s.ws)), "dir": s.ws.LocalAnnotations()})
}

func (s *Server) localExport(w http.ResponseWriter, _ *http.Request) {
	b, err := correct.Export(s.ws)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="annotations.local.zip"`)
	w.Write(b)
}

// reconcile judges every local entry against the checkout (SPEC §19.5):
// an entry the checkout now agrees with is a shadow, offered for dropping.
func (s *Server) reconcile(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	in := s.freshInputs()
	s.mu.Unlock()
	vs, err := correct.Reconcile(s.ws, correct.Sources{Catalog: in.Catalog, Meta: in.Meta})
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	if vs == nil {
		vs = []correct.Verdict{}
	}
	redundant := 0
	for _, v := range vs {
		if v.Redundant {
			redundant++
		}
	}
	jsonOut(w, map[string]any{"verdicts": vs, "redundant": redundant})
}

// drop removes local entries the user let go.
func (s *Server) drop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Entries []correct.LocalEntry `json:"entries"`
		Reason  string               `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	dropped := 0
	for _, e := range req.Entries {
		if err := correct.Drop(s.ws, e, req.Reason); err != nil {
			jsonErr(w, 400, fmt.Errorf("%s: %w", e.File, err))
			return
		}
		dropped++
	}
	jsonOut(w, map[string]any{"dropped": dropped})
}

// redundantLocal counts the local entries a fresh checkout has made
// shadows — what the sync response carries so the user hears about it
// where the sync happened.
func (s *Server) redundantLocal() int {
	s.mu.Lock()
	in := s.freshInputs()
	s.mu.Unlock()
	vs, err := correct.Reconcile(s.ws, correct.Sources{Catalog: in.Catalog, Meta: in.Meta})
	if err != nil {
		return 0
	}
	n := 0
	for _, v := range vs {
		if v.Redundant {
			n++
		}
	}
	return n
}
