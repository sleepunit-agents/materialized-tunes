// Package ui is the embedded browser UI: a localhost HTTP server carrying
// the same JSON the CLI's --json flags emit, plus a vanilla-JS front end
// (go:embed, no toolchain). The design ships in the binary; Wails can wrap
// the identical assets later without touching this package.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jbarket/materialized-tunes/internal/browse"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/materialize"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/view"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

//go:embed assets
var assets embed.FS

type Server struct {
	ws *workspace.Workspace

	mu  sync.Mutex
	run *runState // at most one materialization at a time
}

type runState struct {
	View     string             `json:"view"`
	Status   string             `json:"status"` // running | done | error
	Count    int                `json:"count"`
	Total    int                `json:"total"`
	Started  time.Time          `json:"started"`
	Error    string             `json:"error,omitempty"`
	Written  int                `json:"written"`
	Resumed  int                `json:"resumed"`
	Skipped  []materialize.Skip `json:"skipped,omitempty"`
	LockPath string             `json:"lock,omitempty"`
}

func Handler(ws *workspace.Workspace) http.Handler {
	s := &Server{ws: ws}
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "assets")
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/summary", s.summary)
	mux.HandleFunc("/api/devices", s.devices)
	mux.HandleFunc("/api/packs", s.packs)
	mux.HandleFunc("/api/views", s.views)
	mux.HandleFunc("/api/preflight", s.preflight)
	mux.HandleFunc("/api/materialize", s.materialize)
	mux.HandleFunc("/api/run", s.runStatus)
	mux.HandleFunc("/api/locks", s.locks)
	mux.HandleFunc("/api/diff", s.diff)
	return mux
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	w.WriteHeader(code)
	jsonOut(w, map[string]string{"error": err.Error()})
}

func (s *Server) summary(w http.ResponseWriter, _ *http.Request) {
	type out struct {
		Workspace string `json:"workspace"`
		Locations int    `json:"locations"`
		Files     int    `json:"files"`
		Bytes     int64  `json:"bytes"`
		Packs     int    `json:"packs_annotated"`
	}
	o := out{Workspace: s.ws.Root, Locations: len(s.ws.Config.Locations)}
	for _, lc := range s.ws.Config.Locations {
		entries, err := catalog.Load(s.ws.CatalogPath(lc.Name))
		if err != nil {
			continue
		}
		for _, ce := range entries {
			o.Files++
			o.Bytes += ce.Size
		}
	}
	rows, err := browse.Rows(s.ws, nil, "")
	if err == nil {
		for _, r := range rows {
			if r.Slug != "" {
				o.Packs++
			}
		}
	}
	jsonOut(w, o)
}

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	type dev struct {
		Name string `json:"name"`
		Sub  string `json:"sub"`
	}
	var out []dev
	files, _ := filepath.Glob(filepath.Join(s.ws.Root, "devices", "*.toml"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".toml")
		d, err := profile.LoadDevice(s.ws.Root, name)
		if err != nil {
			continue
		}
		sub := fmt.Sprintf("%d-bit %s %.1fkHz", d.Audio.BitDepth, strings.ToUpper(d.Audio.Format), float64(d.Audio.SampleRate)/1000)
		if d.Audio.Channels == "mono" {
			sub += " mono"
		}
		if d.Filesystem.Type != "" {
			sub += " · " + d.Filesystem.Type
		}
		if d.Delivery.Layout == "flatten" {
			sub += " · flat"
		}
		out = append(out, dev{Name: name, Sub: sub})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	jsonOut(w, out)
}

func (s *Server) packs(w http.ResponseWriter, r *http.Request) {
	var dev *profile.Device
	if d := r.URL.Query().Get("device"); d != "" {
		var err error
		if dev, err = profile.LoadDevice(s.ws.Root, d); err != nil {
			jsonErr(w, 400, err)
			return
		}
	}
	rows, err := browse.Rows(s.ws, dev, r.URL.Query().Get("location"))
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonOut(w, rows)
}

func (s *Server) views(w http.ResponseWriter, _ *http.Request) {
	type vw struct {
		Name    string `json:"name"`
		Device  string `json:"device"`
		Storage string `json:"storage"`
		Target  string `json:"target,omitempty"`
		Rules   int    `json:"rules"`
	}
	var out []vw
	files, _ := filepath.Glob(filepath.Join(s.ws.Root, "views", "*.toml"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".toml")
		v, err := view.Load(s.ws.Root, name)
		if err != nil {
			continue
		}
		out = append(out, vw{Name: name, Device: v.Device, Storage: v.Storage, Target: v.Target, Rules: len(v.Include)})
	}
	jsonOut(w, out)
}

// preflight runs the plan for a view with an optional subset of include
// rules disabled — a preview; the recipe file is never touched.
func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	var req struct {
		View     string `json:"view"`
		Disabled []int  `json:"disabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	v, err := view.Load(s.ws.Root, req.View)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	off := map[int]bool{}
	for _, i := range req.Disabled {
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

	// Per-rule stats: each include planned alone (cheap relative to the
	// full plan, honest about overlap being possible between rules).
	for i, inc := range v.Include {
		rules[i] = rule{Location: inc.Location, Glob: inc.Glob, As: inc.As, Enabled: !off[i]}
		single := *v
		single.Include = []view.Include{inc}
		single.Exclude = v.Exclude
		p, err := plan.BuildView(s.ws, &single)
		if err != nil {
			continue
		}
		rules[i].Files = len(p.Entries)
		rules[i].Bytes = p.TotalBytes
	}

	enabled := *v
	enabled.Include = nil
	for i, inc := range v.Include {
		if !off[i] {
			enabled.Include = append(enabled.Include, inc)
		}
	}
	var p *plan.Plan
	if len(enabled.Include) > 0 {
		if p, err = plan.BuildView(s.ws, &enabled); err != nil {
			jsonErr(w, 500, err)
			return
		}
	}

	out := map[string]any{"view": req.View, "device": v.Device, "storage": v.Storage, "rules": rules}
	if p != nil {
		out["files"] = len(p.Entries)
		p.Entries = nil // the UI wants the verdict, not 84k rows
		out["plan"] = p
	}
	jsonOut(w, out)
}

func (s *Server) materialize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		View string `json:"view"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}

	s.mu.Lock()
	if s.run != nil && s.run.Status == "running" {
		s.mu.Unlock()
		jsonErr(w, 409, fmt.Errorf("a run is already in progress (%s)", s.run.View))
		return
	}
	p, err := plan.Build(s.ws, req.View)
	if err != nil {
		s.mu.Unlock()
		jsonErr(w, 500, err)
		return
	}
	if len(p.Errors) > 0 {
		s.mu.Unlock()
		jsonErr(w, 409, fmt.Errorf("plan has %d error(s) — fix them first", len(p.Errors)))
		return
	}
	v, _ := view.Load(s.ws.Root, req.View)
	target := v.Target
	if strings.HasPrefix(target, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			target = filepath.Join(home, target[2:])
		}
	}
	if target == "" {
		s.mu.Unlock()
		jsonErr(w, 400, fmt.Errorf("view %s has no target — set target in the recipe", req.View))
		return
	}
	rs := &runState{View: req.View, Status: "running", Total: len(p.Entries), Started: time.Now()}
	s.run = rs
	s.mu.Unlock()

	go func() {
		// context.Background, NOT the request context: the POST returns
		// immediately and its context dies with it — the run must not.
		out, err := materialize.Materialize(context.Background(), s.ws, p, target, func(count, total int) {
			s.mu.Lock()
			rs.Count, rs.Total = count, total
			s.mu.Unlock()
		})
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			rs.Status, rs.Error = "error", err.Error()
			return
		}
		rs.Status = "done"
		rs.Count = out.Written
		rs.Written, rs.Resumed, rs.Skipped, rs.LockPath = out.Written, out.Resumed, out.Skipped, out.LockPath
	}()
	jsonOut(w, map[string]string{"status": "started"})
}

func (s *Server) runStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil {
		jsonOut(w, map[string]string{"status": "idle"})
		return
	}
	jsonOut(w, s.run)
}

func (s *Server) locks(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		File    string `json:"file"`
		View    string `json:"view"`
		Created string `json:"created"`
		Files   int    `json:"files"`
		Bytes   int64  `json:"bytes"`
		Newest  bool   `json:"newest"`
	}
	viewName := r.URL.Query().Get("view")
	var out []entry
	pattern := filepath.Join(s.ws.Root, "locks", viewName, "*.lock.json")
	if viewName == "" {
		pattern = filepath.Join(s.ws.Root, "locks", "*", "*.lock.json")
	}
	files, _ := filepath.Glob(pattern)
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for i, f := range files {
		l, err := lock.Read(f)
		if err != nil {
			continue
		}
		out = append(out, entry{
			File: filepath.Base(f), View: l.View,
			Created: l.Created.UTC().Format("2006-01-02 15:04"),
			Files:   l.Totals.Files, Bytes: l.Totals.Bytes,
			Newest: i == 0 && viewName != "",
		})
	}
	jsonOut(w, out)
}

// diff reports staleness of a view's newest lock vs the recipe today.
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	viewName := r.URL.Query().Get("view")
	lockPath, err := lock.Resolve(s.ws.Root, viewName)
	if err != nil {
		jsonErr(w, 404, err)
		return
	}
	l, err := lock.Read(lockPath)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	p, err := plan.Build(s.ws, l.View)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	shaByLoc := map[string]map[string]string{}
	for _, e := range l.Entries {
		if _, ok := shaByLoc[e.Source.Location]; ok {
			continue
		}
		entries, err := catalog.Load(s.ws.CatalogPath(e.Source.Location))
		if err != nil {
			continue
		}
		m := make(map[string]string, len(entries))
		for path, ce := range entries {
			m[path] = ce.SHA256
		}
		shaByLoc[e.Source.Location] = m
	}
	d := lock.Compute(l, p, shaByLoc)
	jsonOut(w, map[string]any{"lock": filepath.Base(lockPath), "in_sync": d.Clean(), "diff": d})
}
