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

	"github.com/BurntSushi/toml"
)

type Device struct {
	Name       string     `toml:"name" json:"name"`
	Audio      Audio      `toml:"audio" json:"audio"`
	Naming     Naming     `toml:"naming" json:"naming"`
	Filesystem Filesystem `toml:"filesystem" json:"filesystem"`
	Delivery   Delivery   `toml:"delivery" json:"delivery"`
}

type Audio struct {
	Format             string  `toml:"format" json:"format"`
	BitDepth           int     `toml:"bit_depth" json:"bit_depth"`
	SampleRate         int     `toml:"sample_rate" json:"sample_rate"`
	Channels           string  `toml:"channels" json:"channels"` // "mono" folds; "stereo" preserves source channels
	Downmix            string  `toml:"downmix" json:"downmix"`
	MaxDurationSeconds float64 `toml:"max_duration_seconds" json:"max_duration_seconds,omitempty"` // 0 = unlimited
}

type Naming struct {
	MaxFilesPerDir    int    `toml:"max_files_per_dir" json:"max_files_per_dir,omitempty"`     // 0 = unlimited
	MaxFilenameLength int    `toml:"max_filename_length" json:"max_filename_length,omitempty"` // warn-level; 0 = unlimited
	AllowedChars      string `toml:"allowed_chars" json:"allowed_chars,omitempty"`             // regex char class body; "" = anything
	MaxPathLength     int    `toml:"max_path_length" json:"max_path_length,omitempty"`         // warn-level; 0 = unlimited
	CaseSensitive     bool   `toml:"case_sensitive" json:"case_sensitive"`
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
