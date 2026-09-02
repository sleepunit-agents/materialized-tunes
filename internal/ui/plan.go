package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sleepunit-agents/materialized-tunes/internal/lock"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/view"
)

// The plan is a run and an artifact (SPEC §19.4). POST /api/plan with a
// view and the rules toggled off either answers from the cached artifact,
// reports the build in progress, or starts one. The key is the recipe as
// it is now, the toggles, and the stamp of everything the plan reads
// (catalogs, meta cache, both annotation layers): equal key, same plan.
// The artifact keeps its entries — they are the review surface §19.2
// reads — while the verdict the Plan step renders leaves them out.

type planRun struct {
	Key     string    `json:"-"`
	View    string    `json:"view"`
	Status  string    `json:"status"` // running | done | error
	Stage   string    `json:"stage,omitempty"`
	Count   int       `json:"count"`
	Total   int       `json:"total"`
	Started time.Time `json:"started"`
	Error   string    `json:"error,omitempty"`
}

type planArtifact struct {
	Key      string
	View     string
	Disabled []int
	Built    time.Time
	Plan     *plan.Plan // nil when nothing was enabled
	Verdict  map[string]any
}

// freshInputs returns the shared inputs, replacing them (and dropping
// every artifact) when the files underneath have changed. Caller holds s.mu.
func (s *Server) freshInputs() *plan.Inputs {
	if s.inputs == nil || !s.inputs.Fresh() {
		s.inputs = plan.NewInputs(s.ws)
		s.plans = map[string]*planArtifact{}
	}
	return s.inputs
}

func planKey(v *view.View, disabled []int, stamp string) string {
	vb, _ := json.Marshal(v)
	db, _ := json.Marshal(disabled)
	h := sha256.Sum256(append(append(append(vb, 0), db...), []byte(stamp)...))
	return hex.EncodeToString(h[:8])
}

func (s *Server) planEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		View     string `json:"view"`
		Disabled []int  `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	// LoadRaw: an empty recipe plans to "nothing selected", not to a 404 —
	// the Recipe screen is the picker you use to put something in it.
	v, err := view.LoadRaw(s.ws.Root, req.View)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	disabled := append([]int(nil), req.Disabled...)
	sort.Ints(disabled)

	s.mu.Lock()
	in := s.freshInputs()
	key := planKey(v, disabled, in.Stamp())
	if a := s.plans[req.View]; a != nil && a.Key == key {
		out := map[string]any{"status": "done", "built": a.Built}
		for k, val := range a.Verdict {
			out[k] = val
		}
		s.mu.Unlock()
		jsonOut(w, out)
		return
	}
	if pr := s.planRun; pr != nil && pr.Status == "running" {
		// one build at a time; a request for a different key polls again
		// and starts its own once this one lands
		cp := *pr
		s.mu.Unlock()
		jsonOut(w, cp)
		return
	}
	pr := &planRun{Key: key, View: req.View, Status: "running", Stage: plan.StageLoad, Started: time.Now()}
	s.planRun = pr
	s.mu.Unlock()

	go guard("plan build", func() {
		a, err := s.buildArtifact(v, disabled, key, in, func(stage string, done, total int) {
			s.mu.Lock()
			pr.Stage, pr.Count, pr.Total = stage, done, total
			s.mu.Unlock()
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			pr.Status, pr.Error = "error", err.Error()
			return
		}
		s.plans[req.View] = a
		pr.Status = "done"
	})
	s.mu.Lock()
	cp := *pr
	s.mu.Unlock()
	jsonOut(w, cp)
}

// buildArtifact plans the enabled rules once and attributes every entry
// to the rule that picked it; a disabled rule reports what it would
// select. The old preflight planned each rule alone and then the set —
// N+1 builds, each reloading the library.
func (s *Server) buildArtifact(v *view.View, disabled []int, key string, in *plan.Inputs, progress func(string, int, int)) (*planArtifact, error) {
	off := map[int]bool{}
	for _, i := range disabled {
		off[i] = true
	}
	type rule struct {
		Location string `json:"location"`
		Glob     string `json:"glob"`
		As       string `json:"as,omitempty"`
		Enabled  bool   `json:"enabled"`
		Files    int    `json:"files"`
		Bytes    int64  `json:"converted_bytes"`
	}
	rules := make([]rule, len(v.Include))
	enabled := *v
	enabled.Include = nil
	var origIdx []int // enabled position → index in the recipe
	for i, inc := range v.Include {
		rules[i] = rule{Location: inc.Location, Glob: inc.Glob, As: inc.As, Enabled: !off[i]}
		if !off[i] {
			enabled.Include = append(enabled.Include, inc)
			origIdx = append(origIdx, i)
			continue
		}
		// what a toggled-off rule would bring back: its matches less the
		// excludes; no placement, so no bytes
		if cat, err := in.Catalog(inc.Location); err == nil {
			for sp := range cat {
				if ok, _ := doublestar.Match(inc.Glob, sp); !ok {
					continue
				}
				excluded := false
				for _, exc := range v.Exclude {
					if hit, _ := doublestar.Match(exc.Glob, sp); hit {
						excluded = true
						break
					}
				}
				if !excluded {
					rules[i].Files++
				}
			}
		}
	}

	var p *plan.Plan
	if len(enabled.Include) > 0 {
		var err error
		if p, err = plan.BuildWith(s.ws, &enabled, plan.Options{Inputs: in, Progress: progress}); err != nil {
			return nil, err
		}
		lock.WarnMoved(s.ws.Root, p)
		for _, e := range p.Entries {
			i := origIdx[e.Rule]
			rules[i].Files++
			rules[i].Bytes += e.OutBytes
		}
	}

	excludes := make([]string, 0, len(v.Exclude))
	for _, e := range v.Exclude {
		excludes = append(excludes, e.Glob)
	}
	out := map[string]any{"view": v.Name, "device": v.Device, "storage": v.Storage, "layout": v.Layout,
		"layouts": view.LayoutPresets, "rules": rules, "excludes": excludes}
	if p != nil {
		// migrate hint: when the newest lock's files would just move, the
		// UI offers the rename path instead of duplicate-and-delete
		if lp, err := lock.Resolve(s.ws.Root, v.Name); err == nil {
			if l, err := lock.Read(lp); err == nil {
				if mg := lock.PlanMigration(l, p); mg.Work() > 0 {
					out["migrate"] = map[string]int{"moves": len(mg.Moves), "companions": len(mg.Companions)}
				}
			}
		}
		out["files"] = len(p.Entries)
		verdict := *p
		verdict.Entries = nil // the Plan step wants the verdict, not 84k rows; the artifact keeps them
		verdict.Overlaps = append([]plan.Overlap(nil), p.Overlaps...)
		for i := range verdict.Overlaps { // plan indexes enabled rules; the UI shows all of them
			verdict.Overlaps[i].RuleA = origIdx[verdict.Overlaps[i].RuleA]
			verdict.Overlaps[i].RuleB = origIdx[verdict.Overlaps[i].RuleB]
		}
		out["plan"] = &verdict
	}
	return &planArtifact{Key: key, View: v.Name, Disabled: disabled, Built: time.Now(), Plan: p, Verdict: out}, nil
}

// cachedPlan is the current artifact for a view with every rule enabled,
// or nil — what materialize and migrate start from without rebuilding.
// Caller holds s.mu.
func (s *Server) cachedPlan(viewName string) *plan.Plan {
	v, err := view.Load(s.ws.Root, viewName)
	if err != nil {
		return nil
	}
	in := s.freshInputs()
	a := s.plans[viewName]
	if a == nil || a.Plan == nil || len(a.Disabled) > 0 || a.Key != planKey(v, nil, in.Stamp()) {
		return nil
	}
	return a.Plan
}

func (s *Server) planFor(viewName string) (*plan.Plan, error) {
	if p := s.cachedPlan(viewName); p != nil {
		return p, nil
	}
	v, err := view.Load(s.ws.Root, viewName)
	if err != nil {
		return nil, err
	}
	return plan.BuildWith(s.ws, v, plan.Options{Inputs: s.inputs})
}
