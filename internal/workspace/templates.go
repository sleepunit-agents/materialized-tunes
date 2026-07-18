package workspace

import (
	"os"
	"path/filepath"
)

// Starter profiles written by Init when missing — real devices with
// manual-verified constraints (see SPEC.md §13), ready to edit.
var templates = map[string]string{
	"devices/syntakt.toml": `# Elektron Syntakt — SP Twinshot sample support (OS 1.40+).
# Facts from the Syntakt User Manual §6.2.4.
name = "syntakt"

[audio]
format               = "wav"
bit_depth            = 16
sample_rate          = 48000
channels             = "mono"
downmix              = "sum-3db"   # sum-3db | sum | left | right
max_duration_seconds = 5.0         # device hard limit; longer sources are excluded

[delivery]
mode   = "staged"    # materialize writes a folder you drag into Elektron Transfer
layout = "flatten"   # the Syntakt has no folders — 64 flat slots; colliding
                     # names get parent-dir prefixes automatically
`,
	"devices/octatrack.toml": `# Elektron Octatrack. 44.1kHz is the only rate it loads; 16-bit keeps
# every file usable by both static and flex machines (static is 16-bit only).
name = "octatrack"

[audio]
format      = "wav"
bit_depth   = 16
sample_rate = 44100
channels    = "stereo"     # preserves source channel count
downmix     = "sum-3db"    # used only if channels = "mono"

[naming]
max_files_per_dir   = 1024   # audio pool folder limit (manual)
# Heuristics — no documented hard limits exist; tune if the OT disagrees:
max_filename_length = 32
allowed_chars       = "A-Za-z0-9 ._()-"
max_path_length     = 120
case_sensitive      = false

[filesystem]
type = "fat32"

[delivery]
mode = "card"
`,
	"storage/syntakt-plusdrive.toml": `# Syntakt sample memory: 64 slots, 32 MB, global across all projects.
name           = "syntakt-plusdrive"
kind           = "quota"
capacity_bytes = 33554432
max_files      = 64
`,
	"storage/octatrack-cf.toml": `# The 32GB CF card. Set capacity_bytes from the real card:
#   diskutil info /Volumes/YOURCARD | grep "Container Total Space"
# Marketing "32GB" is a lie; this placeholder assumes ~29.7 GiB usable.
name           = "octatrack-cf"
kind           = "filesystem"
capacity_bytes = 31914983424
reserve        = "10%"      # headroom for .ot sidecars and OT recordings
cluster_bytes  = 32768      # FAT32 allocation unit
`,
	"views/EXAMPLE.toml.example": `# Rename to <name>.toml and edit. A view = device + storage + selection.
name    = "st-drums"
device  = "syntakt"
storage = "syntakt-plusdrive"

[[include]]
location = "one"
glob     = "samples-from-mars/808 From Mars/WAV/**"
as       = "808"          # optional: output prefix replacing the glob's static root

[[exclude]]
glob = "**/Ableton*/**"
`,
}

func writeTemplates(root string) error {
	for rel, content := range templates {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			continue // never clobber user edits
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
