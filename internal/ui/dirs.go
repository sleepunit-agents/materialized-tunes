package ui

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// dirs backs the folder picker: list the subdirectories of one path, with
// the volumes and the home folder as starting points. Directories only —
// the picker chooses a materialize target, never a file. Nothing is created
// here; materialize makes the target when it runs.
func (s *Server) dirs(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	type resp struct {
		Path    string  `json:"path"`
		Parent  string  `json:"parent,omitempty"`
		Entries []entry `json:"entries"`
		Roots   []entry `json:"roots"`
		Error   string  `json:"error,omitempty"`
	}
	home, _ := os.UserHomeDir()
	roots := []entry{}
	if home != "" {
		roots = append(roots, entry{Name: "~ (home)", Path: home})
		if runtime.GOOS == "windows" {
			ul := filepath.Join(home, "Documents", "Ableton", "User Library")
			if st, err := os.Stat(ul); err == nil && st.IsDir() {
				roots = append(roots, entry{Name: "Ableton User Library", Path: ul})
			}
		}
	}
	seen := map[string]bool{}
	for _, r := range roots {
		seen[r.Path] = true
	}
	for _, v := range listVolumes() {
		if !seen[v.Path] {
			seen[v.Path] = true
			roots = append(roots, entry{Name: v.Name, Path: v.Path})
		}
	}
	if runtime.GOOS != "windows" && !seen["/"] {
		roots = append(roots, entry{Name: "/", Path: "/"})
	}

	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(home, p[1:])
	}
	if p == "" {
		p = home
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	out := resp{Path: p, Entries: []entry{}, Roots: roots}
	if parent := filepath.Dir(p); parent != p {
		out.Parent = parent
	}
	ents, err := os.ReadDir(p)
	if err != nil {
		out.Error = err.Error()
		jsonOut(w, out)
		return
	}
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out.Entries = append(out.Entries, entry{Name: e.Name(), Path: filepath.Join(p, e.Name())})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	jsonOut(w, out)
}
