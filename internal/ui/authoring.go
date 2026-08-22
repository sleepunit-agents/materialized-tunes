package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/view"
)

// Recipes, device profiles and storage profiles are hand-editable TOML and
// stay that way: the UI edits them SURGICALLY (append/remove whole blocks,
// rewrite single keys) so comments and hand-tuning survive a UI round trip.
// Only brand-new files are generated wholesale.

// ---- recipes ------------------------------------------------------------

func (s *Server) viewWrite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action"` // create | add-rule | remove-rule | set-target | rename
		Name    string `json:"name"`
		NewName string `json:"new_name"` // rename
		Device  string `json:"device"`
		Storage string `json:"storage"`
		Target  string `json:"target"`
		// add-rule
		Location string `json:"location"`
		Glob     string `json:"glob"`
		As       string `json:"as"`
		Note     string `json:"note"`
		// ReplaceLocation: drop every existing [[include]] for Location
		// before appending — the "one rule for all of splice" button,
		// which would otherwise stack on top of per-pack rules and land
		// the same files twice (see plan.Overlaps).
		ReplaceLocation bool `json:"replace_location"`
		// remove-rule
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if !recipeNameOK.MatchString(req.Name) {
		jsonErr(w, 400, fmt.Errorf("recipe name must be letters, digits, - or _"))
		return
	}
	path := filepath.Join(s.ws.Root, "views", req.Name+".toml")

	if req.Action == "rename" {
		if err := s.renameView(req.Name, req.NewName); err != nil {
			jsonErr(w, 400, err)
			return
		}
		jsonOut(w, map[string]string{"status": "ok", "view": req.NewName})
		return
	}

	switch req.Action {
	case "create":
		if _, err := os.Stat(path); err == nil {
			jsonErr(w, 409, fmt.Errorf("recipe %q already exists", req.Name))
			return
		}
		if req.Device == "" || req.Storage == "" {
			jsonErr(w, 400, fmt.Errorf("device and storage are required"))
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "# %s — a materialized view: device + storage + selection.\n", req.Name)
		sb.WriteString("# Hand-edit freely; the UI only appends and removes whole blocks.\n")
		fmt.Fprintf(&sb, "name    = %q\ndevice  = %q\nstorage = %q\n", req.Name, req.Device, req.Storage)
		if req.Target != "" {
			fmt.Fprintf(&sb, "target  = %q\n", req.Target)
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			jsonErr(w, 500, err)
			return
		}

	case "add-rule":
		if req.Location == "" || req.Glob == "" {
			jsonErr(w, 400, fmt.Errorf("location and glob are required"))
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		src := string(data)
		replaced := 0
		if req.ReplaceLocation {
			if src, replaced, err = removeIncludesForLocation(src, req.Location); err != nil {
				jsonErr(w, 400, err)
				return
			}
		}
		var sb strings.Builder
		sb.WriteString(src)
		if !strings.HasSuffix(src, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		if req.Note != "" {
			fmt.Fprintf(&sb, "# %s\n", req.Note)
		}
		fmt.Fprintf(&sb, "[[include]]\nlocation = %q\nglob     = %q\n", req.Location, req.Glob)
		if req.As != "" {
			fmt.Fprintf(&sb, "as       = %q\n", req.As)
		}
		if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
			jsonErr(w, 500, err)
			return
		}
		jsonOut(w, map[string]any{"status": "ok", "view": req.Name, "replaced": replaced})
		return

	case "remove-rule":
		data, err := os.ReadFile(path)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		out, err := removeIncludeBlock(string(data), req.Index)
		if err != nil {
			jsonErr(w, 400, err)
			return
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			jsonErr(w, 500, err)
			return
		}

	case "set-target":
		data, err := os.ReadFile(path)
		if err != nil {
			jsonErr(w, 404, err)
			return
		}
		if err := os.WriteFile(path, []byte(setScalar(string(data), "target", req.Target)), 0o644); err != nil {
			jsonErr(w, 500, err)
			return
		}

	default:
		jsonErr(w, 400, fmt.Errorf("unknown action %q", req.Action))
		return
	}

	// Round-trip through the loader so a bad edit surfaces immediately.
	// Skipped for "create": a recipe with no rules yet is legitimately
	// incomplete, and view.Load rightly refuses it.
	if _, err := view.Load(s.ws.Root, req.Name); err != nil && req.Action != "create" {
		jsonErr(w, 500, fmt.Errorf("wrote %s but it no longer parses: %w", req.Name+".toml", err))
		return
	}
	jsonOut(w, map[string]string{"status": "ok", "view": req.Name})
}

// Recipe names are case-preserving: the name is usually also the folder a
// device sees ("Samples"), so the host's casing is the user's call. Devices
// and storages stay lowercase (they are referenced from recipes by name).
var recipeNameOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// renameView moves views/<old>.toml to views/<new>.toml, rewrites its name
// key, and carries the lock history (locks/<old>/) along so `restore`,
// `diff` and the Cards screen keep finding it. Past lockfiles still record
// the old name inside — that is history, left alone.
func (s *Server) renameView(oldName, newName string) error {
	if !recipeNameOK.MatchString(newName) {
		return fmt.Errorf("recipe name must be letters, digits, - or _")
	}
	if oldName == newName {
		return nil
	}
	oldPath := filepath.Join(s.ws.Root, "views", oldName+".toml")
	newPath := filepath.Join(s.ws.Root, "views", newName+".toml")
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return fmt.Errorf("no such recipe: %s", oldName)
	}
	// Case-only renames on case-insensitive filesystems (Windows, macOS)
	// stat the same file, so only refuse when it is genuinely another recipe.
	if _, err := os.Stat(newPath); err == nil && !strings.EqualFold(oldName, newName) {
		return fmt.Errorf("recipe %q already exists", newName)
	}
	out := setScalar(string(data), "name", newName)
	if err := os.WriteFile(oldPath, []byte(out), 0o644); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}
	oldLocks := filepath.Join(s.ws.Root, "locks", oldName)
	newLocks := filepath.Join(s.ws.Root, "locks", newName)
	if st, err := os.Stat(oldLocks); err == nil && st.IsDir() {
		if _, err := os.Stat(newLocks); err != nil || strings.EqualFold(oldName, newName) {
			if err := os.Rename(oldLocks, newLocks); err != nil {
				return fmt.Errorf("recipe renamed but its lock folder did not follow: %w", err)
			}
		}
	}
	if _, err := view.Load(s.ws.Root, newName); err != nil {
		return fmt.Errorf("renamed, but %s.toml no longer parses: %w", newName, err)
	}
	return nil
}

// removeIncludesForLocation drops every [[include]] whose location is loc
// (comments attached to each go with it) and reports how many went. The
// TOML is parsed once to find them and edited textually so hand-written
// lines elsewhere survive.
func removeIncludesForLocation(src, loc string) (string, int, error) {
	var v struct {
		Include []view.Include `toml:"include"`
	}
	if err := toml.Unmarshal([]byte(src), &v); err != nil {
		return "", 0, fmt.Errorf("recipe does not parse: %w", err)
	}
	removed := 0
	// walk backwards so earlier indexes stay valid as blocks vanish
	for i := len(v.Include) - 1; i >= 0; i-- {
		if v.Include[i].Location != loc {
			continue
		}
		out, err := removeIncludeBlock(src, i)
		if err != nil {
			return "", 0, err
		}
		src = out
		removed++
	}
	return src, removed, nil
}

// removeIncludeBlock drops the n-th [[include]] block (0-based) along with
// any comment lines directly attached above it.
func removeIncludeBlock(src string, n int) (string, error) {
	lines := strings.Split(src, "\n")
	starts := []int{}
	for i, l := range lines {
		if strings.TrimSpace(l) == "[[include]]" {
			starts = append(starts, i)
		}
	}
	if n < 0 || n >= len(starts) {
		return "", fmt.Errorf("rule %d does not exist (%d rules)", n, len(starts))
	}
	start := starts[n]
	// absorb attached comments above
	for start > 0 {
		prev := strings.TrimSpace(lines[start-1])
		if strings.HasPrefix(prev, "#") {
			start--
			continue
		}
		break
	}
	end := len(lines)
	for i := starts[n] + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "[") {
			end = i
			break
		}
		if strings.HasPrefix(t, "#") {
			// a comment introducing the NEXT block ends this one
			for j := i; j < len(lines); j++ {
				tj := strings.TrimSpace(lines[j])
				if strings.HasPrefix(tj, "[") {
					end = i
					break
				}
				if tj != "" && !strings.HasPrefix(tj, "#") {
					break // comment belonged to this block after all
				}
			}
			if end != len(lines) {
				break
			}
		}
	}
	// trim one blank line above the removed span so gaps don't accumulate
	for start > 0 && strings.TrimSpace(lines[start-1]) == "" {
		start--
		break
	}
	kept := append(append([]string{}, lines[:start]...), lines[end:]...)
	out := strings.Join(kept, "\n")
	if strings.HasSuffix(src, "\n") && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// setScalar rewrites a top-level key in place, or inserts it after the
// last top-level scalar if it isn't there yet.
func setScalar(src, key, val string) string {
	re := regexp.MustCompile(`(?m)^` + key + `\s*=.*$`)
	if re.MatchString(src) {
		if val == "" {
			return re.ReplaceAllString(src, "")
		}
		return re.ReplaceAllString(src, fmt.Sprintf("%-7s = %q", key, val))
	}
	if val == "" {
		return src
	}
	lines := strings.Split(src, "\n")
	insert := 0
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "[") {
			break
		}
		if strings.Contains(l, "=") {
			insert = i + 1
		}
	}
	out := append([]string{}, lines[:insert]...)
	out = append(out, fmt.Sprintf("%-7s = %q", key, val))
	out = append(out, lines[insert:]...)
	return strings.Join(out, "\n")
}

// ---- device + storage profiles -----------------------------------------

// devicePresets are STARTING POINTS, not gospel: every field is editable in
// the form, because the next box out is one we've never seen.
var devicePresets = []map[string]any{
	{"id": "octatrack", "label": "Elektron Octatrack", "bit_depth": 16, "sample_rate": 44100,
		"channels": "stereo", "filesystem": "fat32", "mode": "card", "layout": "mirror",
		"max_files_per_dir": 1024, "max_filename_length": 32, "sanitize": true},
	{"id": "digitakt", "label": "Elektron Digitakt", "bit_depth": 16, "sample_rate": 48000,
		"channels": "mono", "mode": "staged", "layout": "mirror"},
	{"id": "digitakt-ii", "label": "Elektron Digitakt II", "bit_depth": 16, "sample_rate": 48000,
		"channels": "stereo", "mode": "staged", "layout": "mirror"},
	{"id": "syntakt", "label": "Elektron Syntakt", "bit_depth": 16, "sample_rate": 48000,
		"channels": "mono", "mode": "staged", "layout": "flatten", "max_duration_seconds": 5,
		"display_length": 16, "rename": "distinguishing-first"},
	{"id": "model-samples", "label": "Elektron Model:Samples", "bit_depth": 16, "sample_rate": 48000,
		"channels": "mono", "mode": "staged", "layout": "flatten",
		"display_length": 16, "rename": "distinguishing-first"},
	{"id": "sp404mk2", "label": "Roland SP-404MKII", "bit_depth": 16, "sample_rate": 48000,
		"channels": "stereo", "filesystem": "fat32", "mode": "card", "layout": "mirror"},
	{"id": "deluge", "label": "Synthstrom Deluge", "bit_depth": 24, "sample_rate": 44100,
		"channels": "stereo", "filesystem": "fat32", "mode": "card", "layout": "mirror"},
	{"id": "generic-card", "label": "Generic card sampler", "bit_depth": 16, "sample_rate": 44100,
		"channels": "stereo", "filesystem": "fat32", "mode": "card", "layout": "mirror"},
}

func (s *Server) presets(w http.ResponseWriter, _ *http.Request) {
	jsonOut(w, devicePresets)
}

func (s *Server) deviceWrite(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Name        string  `json:"name"`
		BitDepth    int     `json:"bit_depth"`
		SampleRate  int     `json:"sample_rate"`
		Channels    string  `json:"channels"`
		Downmix     string  `json:"downmix"`
		MaxDuration float64 `json:"max_duration_seconds"`
		Filesystem  string  `json:"filesystem"`
		Mode        string  `json:"mode"`
		Layout      string  `json:"layout"`
		MaxFiles    int     `json:"max_files_per_dir"`
		MaxName     int     `json:"max_filename_length"`
		MaxPath     int     `json:"max_path_length"`
		Sanitize    bool    `json:"sanitize"`
		DisplayLen  int     `json:"display_length"`
		Rename      string  `json:"rename"`
		Companions  bool    `json:"companions"`
		Overwrite   bool    `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if !nameOK.MatchString(d.Name) {
		jsonErr(w, 400, fmt.Errorf("device name must be lowercase letters, digits, - or _"))
		return
	}
	path := filepath.Join(s.ws.Root, "devices", d.Name+".toml")
	if _, err := os.Stat(path); err == nil && !d.Overwrite {
		jsonErr(w, 409, fmt.Errorf("device %q already exists", d.Name))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s — created in the mtunes UI. Every value here is a claim\n", d.Name)
	sb.WriteString("# about what the hardware accepts; check it against the manual.\n")
	fmt.Fprintf(&sb, "name = %q\n\n[audio]\nformat      = \"wav\"\n", d.Name)
	fmt.Fprintf(&sb, "bit_depth   = %d\nsample_rate = %d\nchannels    = %q\n", d.BitDepth, d.SampleRate, d.Channels)
	if d.Downmix != "" {
		fmt.Fprintf(&sb, "downmix     = %q\n", d.Downmix)
	}
	if d.MaxDuration > 0 {
		fmt.Fprintf(&sb, "max_duration_seconds = %g\n", d.MaxDuration)
	}
	if d.MaxFiles > 0 || d.MaxName > 0 || d.MaxPath > 0 || d.Sanitize || d.DisplayLen > 0 {
		sb.WriteString("\n[naming]\n")
		if d.DisplayLen > 0 {
			fmt.Fprintf(&sb, "display_length      = %d\n", d.DisplayLen)
			if d.Rename != "" {
				fmt.Fprintf(&sb, "rename              = %q\n", d.Rename)
			}
		}
		if d.MaxFiles > 0 {
			fmt.Fprintf(&sb, "max_files_per_dir   = %d\n", d.MaxFiles)
		}
		if d.MaxName > 0 {
			fmt.Fprintf(&sb, "max_filename_length = %d\n", d.MaxName)
		}
		if d.MaxPath > 0 {
			fmt.Fprintf(&sb, "max_path_length     = %d\n", d.MaxPath)
		}
		if d.Sanitize {
			sb.WriteString("allowed_chars       = \"A-Za-z0-9 ._()-\"\n")
			sb.WriteString("case_sensitive      = false\n")
			sb.WriteString("sanitize            = { \"#\" = \"s\", \"&\" = \"and\", \"'\" = \"\" }\n")
		}
	}
	if d.Filesystem != "" {
		fmt.Fprintf(&sb, "\n[filesystem]\ntype = %q\n", d.Filesystem)
	}
	fmt.Fprintf(&sb, "\n[delivery]\nmode   = %q\nlayout = %q\n", d.Mode, d.Layout)
	if d.Companions {
		sb.WriteString("\n# Ableton documents ride along with their sample refs rewritten to where the\n")
		sb.WriteString("# samples landed. anchor = \"user-library\" assumes the recipe target is\n")
		sb.WriteString("# <User Library>/<user_library_prefix> — racks then resolve on Push too.\n")
		sb.WriteString("[companions]\ntypes  = [\"adg\", \"adv\", \"als\"]\nanchor = \"user-library\"\nuser_library_prefix = \"Samples\"\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		jsonErr(w, 500, err)
		return
	}
	if _, err := profile.LoadDevice(s.ws.Root, d.Name); err != nil {
		jsonErr(w, 400, fmt.Errorf("wrote %s.toml but it is invalid: %w", d.Name, err))
		return
	}
	jsonOut(w, map[string]string{"status": "ok", "device": d.Name})
}

func (s *Server) storageWrite(w http.ResponseWriter, r *http.Request) {
	var st struct {
		Name      string `json:"name"`
		Capacity  int64  `json:"capacity_bytes"`
		Reserve   string `json:"reserve"`
		Cluster   int64  `json:"cluster_bytes"`
		Kind      string `json:"kind"`
		MaxFiles  int    `json:"max_files"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		jsonErr(w, 400, err)
		return
	}
	if !nameOK.MatchString(st.Name) {
		jsonErr(w, 400, fmt.Errorf("storage name must be lowercase letters, digits, - or _"))
		return
	}
	if st.Kind == "" {
		st.Kind = "filesystem"
	}
	path := filepath.Join(s.ws.Root, "storage", st.Name+".toml")
	if _, err := os.Stat(path); err == nil && !st.Overwrite {
		jsonErr(w, 409, fmt.Errorf("storage %q already exists", st.Name))
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s — created in the mtunes UI.\nname           = %q\nkind           = %q\n",
		st.Name, st.Name, st.Kind)
	fmt.Fprintf(&sb, "capacity_bytes = %d\n", st.Capacity)
	if st.Reserve != "" {
		fmt.Fprintf(&sb, "reserve        = %q\n", st.Reserve)
	}
	if st.Cluster > 0 {
		fmt.Fprintf(&sb, "cluster_bytes  = %d\n", st.Cluster)
	}
	if st.MaxFiles > 0 {
		fmt.Fprintf(&sb, "max_files      = %d\n", st.MaxFiles)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		jsonErr(w, 500, err)
		return
	}
	jsonOut(w, map[string]string{"status": "ok", "storage": st.Name})
}

func (s *Server) storages(w http.ResponseWriter, _ *http.Request) {
	type sto struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Capacity int64  `json:"capacity_bytes"`
		Reserve  string `json:"reserve,omitempty"`
	}
	var out []sto
	files, _ := filepath.Glob(filepath.Join(s.ws.Root, "storage", "*.toml"))
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".toml")
		p, err := profile.LoadStorage(s.ws.Root, name)
		if err != nil {
			continue
		}
		out = append(out, sto{name, p.Kind, p.CapacityBytes, p.Reserve})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	jsonOut(w, out)
}

// Volume is one mounted filesystem a storage profile could describe.
type Volume struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Capacity int64  `json:"capacity_bytes"`
	Free     int64  `json:"free_bytes"`
}

// volumes lists mounted filesystems, so making a storage profile for a card
// means picking it rather than looking up its true capacity by hand. The
// enumeration is per-OS (volumes_*.go): /Volumes on macOS, the usual
// removable-media roots on Linux, drive letters on Windows.
func (s *Server) volumes(w http.ResponseWriter, _ *http.Request) {
	out := listVolumes()
	if out == nil {
		out = []Volume{}
	}
	jsonOut(w, out)
}
