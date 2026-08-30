package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/location"
	"github.com/sleepunit-agents/materialized-tunes/internal/resolve"
	"github.com/sleepunit-agents/materialized-tunes/internal/scan"
	"github.com/sleepunit-agents/materialized-tunes/internal/version"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// ---- annotations status -------------------------------------------------
//
// "Are we actually updating annotations?" deserves an answer you can see:
// GET says what commit the checkout is at (plus this binary's version, the
// other half of "why am I still getting the old layout"); POST updates it
// right now, bypassing the scan-time throttle. When an update lands, the
// existing catalogs are re-harvested on the spot — classification changes
// must never sit stale behind a "rescan to apply" the user hasn't done.

func (s *Server) annotationsEndpoint(w http.ResponseWriter, r *http.Request) {
	type resp struct {
		Version     string            `json:"version"`
		Head        *annotations.Head `json:"head"`
		Action      string            `json:"action,omitempty"`
		Note        string            `json:"note,omitempty"`
		Reharvested bool              `json:"reharvested,omitempty"`
	}
	out := resp{Version: version.Version}
	if r.Method == http.MethodPost {
		res := annotations.SyncNow(r.Context(), s.ws.Root)
		out.Action, out.Note = string(res.Action), res.Note
		if res.Changed() {
			s.reharvestAll()
			out.Reharvested = true
		}
	}
	out.Head = annotations.CheckoutHead(r.Context(), s.ws.Root)
	jsonOut(w, out)
}

// reharvestAll rederives every location's per-file metadata from its
// existing catalog — the step a scan normally does, minus the disk walk.
// Cheap (string ops), so it runs whenever the annotations snapshot moves:
// new grammar applies to the trees immediately, no rescan needed. New
// files on disk still need a scan; this only refreshes what's cataloged.
func (s *Server) reharvestAll() {
	for _, lc := range s.ws.Config.Locations {
		harvest.Run(s.ws, lc) // best-effort; a failed location keeps its old meta
	}
	s.mu.Lock()
	s.meta = nil // per-file metadata caches were just rewritten
	s.mu.Unlock()
}

// ---- source suggestions -------------------------------------------------
//
// We never crawl the disk looking for audio. We check a short list of
// KNOWN install locations — from vendor annotations (a fact the community
// maintains) plus a builtin table for the DAW/sampler factory content that
// has no vendor profile — and offer only the ones that actually exist and
// aren't already configured.

type suggestion struct {
	Name    string `json:"name"`  // proposed location name
	Label   string `json:"label"` // human label ("Splice")
	Root    string `json:"root"`  // absolute path that exists
	Vendor  string `json:"vendor,omitempty"`
	Rescan  string `json:"rescan"` // suggested cadence
	Note    string `json:"note,omitempty"`
	Entries int    `json:"entries"` // top-level dir count, a rough size hint
}

// builtinSuggestions are well-known content locations with no vendor
// annotation of their own. Paths are ~-relative; only existing ones surface.
var builtinSuggestions = []struct{ name, label, path, rescan, note string }{
	{"ableton-user", "Ableton User Library", "~/Music/Ableton/User Library/Samples", "manual", "Ableton's own sample folder"},
	{"logic", "Logic / GarageBand", "~/Music/Audio Music Apps/Sampler Instruments", "manual", "Apple loops and sampler content"},
	{"ni", "Native Instruments", "~/Documents/Native Instruments", "manual", "Maschine/Kontakt user content"},
	{"loopcloud", "Loopcloud", "~/Documents/Loopcloud", "6h", "app-managed, changes as you download"},
	{"downloads-samples", "Downloads/Samples", "~/Downloads/Samples", "1h", "the unzip-here folder"},
	{"music-samples", "Music/Samples", "~/Music/Samples", "6h", ""},
}

func (s *Server) suggestions(w http.ResponseWriter, _ *http.Request) {
	configured := map[string]bool{}
	for _, lc := range s.ws.Config.Locations {
		if abs, err := workspace.ExpandUser(lc.Root); err == nil {
			configured[filepath.Clean(abs)] = true
		}
	}

	var out []suggestion
	seen := map[string]bool{}
	add := func(sg suggestion) {
		root, err := workspace.ExpandUser(sg.Root)
		if err != nil {
			return
		}
		root = filepath.Clean(root)
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() || configured[root] || seen[root] {
			return
		}
		// A configured location that CONTAINS this path counts as covered.
		for c := range configured {
			if strings.HasPrefix(root, c+string(filepath.Separator)) {
				return
			}
		}
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					sg.Entries++
				}
			}
		}
		if sg.Entries == 0 {
			return // empty dir: nothing to offer
		}
		sg.Root = root
		seen[root] = true
		out = append(out, sg)
	}

	// tier 1: vendors that declare their install location
	vendors, _ := annotations.Load(filepath.Join(s.ws.Root, "annotations"))
	for _, v := range vendors {
		paths := v.InstallMac
		switch runtime.GOOS {
		case "linux":
			paths = v.InstallLinux
		case "windows":
			paths = v.InstallWin
		}
		for _, p := range paths {
			add(suggestion{
				Name: v.Slug, Label: v.Name, Root: p, Vendor: v.Slug,
				Rescan: "1h", Note: v.InstallNote,
			})
		}
	}
	// tier 2: builtin table
	for _, b := range builtinSuggestions {
		add(suggestion{Name: b.name, Label: b.label, Root: b.path, Rescan: b.rescan, Note: b.note})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Entries > out[j].Entries })
	jsonOut(w, out)
}

// ---- locations: list / add ---------------------------------------------

var nameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (s *Server) locations(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.addLocation(w, r)
		return
	}
	type loc struct {
		workspace.LocationConfig
		Files    int    `json:"files"`
		Scanned  string `json:"scanned,omitempty"` // last catalog write, RFC3339
		Stale    bool   `json:"stale"`             // past its rescan cadence
		Scanning bool   `json:"scanning"`
	}
	var out []loc
	s.mu.Lock()
	scanning := map[string]bool{}
	for name, st := range s.scans {
		if st.Status == "running" {
			scanning[name] = true
		}
	}
	s.mu.Unlock()
	for _, lc := range s.ws.Config.Locations {
		l := loc{LocationConfig: lc, Scanning: scanning[lc.Name]}
		if st, err := os.Stat(s.ws.CatalogPath(lc.Name)); err == nil {
			l.Scanned = st.ModTime().UTC().Format(time.RFC3339)
			l.Stale = isStale(lc.Rescan, st.ModTime())
		} else {
			l.Stale = true
		}
		if entries, err := loadCatalogCount(s.ws.CatalogPath(lc.Name)); err == nil {
			l.Files = entries
		}
		out = append(out, l)
	}
	jsonOut(w, out)
}

func isStale(rescan string, last time.Time) bool {
	if rescan == "" || rescan == "manual" {
		return false
	}
	d, err := time.ParseDuration(rescan)
	if err != nil {
		return false
	}
	return time.Since(last) > d
}

func loadCatalogCount(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 32*1024)
	count := 0
	for {
		n, err := f.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				count++
			}
		}
		if err != nil {
			break
		}
	}
	return count, nil
}

func (s *Server) addLocation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Root   string `json:"root"`
		Host   string `json:"host"`
		Vendor string `json:"vendor"`
		Rescan string `json:"rescan"`
		Scan   bool   `json:"scan"`   // kick off the first scan immediately
		Update string `json:"update"` // existing location: change its cadence
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if req.Update != "" {
		for i := range s.ws.Config.Locations {
			if s.ws.Config.Locations[i].Name == req.Update {
				s.ws.Config.Locations[i].Rescan = req.Rescan
				if err := s.ws.SaveConfig(); err != nil {
					jsonErr(w, 500, err)
					return
				}
				jsonOut(w, map[string]string{"status": "updated"})
				return
			}
		}
		jsonErr(w, 404, fmt.Errorf("unknown location %q", req.Update))
		return
	}
	req.Name = strings.TrimSpace(strings.ToLower(req.Name))
	if !nameOK.MatchString(req.Name) {
		jsonErr(w, 400, fmt.Errorf("name must be lowercase letters, digits, - or _"))
		return
	}
	if _, exists := s.ws.Location(req.Name); exists {
		jsonErr(w, 409, fmt.Errorf("location %q already exists", req.Name))
		return
	}
	if req.Type == "" {
		req.Type = "local"
	}
	if req.Type != "local" && req.Type != "ssh" {
		jsonErr(w, 400, fmt.Errorf("type must be local or ssh"))
		return
	}
	if req.Type == "local" {
		abs, err := workspace.ExpandUser(req.Root)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			jsonErr(w, 400, fmt.Errorf("not a directory: %s", abs))
			return
		}
		req.Root = abs
	} else if req.Host == "" {
		jsonErr(w, 400, fmt.Errorf("ssh locations need a host"))
		return
	}

	s.ws.Config.Locations = append(s.ws.Config.Locations, workspace.LocationConfig{
		Name: req.Name, Type: req.Type, Root: req.Root,
		Host: req.Host, Vendor: req.Vendor, Rescan: req.Rescan,
	})
	if err := s.ws.SaveConfig(); err != nil {
		jsonErr(w, 500, err)
		return
	}
	if req.Scan {
		s.startScan(req.Name)
	}
	jsonOut(w, map[string]string{"status": "added", "name": req.Name})
}

// ---- scanning -----------------------------------------------------------

type scanState struct {
	Location string    `json:"location"`
	Status   string    `json:"status"` // running | done | error
	Stage    string    `json:"stage,omitempty"`
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Started  time.Time `json:"started"`
	Error    string    `json:"error,omitempty"`
	Result   string    `json:"result,omitempty"`
}

func (s *Server) startScan(name string) error {
	s.mu.Lock()
	if s.scans == nil {
		s.scans = map[string]*scanState{}
	}
	if st, ok := s.scans[name]; ok && st.Status == "running" {
		s.mu.Unlock()
		return fmt.Errorf("already scanning %s", name)
	}
	lc, ok := s.ws.Location(name)
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown location %q", name)
	}
	st := &scanState{Location: name, Status: "running", Started: time.Now()}
	s.scans[name] = st
	s.mu.Unlock()

	go func() {
		// Freshen the annotations checkout before harvest reads it. Throttled
		// and serialized inside Sync, so concurrent/auto scans stay cheap.
		annSync := annotations.Sync(context.Background(), s.ws.Root)
		loc, err := location.New(lc)
		if err == nil {
			var res *scan.Result
			res, err = scan.Run(context.Background(), loc, s.ws.CatalogPath(name), func(stage string, done, total int) {
				s.mu.Lock()
				st.Stage, st.Done, st.Total = stage, done, total
				s.mu.Unlock()
			})
			if err == nil {
				// derive per-file metadata (bpm/key/category) from the fresh catalog
				harvest.Run(s.ws, lc)
				if annSync.Changed() {
					// the pre-scan pull landed new grammar — the other
					// locations' harvests are stale under it too
					s.reharvestAll()
				}
				if vendors, err := annotations.Load(filepath.Join(s.ws.Root, "annotations")); err == nil {
					resolve.Location(context.Background(), s.ws, lc, vendors, nil) // best-effort, cached
				}
				s.mu.Lock()
				st.Result = fmt.Sprintf("%d files: %d added, %d changed, %d removed, %d unchanged",
					res.Total, res.Added, res.Changed, res.Removed, res.Unchanged)
				if annSync.Note != "" {
					st.Result = annSync.Note + " · " + st.Result
				}
				st.Status = "done"
				delete(s.meta, name) // per-file metadata was just rewritten
				s.mu.Unlock()
				return
			}
		}
		s.mu.Lock()
		st.Status, st.Error = "error", err.Error()
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) scanEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Location string `json:"location"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if err := s.startScan(req.Location); err != nil {
			jsonErr(w, 409, err)
			return
		}
		jsonOut(w, map[string]string{"status": "started"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*scanState{}
	for k, v := range s.scans {
		out[k] = v
	}
	jsonOut(w, out)
}

// autoScan rescans locations whose cadence has elapsed. Cheap by design:
// scans are incremental (unchanged size+mtime keeps the recorded hash), so
// a no-op rescan of a big library costs a listing, not a rehash.
func (s *Server) autoScan() {
	for {
		time.Sleep(2 * time.Minute)
		for _, lc := range s.ws.Config.Locations {
			if lc.Rescan == "" || lc.Rescan == "manual" {
				continue
			}
			st, err := os.Stat(s.ws.CatalogPath(lc.Name))
			if err == nil && !isStale(lc.Rescan, st.ModTime()) {
				continue
			}
			s.startScan(lc.Name)
		}
	}
}
