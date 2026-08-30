// Package lock reads and writes lockfiles: the machine-written record of
// exactly what a materialization contained — source SHAs, transform args,
// output hashes, and snapshots of the profiles used. Locks are kept
// forever; restore-from-lock is the git-revert of this system. Recipes are
// TOML for humans; locks are JSON for machines.
package lock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jbarket/materialized-tunes/internal/profile"
)

type Lock struct {
	View         string            `json:"view"`
	Created      time.Time         `json:"created"`
	RecipeSHA256 string            `json:"recipe_sha256"`
	Layout       string            `json:"layout,omitempty"` // the recipe's layout template at the time ("" = mirror)
	Device       profile.Device    `json:"device"`           // snapshot — restores don't depend on current profiles
	Storage      profile.Storage   `json:"storage"`          // snapshot
	Tooling      map[string]string `json:"tooling"`
	Entries      []Entry           `json:"entries"`
	Totals       Totals            `json:"totals"`
}

type Entry struct {
	Source    Source    `json:"source"`
	Transform Transform `json:"transform"`
	Output    Output    `json:"output"`
}

type Source struct {
	Location string `json:"location"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
}

type Transform struct {
	FFmpegArgs []string `json:"ffmpeg_args"`
	Copy       bool     `json:"copy,omitempty"` // byte-for-byte copy of the source; FFmpegArgs empty
	// Companion: an Ableton document whose sample refs were rewritten.
	// Refs maps each reference as the source wrote it to the output path
	// it now points at, so restore replays the exact rewrite.
	Companion bool              `json:"companion,omitempty"`
	Refs      map[string]string `json:"refs,omitempty"`
}

type Output struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Totals struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Write stores the lock under locks/<view>/<utc-timestamp>.lock.json and
// returns the path.
func Write(workspaceRoot string, l *Lock) (string, error) {
	dir := filepath.Join(workspaceRoot, "locks", l.View)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := l.Created.UTC().Format("20060102T150405Z")
	path := filepath.Join(dir, name+".lock.json")
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s.%d.lock.json", name, i))
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func Read(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &l, nil
}

// Resolve turns a lock argument into a path: a real file path is used
// as-is; otherwise it is treated as a view name and the newest lock for
// that view is returned.
func Resolve(workspaceRoot, arg string) (string, error) {
	if info, err := os.Stat(arg); err == nil && !info.IsDir() {
		return arg, nil
	}
	dir := filepath.Join(workspaceRoot, "locks", arg)
	matches, err := filepath.Glob(filepath.Join(dir, "*.lock.json"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("no lock file at %q and no locks for a view of that name", arg)
	}
	sort.Strings(matches) // timestamp names sort chronologically
	return matches[len(matches)-1], nil
}
