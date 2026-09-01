package plan

import (
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
)

// The 2026-09-01 Greystate/Thump report (t-343 follow-up): one stranger on
// an output path used to poison the whole cut group, because isCutSet was
// asked about the group instead of each coordinate. Two real shapes:
//
//   - a Step-Kit tree re-files the pack's own drum hits under kit folders,
//     byte-identical to the 16-bit-mono cuts — the kit copy shares the
//     trio's output path but not its coordinate, the whole group was
//     refused, and the three real cuts fell through to a collision while
//     the kit copy slipped away behind a disambiguation prefix;
//   - one basename lives in two folders of every tree (Thump's artificial/
//     and layered/ kick_thick) — six entries, tree-dup, same refusal.
//
// Only drums collided in the field because step kits are drum kits.
func TestPickCutsStrangerOnThePath(t *testing.T) {
	ann := map[string]string{
		"annotations/vendors/polyend/vendor.toml": `[vendor]
name = "Polyend"
slug = "polyend"
[formats]
canonical_dir = "* 24 bit stereo"
parallel_dirs = ["* 16 bit mono", "* 16 bit stereo", "*Step Kits*"]
[[category]]
id    = "one-shots"
match = ["* 24 bit stereo", "* 16 bit stereo", "* 16 bit mono", "*Step Kits*"]
`,
		"annotations/vendors/polyend/packs/greystate.toml": `[pack]
name = "Greystate"
slug = "greystate"
dir  = "Greystate"
[[dir]]
path = "Greystate 16 bit mono"
role = "format-tree"
[[dir]]
path = "Greystate 16 bit stereo"
role = "format-tree"
[[dir]]
path = "Greystate 24 bit stereo"
role = "canonical-audio"
category = "one-shots"
[[dir]]
path = "Step Kits 16 bit mono"
role = "format-tree"
category = "one-shots"
`,
		"annotations/vendors/polyend/packs/thump.toml": `[pack]
name = "Thump"
slug = "thump"
dir  = "Thump"
[[dir]]
path = "Thump 16 bit mono"
role = "format-tree"
[[dir]]
path = "Thump 16 bit stereo"
role = "format-tree"
[[dir]]
path = "Thump 24 bit stereo"
role = "canonical-audio"
category = "one-shots"
`,
		"annotations/instruments.toml": `[[instrument]]
id      = "kick"
family  = "drums"
aliases = ["kick", "kicks"]
[[instrument]]
id      = "clap"
family  = "drums"
aliases = ["clap", "claps"]
`,
	}

	var entries []catalog.Entry
	trio := func(pack, rel string, frames int64) {
		entries = append(entries,
			wavEntry("Polyend/"+pack+"/"+pack+" 24 bit stereo/"+rel, 2, 44100, 24, frames),
			wavEntry("Polyend/"+pack+"/"+pack+" 16 bit stereo/"+rel, 2, 44100, 16, frames),
			wavEntry("Polyend/"+pack+"/"+pack+" 16 bit mono/"+rel, 1, 44100, 16, frames),
		)
	}
	trio("Greystate", "Clap/Clap_Code.wav", 44100)
	// the stranger: a kit-folder copy, byte-identical to the 16-bit-mono cut
	kit := wavEntry("Polyend/Greystate/Step Kits 16 bit mono/Kit 1/Clap_Code.wav", 1, 44100, 16, 44100)
	kit.SHA256 = entries[2].SHA256
	entries = append(entries, kit)
	trio("Thump", "artificial/kick_thick.wav", 50000)
	trio("Thump", "layered/kick_thick.wav", 50000)

	ws := testWorkspace(t, entries, ann)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := harvest.Run(ws, ws.Config.Locations[0]); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", `name="v"
device="dev"
storage="sq"
layout="{family}/{instrument}/{category}/{pack}/{file}"
dedup="content"
[[include]]
location="src"
glob="**"
`)
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	// 3 survivors: the 24-bit clap, and both Thump kicks disambiguated.
	if len(p.Entries) != 3 {
		t.Fatalf("%d entries, want 3: %+v", len(p.Entries), p.Entries)
	}
	byOut := map[string]string{}
	for _, e := range p.Entries {
		byOut[e.OutPath] = e.SourcePath
	}
	if src, ok := byOut["Drums/Clap/One-Shots/Greystate/Clap_Code.wav"]; !ok || !strings.Contains(src, "24 bit stereo") {
		t.Errorf("clap: got %v, want the clean name kept from the 24-bit tree", byOut)
	}
	for _, want := range []string{
		"Drums/Kick/One-Shots/Thump/artificial - kick_thick.wav",
		"Drums/Kick/One-Shots/Thump/layered - kick_thick.wav",
	} {
		if _, ok := byOut[want]; !ok {
			t.Errorf("missing %s in %v", want, byOut)
		}
	}
	// The kit copy is a dropped cut, not a shipped stray: trio + kit + two
	// thump pairs = 3+1+2+2 dropped.
	if p.CutsDropped != 7 {
		t.Errorf("CutsDropped = %d, want 7", p.CutsDropped)
	}
}
