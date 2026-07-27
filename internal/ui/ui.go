// Package ui is the embedded browser UI: a localhost HTTP server carrying
// the same JSON the CLI's --json flags emit, plus a vanilla-JS front end
// (go:embed, no toolchain). The design ships in the binary; Wails can wrap
// the identical assets later without touching this package.
package ui

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/browse"
	"github.com/jbarket/materialized-tunes/internal/cache"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/location"
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

	mu   sync.Mutex
	run  *runState // at most one materialization at a time
	meta map[string]map[string]fileMeta
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
	mux.HandleFunc("/api/art", s.art)
	mux.HandleFunc("/api/blurb", s.blurb)
	mux.HandleFunc("/api/pack", s.packDetail)
	mux.HandleFunc("/api/preview", s.preview)
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

// ---- enrichment cache: vendor prose and pixels live in the workspace, ----
// ---- never in the annotations repo. Fetched once, cached, gitignored. ----

// allowedURLs collects every image/product URL the annotation layer knows.
// The art/blurb endpoints only ever fetch these — no open proxy.
func (s *Server) allowedURLs() (images, pages map[string]bool) {
	images, pages = map[string]bool{}, map[string]bool{}
	vendors, err := annotations.Load(filepath.Join(s.ws.Root, "annotations"))
	if err != nil {
		return
	}
	for _, v := range vendors {
		for _, p := range v.Packs {
			if p.Meta.Image != "" {
				images[p.Meta.Image] = true
			}
			if p.URL != "" {
				pages[p.URL] = true
			}
		}
	}
	return
}

func (s *Server) cachePath(kind, url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(s.ws.Root, "annotations-cache", kind, hex.EncodeToString(sum[:8]))
}

func fetchURL(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mtunes/0.4 (+local enrichment cache)")
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// art serves a pack's cover image from the workspace cache, fetching it
// from the vendor CDN once. Only URLs present in annotations are allowed.
func (s *Server) art(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	images, _ := s.allowedURLs()
	if !images[u] {
		w.WriteHeader(403)
		return
	}
	path := s.cachePath("img", u)
	if _, err := os.Stat(path); err != nil {
		data, err := fetchURL(u)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, data, 0o644)
	}
	w.Header().Set("Cache-Control", "max-age=86400")
	http.ServeFile(w, r, path)
}

// blurb serves a pack's og title/description from the workspace cache,
// scraped from its product page once. Vendor prose stays local.
func (s *Server) blurb(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	_, pages := s.allowedURLs()
	if !pages[u] {
		w.WriteHeader(403)
		return
	}
	path := s.cachePath("blurb", u) + ".json"
	if _, err := os.Stat(path); err != nil {
		title, desc := "", ""
		// Shopify stores expose the FULL product description as public
		// JSON at <product-url>.js — og:description is truncated by the
		// platform (~320 chars, mid-sentence). Try the JSON first.
		if data, err := fetchURL(u + ".js"); err == nil {
			var prod struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			}
			if json.Unmarshal(data, &prod) == nil && prod.Description != "" {
				title = prod.Title
				txt := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(prod.Description, " ")
				desc = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(html.UnescapeString(txt), " "))
			}
		}
		if desc == "" {
			data, err := fetchURL(u)
			if err != nil {
				w.WriteHeader(502)
				return
			}
			og := func(prop string) string {
				re := regexp.MustCompile(`<meta property="og:` + prop + `" content="([^"]*)"`)
				if m := re.FindSubmatch(data); m != nil {
					return html.UnescapeString(string(m[1]))
				}
				return ""
			}
			title, desc = og("title"), og("description")
		}
		blob, _ := json.Marshal(map[string]string{"title": title, "description": desc})
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, blob, 0o644)
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeFile(w, r, path)
}

// ---- pack detail: the drill-in view. Folder rail + file table straight ----
// ---- from the catalog; per-file device lens via the plan predicates.   ----

func (s *Server) packDetail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	locName, dir, folder := q.Get("location"), q.Get("dir"), q.Get("folder")
	var dev *profile.Device
	if d := q.Get("device"); d != "" {
		var err error
		if dev, err = profile.LoadDevice(s.ws.Root, d); err != nil {
			jsonErr(w, 400, err)
			return
		}
	}
	entries, err := catalog.Load(s.ws.CatalogPath(locName))
	if err != nil {
		jsonErr(w, 404, err)
		return
	}

	type file struct {
		Path      string  `json:"path"` // catalog path (location-relative)
		Name      string  `json:"name"`
		Format    string  `json:"format,omitempty"`
		Channels  int     `json:"channels,omitempty"`
		Rate      int     `json:"rate,omitempty"`
		Depth     int     `json:"depth,omitempty"`
		Duration  float64 `json:"duration,omitempty"`
		Size      int64   `json:"size"`
		Reason    string  `json:"ineligible,omitempty"` // device lens: why it can't ride
		Converted int64   `json:"converted,omitempty"`
		BPM       int     `json:"bpm,omitempty"`
		Key       string  `json:"key,omitempty"`
		Chord     string  `json:"chord,omitempty"`
		Cat       string  `json:"cat,omitempty"`
	}
	// folder is a path WITHIN the pack ("WAV", "WAV/Acid Synths", ...);
	// the response describes that level: its child dirs and its own files.
	cur := dir + "/"
	if folder != "" {
		cur += folder + "/"
	}
	folderCount := map[string]int{}
	total := 0
	var paths []string
	byPath := map[string]catalog.Entry{}
	for _, ce := range entries {
		if !strings.HasPrefix(ce.Path, cur) {
			continue
		}
		rest := ce.Path[len(cur):]
		top, _, nested := strings.Cut(rest, "/")
		if nested {
			folderCount[top]++ // recursive count under this child
			continue
		}
		total++
		paths = append(paths, ce.Path)
		byPath[ce.Path] = ce
	}
	var files []file
	sort.Strings(paths)
	const cap = 400
	shown := paths
	if len(shown) > cap {
		shown = shown[:cap]
	}
	meta := s.loadMeta(locName)
	for _, p := range shown {
		ce := byPath[p]
		f := file{Path: ce.Path, Name: p[strings.LastIndex(p, "/")+1:], Size: ce.Size}
		if mr, ok := meta[ce.SHA256]; ok {
			f.BPM, f.Key, f.Chord, f.Cat = mr.BPM, mr.Key, mr.Chord, mr.Category
		}
		if ce.Audio != nil {
			f.Format = ce.Audio.Format
			f.Channels, f.Rate, f.Depth = ce.Audio.Channels, ce.Audio.SampleRate, ce.Audio.BitDepth
			f.Duration = ce.Audio.DurationS
		}
		if dev != nil {
			if reason := plan.Eligibility(dev, ce); reason != "" {
				f.Reason = reason
			} else {
				f.Converted = plan.ConvertedBytes(dev, ce)
			}
		}
		files = append(files, f)
	}
	type foldOut struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	var folds []foldOut
	for name, c := range folderCount {
		folds = append(folds, foldOut{name, c})
	}
	sort.Slice(folds, func(i, j int) bool { return folds[i].Name < folds[j].Name })
	jsonOut(w, map[string]any{"folders": folds, "files": files, "total": total, "shown": len(files)})
}

// preview streams one cataloged source file: local sources in place,
// remote ones through the same content-addressed cache materialize uses
// (a preview warms the cache; nothing is ever written to a target).
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	locName, path := r.URL.Query().Get("location"), r.URL.Query().Get("path")
	lc, ok := s.ws.Location(locName)
	if !ok {
		jsonErr(w, 404, fmt.Errorf("unknown location %q", locName))
		return
	}
	entries, err := catalog.Load(s.ws.CatalogPath(locName))
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	ce, ok := entries[path]
	if !ok {
		jsonErr(w, 404, fmt.Errorf("not in catalog: %s", path))
		return
	}
	loc, err := location.New(lc)
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	local, err := cache.Ensure(r.Context(), loc, path, ce.SHA256, filepath.Join(s.ws.Root, "cache", "objects"))
	if err != nil {
		jsonErr(w, 502, err)
		return
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		w.Header().Set("Content-Type", "audio/wav")
	case ".aif", ".aiff":
		w.Header().Set("Content-Type", "audio/aiff")
	case ".flac":
		w.Header().Set("Content-Type", "audio/flac")
	}
	http.ServeFile(w, r, local)
}

// ---- local per-file metadata (annotations-cache/meta/<location>.jsonl) ----
// Harvested vendor data (bpm, key, tags), keyed by content SHA — proprietary
// vendor databases stay in the workspace, never in the annotations repo.

type fileMeta struct {
	SHA      string   `json:"sha"`
	BPM      int      `json:"bpm,omitempty"`
	Key      string   `json:"key,omitempty"`
	Chord    string   `json:"chord,omitempty"`
	Category string   `json:"category,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

func (s *Server) loadMeta(location string) map[string]fileMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta == nil {
		s.meta = map[string]map[string]fileMeta{}
	}
	if m, ok := s.meta[location]; ok {
		return m
	}
	m := map[string]fileMeta{}
	f, err := os.Open(filepath.Join(s.ws.Root, "annotations-cache", "meta", location+".jsonl"))
	if err == nil {
		defer f.Close()
		dec := json.NewDecoder(f)
		for {
			var r fileMeta
			if dec.Decode(&r) != nil {
				break
			}
			m[r.SHA] = r
		}
	}
	s.meta[location] = m
	return m
}

// Assets exposes the embedded frontend for alternative hosts (the Wails
// desktop shell serves these same files natively).
func Assets() fs.FS {
	static, _ := fs.Sub(assets, "assets")
	return static
}
