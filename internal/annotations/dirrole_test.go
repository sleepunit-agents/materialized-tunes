package annotations

import "testing"

// PackDirRoleAt reads a [[dir]] role at any depth — whole-path, case-folded,
// globs allowed — and PackDirRole stays a top-level-only view of the same
// map, so a nested entry never changes what the top segment is.
func TestPackDirRoleAt(t *testing.T) {
	p := &Pack{Dirs: []Dir{
		{Path: "1. Modular Loops (120 BPM)/WAV", Role: "canonical-audio"},
		{Path: "1. Modular Loops (120 BPM)/Apple Loops", Role: "format-tree"},
		{Path: "2. Modular Drones", Category: "one-shots"},
		{Path: "4. Modular Instruments", Role: "format-tree"},
		{Path: "Kits/* 16 bit mono", Role: "format-tree"},
	}}
	cases := []struct {
		rel     string
		role    string
		claimed bool
	}{
		{"1. Modular Loops (120 BPM)/WAV", "canonical-audio", true},
		{"1. modular loops (120 bpm)/apple loops", "format-tree", true},
		{"1. Modular Loops (120 BPM)", "", false},      // the parent is not annotated
		{"1. Modular Loops (120 BPM)/REX2", "", false}, // nor its unannotated sibling
		{"2. Modular Drones", "", true},                // claimed, no role: content
		{"4. Modular Instruments", "format-tree", true},
		{"Kits/Thump 16 bit mono", "format-tree", true},
		{"Kits/Thump 24 bit stereo", "", false},
	}
	for _, c := range cases {
		role, claimed := PackDirRoleAt(p, c.rel)
		if role != c.role || claimed != c.claimed {
			t.Errorf("PackDirRoleAt(%q) = %q,%v want %q,%v", c.rel, role, claimed, c.role, c.claimed)
		}
	}
	if role, claimed := PackDirRole(p, "1. Modular Loops (120 BPM)"); claimed || role != "" {
		t.Errorf("PackDirRole on the unannotated parent = %q,%v; nested entries must not claim it", role, claimed)
	}
	if _, claimed := PackDirRoleAt(nil, "x"); claimed {
		t.Error("nil pack claimed a dir")
	}
}
