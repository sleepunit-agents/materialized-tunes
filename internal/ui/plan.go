package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/sleepunit-agents/materialized-tunes/internal/lock"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/progress"
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
	Stamp    string // the inputs it was built from; differs from the live stamp once the workspace moved
	View     string
	Disabled []int
	Built    time.Time
	Plan     *plan.Plan // nil when nothing was enabled
	Verdict  map[string]any
	// The migrate hint is not part of the verdict: it compares the plan
	// with the newest lock, and the lock is not in the key — a materialize
	// or a migrate rewrites it without touching what the plan reads. It
	// is re-read whenever the lock underneath has changed (lockStamp), so
	// "MOVE 18 FILES" leaves the button once the 18 have moved.
	LockStamp string
	Migrate   map[string]int // nil when nothing would move
}

// lockStamp identifies the newest lock for a view — its path, size and
// mtime — or "" when it has never been materialized. A new lock is a new
// path, so a run always changes it.
func lockStamp(root, viewName string) string {
	lp, err := lock.Resolve(root, viewName)
	if err != nil {
		return ""
	}
	info, err := os.Stat(lp)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", lp, info.Size(), info.ModTime().UnixNano())
}

// migrateHint compares the plan with the newest lock: when the locked
// files would just move, the UI offers the rename path instead of
// duplicate-and-delete. nil when there is no lock or nothing would move.
func migrateHint(root string, viewName string, p *plan.Plan) map[string]int {
	if p == nil {
		return nil
	}
	lp, err := lock.Resolve(root, viewName)
	if err != nil {
		return nil
	}
	l, err := lock.Read(lp)
	if err != nil {
		return nil
	}
	if mg := lock.PlanMigration(l, p); mg.Work() > 0 {
		return map[string]int{"moves": len(mg.Moves), "companions": len(mg.Companions)}
	}
	return nil
}

// verdict is the artifact's answer to POST /api/plan, with the migrate
// hint re-read if the lock moved since it was last looked at. Caller
// holds s.mu.
func (s *Server) verdict(a *planArtifact) map[string]any {
	if st := lockStamp(s.ws.Root, a.View); st != a.LockStamp {
		a.LockStamp, a.Migrate = st, migrateHint(s.ws.Root, a.View, a.Plan)
	}
	out := map[string]any{"status": "done", "built": a.Built}
	for k, val := range a.Verdict {
		out[k] = val
	}
	if a.Migrate != nil {
		out["migrate"] = a.Migrate
	}
	return out
}

// freshInputs returns the shared inputs, replacing them when the files
// underneath have changed. The artifacts stay: their key no longer
// matches, so the next POST /api/plan rebuilds and materialize refuses
// them (cachedPlan), but the review surface keeps answering from the last
// plan built — marked stale — while the rebuild runs, instead of 409ing
// and throwing the user out of the folder they were deciding. A
// correction does not come through here at all: correctEndpoint patches
// the loaded inputs in place so the catalogs survive it. Caller holds s.mu.
func (s *Server) freshInputs() *plan.Inputs {
	if s.inputs == nil || !s.inputs.Fresh() {
		s.inputs = plan.NewInputs(s.ws)
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
		out := s.verdict(a)
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
		task := progress.Start("plan", "planning "+req.View).Set(plan.StageLoad, 0, 0)
		defer task.End()
		a, err := s.buildArtifact(v, disabled, key, in, func(stage string, done, total int) {
			task.Set(stage, int64(done), int64(total))
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
	return &planArtifact{Key: key, Stamp: in.Stamp(), View: v.Name, Disabled: disabled, Built: time.Now(), Plan: p, Verdict: out,
		LockStamp: lockStamp(s.ws.Root, v.Name), Migrate: migrateHint(s.ws.Root, v.Name, p)}, nil
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
