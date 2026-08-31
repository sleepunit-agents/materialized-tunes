// Package view loads view recipes: the human-edited TOML that selects
// which sources materialize for which device onto which storage. A view is
// a query; the lockfile a materialization writes is the pin.
package view

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/bmatcuk/doublestar/v4"
)

type View struct {
	Name    string `toml:"name" json:"name"`
	Device  string `toml:"device" json:"device"`
	Storage string `toml:"storage" json:"storage"`
	Target  string `toml:"target" json:"target,omitempty"` // default materialize destination; --to overrides. "~/" expands.
	Limit   int    `toml:"limit" json:"limit,omitempty"`   // keep only the first N eligible files (by output path); 0 = all

	// FormatTree: what to do with a vendor's format-tree level (SFM's "WAV/",
	// Polyend's "<Pack> 24 bit stereo/") in output paths. "strip" (default)
	// drops it — it carries no musical information and costs a tap on every
	// browser; "keep" mirrors the source verbatim. Needs annotations to know
	// the vendor; unknown vendors are always mirrored.
	FormatTree string `toml:"format_tree" json:"format_tree,omitempty"`

	// Dedup: "" (default — every selected source renders, even when two
	// paths hold identical bytes; a DAW kit folder wants its members) or
	// "content" — identical audio (same SHA) renders once, at the first
	// output path in sort order. For slot- and card-bound devices where a
	// duplicate is a wasted slot.
	Dedup string `toml:"dedup" json:"dedup,omitempty"`

	// Cuts: "" / "best" (default) — when a pack ships one sample under
	// several format trees (Polyend's 24-bit-stereo / 16-bit-stereo /
	// 16-bit-mono cuts of the same one-shots), only the cut this device
	// takes best renders; or "all" — every cut renders, which under
	// format_tree = "strip" means they collide.
	Cuts string `toml:"cuts" json:"cuts,omitempty"`

	// Layout: "" (default — mirror: source-relative paths under each
	// include's `as` prefix) or a template over the tokens in
	// ParseLayout's doc, e.g. "{family}/{instrument}/{category}/{pack}/{file}".
	// When set, the template decides every output path and `as` is ignored.
	Layout string `toml:"layout" json:"layout,omitempty"`

	Include []Include `toml:"include" json:"include"`
	Exclude []Exclude `toml:"exclude" json:"exclude,omitempty"`
}

type Include struct {
	Location string `toml:"location" json:"location"`
	Glob     string `toml:"glob" json:"glob"`
	As       string `toml:"as" json:"as,omitempty"` // optional output prefix replacing the glob's static root
}

type Exclude struct {
	Glob string `toml:"glob" json:"glob"`
}

// Load reads a recipe and refuses one with no rules — everything that
// materializes goes through here.
func Load(workspaceRoot, name string) (*View, error) {
	v, err := LoadRaw(workspaceRoot, name)
	if err != nil {
		return nil, err
	}
	if len(v.Include) == 0 {
		return nil, fmt.Errorf("view %s: at least one [[include]] is required", name)
	}
	return v, nil
}

// LoadRaw is Load without the "at least one rule" requirement. The UI edits
// a recipe by checking and unchecking vendors, so a recipe momentarily
// emptied of rules is a legitimate state to load, list and pre-flight — it
// just cannot be materialized.
func LoadRaw(workspaceRoot, name string) (*View, error) {
	path := filepath.Join(workspaceRoot, "views", name+".toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no such view: %s", path)
		}
		return nil, err
	}
	var v View
	if err := toml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if v.Name == "" {
		v.Name = name
	}
	if v.Device == "" || v.Storage == "" {
		return nil, fmt.Errorf("view %s: device and storage are required", name)
	}
	switch v.FormatTree {
	case "", "strip", "keep":
	default:
		return nil, fmt.Errorf("view %s: format_tree must be strip or keep", name)
	}
	switch v.Dedup {
	case "", "content":
	default:
		return nil, fmt.Errorf("view %s: dedup must be empty or content", name)
	}
	switch v.Cuts {
	case "", "best", "all":
	default:
		return nil, fmt.Errorf("view %s: cuts must be best or all", name)
	}
	if _, err := ParseLayout(v.Layout); err != nil {
		return nil, fmt.Errorf("view %s: %w", name, err)
	}
	for i, inc := range v.Include {
		if inc.Location == "" || inc.Glob == "" {
			return nil, fmt.Errorf("view %s: include %d needs location and glob", name, i+1)
		}
		if !doublestar.ValidatePattern(inc.Glob) {
			return nil, fmt.Errorf("view %s: bad glob %q", name, inc.Glob)
		}
	}
	for i, exc := range v.Exclude {
		if !doublestar.ValidatePattern(exc.Glob) {
			return nil, fmt.Errorf("view %s: bad exclude glob %d: %q", name, i+1, exc.Glob)
		}
	}
	return &v, nil
}

// GlobRoot returns the static directory prefix of a glob — the segments
// before the first one containing a metacharacter — including a trailing
// slash ("a/b/**" → "a/b/"). Empty if the glob starts with a pattern.
func GlobRoot(glob string) string {
	root := ""
	for _, seg := range splitKeep(glob) {
		if containsMeta(seg) {
			break
		}
		root += seg
	}
	return root
}

func splitKeep(path string) []string {
	var out []string
	start := 0
	for i, r := range path {
		if r == '/' {
			out = append(out, path[start:i+1])
			start = i + 1
		}
	}
	if start < len(path) {
		out = append(out, path[start:])
	}
	return out
}

func containsMeta(s string) bool {
	for _, r := range s {
		switch r {
		case '*', '?', '[', '{':
			return true
		}
	}
	return false
}
