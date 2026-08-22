// Package profile loads device and storage profiles from the workspace.
// A device profile is the format contract a sampler imposes; a storage
// profile is one physical card/drive or a device-managed quota. Both are
// deliberately dumb data — all behavior lives in plan/materialize.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

type Device struct {
	Name       string     `toml:"name" json:"name"`
	Audio      Audio      `toml:"audio" json:"audio"`
	Naming     Naming     `toml:"naming" json:"naming"`
	Filesystem Filesystem `toml:"filesystem" json:"filesystem"`
	Delivery   Delivery   `toml:"delivery" json:"delivery"`
	Companions Companions `toml:"companions" json:"companions"`
}

// Companions: non-audio files that ride along with the samples because
// they reference them — Ableton drum racks (.adg), presets (.adv), sets
// (.als). They are gzipped XML carrying sample paths, so materialize
// rewrites those paths to where the samples landed. Empty Types = drop
// them (hardware samplers). Anchor is how the rewritten paths resolve:
// "user-library" (the target sits inside the Live User Library, at
// UserLibraryPrefix — racks then resolve on any machine or Push that
// holds the library) or "document" (relative to the companion file
// itself). .alp is a pack installer, never a companion.
type Companions struct {
	Types             []string `toml:"types" json:"types,omitempty"`
	Anchor            string   `toml:"anchor" json:"anchor,omitempty"`
	UserLibraryPrefix string   `toml:"user_library_prefix" json:"user_library_prefix,omitempty"`
}

// Companion reports whether a path's extension is a companion type for
// this device.
func (d *Device) Companion(p string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
	for _, t := range d.Companions.Types {
		if t == ext {
			return true
		}
	}
	return false
}

// PathType is the Ableton RelativePathType to write for the anchor.
func (c Companions) PathType() string {
	if c.Anchor == "document" {
		return "3"
	}
	return "5"
}

type Audio struct {
	Format             string  `toml:"format" json:"format"`
	BitDepth           int     `toml:"bit_depth" json:"bit_depth"`
	SampleRate         int     `toml:"sample_rate" json:"sample_rate"`
	Channels           string  `toml:"channels" json:"channels"` // "mono" folds; "stereo" preserves source channels
	Downmix            string  `toml:"downmix" json:"downmix"`
	MaxDurationSeconds float64 `toml:"max_duration_seconds" json:"max_duration_seconds,omitempty"` // 0 = unlimited

	// DualMono: what to do with 2-channel sources whose channels are
	// identical (catalog verdict). Mono devices always take one channel
	// (no −3 dB pad — the pad is for summing two different signals). For
	// stereo-preserving devices: "keep" (default) renders them as the
	// stereo they claim to be; "fold" writes them mono — lossless, half the
	// bytes, and what the sound actually is.
	DualMono string `toml:"dual_mono" json:"dual_mono,omitempty"`
}

type Naming struct {
	MaxFilesPerDir    int               `toml:"max_files_per_dir" json:"max_files_per_dir,omitempty"`     // 0 = unlimited
	MaxFilenameLength int               `toml:"max_filename_length" json:"max_filename_length,omitempty"` // warn-level; 0 = unlimited
	AllowedChars      string            `toml:"allowed_chars" json:"allowed_chars,omitempty"`             // regex char class body; "" = anything
	MaxPathLength     int               `toml:"max_path_length" json:"max_path_length,omitempty"`         // warn-level; 0 = unlimited
	CaseSensitive     bool              `toml:"case_sensitive" json:"case_sensitive"`
	Sanitize          map[string]string `toml:"sanitize" json:"sanitize,omitempty"` // char → replacement, applied to output paths at plan time

	// DisplayLength: how many characters of a filename the device's browser
	// actually shows before cropping (Syntakt's list view). 0 = unknown.
	// With it set, plan warns about names that are identical within it.
	DisplayLength int `toml:"display_length" json:"display_length,omitempty"`
	// Rename: "" (never) | "distinguishing-first" — for names indistinct
	// within DisplayLength, move the tokens that differ to the front so the
	// crop shows them ("BD A 808 Decay A 01" → "01 BD A 808 Decay A").
	// Deterministic; pinned in the lock like every other output path.
	Rename string `toml:"rename" json:"rename,omitempty"`
}

type Filesystem struct {
	Type string `toml:"type" json:"type,omitempty"`
}

type Delivery struct {
	Mode   string `toml:"mode" json:"mode"`     // "card" | "staged"
	Layout string `toml:"layout" json:"layout"` // "mirror" (default) | "flatten" — flat devices (Syntakt: 64 slots, no folders)
}

func LoadDevice(workspaceRoot, name string) (*Device, error) {
	path := filepath.Join(workspaceRoot, "devices", name+".toml")
	var d Device
	if err := loadTOML(path, &d); err != nil {
		return nil, err
	}
	if d.Name == "" {
		d.Name = name
	}
	if d.Audio.Downmix == "" {
		d.Audio.Downmix = "sum-3db"
	}
	if d.Delivery.Mode == "" {
		d.Delivery.Mode = "card"
	}
	if d.Audio.Format != "wav" {
		return nil, fmt.Errorf("device %s: only wav output is supported in v0 (got %q)", name, d.Audio.Format)
	}
	if d.Audio.BitDepth != 16 && d.Audio.BitDepth != 24 {
		return nil, fmt.Errorf("device %s: bit_depth must be 16 or 24", name)
	}
	if d.Audio.SampleRate <= 0 {
		return nil, fmt.Errorf("device %s: sample_rate is required", name)
	}
	if d.Audio.Channels != "mono" && d.Audio.Channels != "stereo" {
		return nil, fmt.Errorf("device %s: channels must be mono or stereo", name)
	}
	switch d.Audio.Downmix {
	case "sum-3db", "sum", "left", "right":
	default:
		return nil, fmt.Errorf("device %s: unknown downmix %q", name, d.Audio.Downmix)
	}
	if d.Delivery.Mode != "card" && d.Delivery.Mode != "staged" {
		return nil, fmt.Errorf("device %s: delivery.mode must be card or staged", name)
	}
	if d.Delivery.Layout == "" {
		d.Delivery.Layout = "mirror"
	}
	if d.Delivery.Layout != "mirror" && d.Delivery.Layout != "flatten" {
		return nil, fmt.Errorf("device %s: delivery.layout must be mirror or flatten", name)
	}
	if d.Naming.AllowedChars != "" {
		if _, err := d.Naming.DisallowedRe(); err != nil {
			return nil, fmt.Errorf("device %s: allowed_chars: %w", name, err)
		}
	}
	switch d.Audio.DualMono {
	case "", "keep", "fold":
	default:
		return nil, fmt.Errorf("device %s: audio.dual_mono must be keep or fold", name)
	}
	for i, t := range d.Companions.Types {
		t = strings.ToLower(strings.TrimPrefix(t, "."))
		d.Companions.Types[i] = t
		switch t {
		case "adg", "adv", "als":
		case "alp":
			return nil, fmt.Errorf("device %s: companions.types: .alp is a pack installer, not a document — install it in Live", name)
		default:
			return nil, fmt.Errorf("device %s: companions.types: unknown type %q (adg, adv, als)", name, t)
		}
	}
	switch d.Companions.Anchor {
	case "":
		if len(d.Companions.Types) > 0 {
			d.Companions.Anchor = "user-library"
		}
	case "user-library", "document":
	default:
		return nil, fmt.Errorf("device %s: companions.anchor must be user-library or document", name)
	}
	if d.Companions.Anchor == "user-library" && d.Companions.UserLibraryPrefix == "" {
		d.Companions.UserLibraryPrefix = "Samples"
	}
	d.Companions.UserLibraryPrefix = strings.Trim(strings.ReplaceAll(d.Companions.UserLibraryPrefix, "\\", "/"), "/")
	switch d.Naming.Rename {
	case "", "distinguishing-first":
	default:
		return nil, fmt.Errorf("device %s: naming.rename must be empty or distinguishing-first", name)
	}
	if d.Naming.Rename != "" && d.Naming.DisplayLength <= 0 {
		return nil, fmt.Errorf("device %s: naming.rename needs naming.display_length (the crop it works around)", name)
	}
	for k, v := range d.Naming.Sanitize {
		if utf8.RuneCountInString(k) != 1 || k == "/" {
			return nil, fmt.Errorf("device %s: naming.sanitize key %q must be a single non-slash character", name, k)
		}
		if strings.Contains(v, "/") {
			return nil, fmt.Errorf("device %s: naming.sanitize replacement %q must not contain '/'", name, v)
		}
	}
	return &d, nil
}

// DisallowedRe compiles a regexp matching any character NOT in AllowedChars.
func (n Naming) DisallowedRe() (*regexp.Regexp, error) {
	return regexp.Compile("[^" + n.AllowedChars + "]")
}

type Storage struct {
	Name          string `toml:"name" json:"name"`
	Kind          string `toml:"kind" json:"kind"` // "filesystem" | "quota"
	CapacityBytes int64  `toml:"capacity_bytes" json:"capacity_bytes"`
	Reserve       string `toml:"reserve" json:"reserve,omitempty"`             // "10%" or bytes; filesystem only
	ClusterBytes  int64  `toml:"cluster_bytes" json:"cluster_bytes,omitempty"` // filesystem only
	MaxFiles      int    `toml:"max_files" json:"max_files,omitempty"`         // quota only; 0 = unlimited
}

func LoadStorage(workspaceRoot, name string) (*Storage, error) {
	path := filepath.Join(workspaceRoot, "storage", name+".toml")
	var s Storage
	if err := loadTOML(path, &s); err != nil {
		return nil, err
	}
	if s.Name == "" {
		s.Name = name
	}
	switch s.Kind {
	case "filesystem":
		if s.ClusterBytes == 0 {
			s.ClusterBytes = 32768
		}
		if s.Reserve == "" {
			s.Reserve = "10%" // max fill is a policy, not an accident
		}
		if _, err := s.ReserveBytes(); err != nil {
			return nil, fmt.Errorf("storage %s: %w", name, err)
		}
	case "quota":
	default:
		return nil, fmt.Errorf("storage %s: kind must be filesystem or quota", name)
	}
	if s.CapacityBytes <= 0 {
		return nil, fmt.Errorf("storage %s: capacity_bytes is required", name)
	}
	return &s, nil
}

func (s *Storage) ReserveBytes() (int64, error) {
	if s.Kind != "filesystem" || s.Reserve == "" {
		return 0, nil
	}
	r := strings.TrimSpace(s.Reserve)
	if pct, ok := strings.CutSuffix(r, "%"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || f < 0 || f >= 100 {
			return 0, fmt.Errorf("bad reserve %q", s.Reserve)
		}
		return int64(float64(s.CapacityBytes) * f / 100), nil
	}
	b, err := strconv.ParseInt(r, 10, 64)
	if err != nil || b < 0 {
		return 0, fmt.Errorf("bad reserve %q", s.Reserve)
	}
	return b, nil
}

// UsableBytes is what a selection may occupy.
func (s *Storage) UsableBytes() int64 {
	reserve, _ := s.ReserveBytes() // validated at load
	return s.CapacityBytes - reserve
}

func loadTOML(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no such profile: %s", path)
		}
		return err
	}
	if err := toml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	return nil
}
