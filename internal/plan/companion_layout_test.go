package plan

import (
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
)

// TestLayoutCompanionInherits: under a layout template a Live document
// has no harvested facts of its own — it lands where the samples it
// references landed. A Sampler multisample follows its zone map's files
// (all one instrument); a drum kit spanning kick/snare/hat is a drums
// thing, so {instrument} is the family catch-all; a document none of
// whose refs are in the catalog falls to _Unsorted and is named in a
// warning.
func TestLayoutCompanionInherits(t *testing.T) {
	doc := func(path string, refs ...string) catalog.Entry {
		d := &ableton.Doc{}
		for _, r := range refs {
			d.Refs = append(d.Refs, ableton.Ref{Rel: r, Name: r[strings.LastIndex(r, "/")+1:], Type: "3"})
		}
		return catalog.Entry{Path: path, Size: 1000, SHA256: "dd" + path, Doc: d}
	}
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("MS10 From Mars/WAV/SuperPulse/SuperPulse C0.wav", 1, 48000, 16, 4800),
		wavEntry("MS10 From Mars/WAV/SuperPulse/SuperPulse C1.wav", 1, 48000, 16, 4800),
		wavEntry("MS10 From Mars/WAV/SuperPulse/SuperPulse C2.wav", 1, 48000, 16, 4800),
		wavEntry("MS10 From Mars/WAV/Kit/Kick.wav", 1, 48000, 16, 4800),
		wavEntry("MS10 From Mars/WAV/Kit/Snare.wav", 1, 48000, 16, 4800),
		wavEntry("MS10 From Mars/WAV/Kit/Hat.wav", 1, 48000, 16, 4800),
		doc("MS10 From Mars/Ableton Live/MS10 From Mars/Presets/Leads/SuperPulse.adg",
			"../../../../WAV/SuperPulse/SuperPulse C0.wav", "../../../../WAV/SuperPulse/SuperPulse C1.wav", "../../../../WAV/SuperPulse/SuperPulse C2.wav"),
		doc("MS10 From Mars/Ableton Live/MS10 From Mars/Presets/Kits/Clean Kit.adg",
			"../../../../WAV/Kit/Kick.wav", "../../../../WAV/Kit/Snare.wav", "../../../../WAV/Kit/Hat.wav"),
		doc("MS10 From Mars/Ableton Live/MS10 From Mars/Presets/FX/Blind.adg",
			"../../../../WAV/Nowhere/Missing.wav"),
	}, map[string]string{
		"annotations/vendors/sfm/vendor.toml": "[vendor]\nname=\"Samples From Mars\"\nslug=\"sfm\"\n[formats]\ncanonical=[\"WAV\"]\nparallel_dirs=[\"Ableton Live*\"]\n",
		"annotations/instruments.toml":        "[[family]]\nid=\"synth\"\nflat=false\n",
		"annotations-cache/meta/src.jsonl": strings.Join([]string{
			`{"path":"MS10 From Mars/WAV/SuperPulse/SuperPulse C0.wav","category":"multisamples","instrument":"lead","family":"synth"}`,
			`{"path":"MS10 From Mars/WAV/SuperPulse/SuperPulse C1.wav","category":"multisamples","instrument":"lead","family":"synth"}`,
			`{"path":"MS10 From Mars/WAV/SuperPulse/SuperPulse C2.wav","category":"multisamples","instrument":"lead","family":"synth"}`,
			`{"path":"MS10 From Mars/WAV/Kit/Kick.wav","category":"one-shots","instrument":"kick","family":"drums"}`,
			`{"path":"MS10 From Mars/WAV/Kit/Snare.wav","category":"one-shots","instrument":"snare","family":"drums"}`,
			`{"path":"MS10 From Mars/WAV/Kit/Hat.wav","category":"one-shots","instrument":"hat","family":"drums"}`,
		}, "\n") + "\n",
	})
	ws.Config.Locations[0].Vendor = "sfm"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/live.toml", syntaktDevice+"[companions]\ntypes = [\"adg\"]\n")
	writeProfile(t, ws, "storage/sq.toml", "name = \"sq\"\nkind = \"quota\"\ncapacity_bytes = 33554432\n")
	writeView(t, ws, "v", `name="v"
device="live"
storage="sq"
layout="{family}/{instrument}/{category}/{pack}/{file}"
[[include]]
location="src"
glob="**"
`)
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range p.Entries {
		got[e.SourcePath] = e.OutPath
	}
	want := map[string]string{
		"MS10 From Mars/WAV/SuperPulse/SuperPulse C0.wav":                         "Synth/Lead/Multisamples/MS10 From Mars/SuperPulse C0.wav",
		"MS10 From Mars/Ableton Live/MS10 From Mars/Presets/Leads/SuperPulse.adg": "Synth/Lead/Multisamples/MS10 From Mars/SuperPulse.adg",
		"MS10 From Mars/WAV/Kit/Kick.wav":                                         "Drums/Kick/One-Shots/MS10 From Mars/Kick.wav",
		"MS10 From Mars/Ableton Live/MS10 From Mars/Presets/Kits/Clean Kit.adg":   "Drums/_General/One-Shots/MS10 From Mars/Clean Kit.adg",
		"MS10 From Mars/Ableton Live/MS10 From Mars/Presets/FX/Blind.adg":         "_Unsorted/Samples From Mars/MS10 From Mars/MS10 From Mars/Presets/FX/Blind.adg",
	}
	for src, out := range want {
		if got[src] != out {
			t.Errorf("%s → %q, want %q", src, got[src], out)
		}
	}
	if p.Companions != 3 {
		t.Errorf("companions = %d, want 3", p.Companions)
	}
	// Blind.adg sits under _Unsorted, but a document is never a decision
	// of its own: the counters the UI turns into "N need a decision"
	// describe samples only.
	if p.Unsorted != 0 || p.Uncategorized != 0 || p.General != 0 {
		t.Errorf("companions must not count as decisions: unsorted=%d uncategorized=%d general=%d", p.Unsorted, p.Uncategorized, p.General)
	}
	if len(p.Errors) != 0 {
		t.Errorf("errors: %v", p.Errors)
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "1 Ableton document points at no sample") || !strings.Contains(joined, "Blind.adg") {
		t.Errorf("warnings: %v", p.Warnings)
	}
}

func TestInheritVote(t *testing.T) {
	m := inherit(nil)
	if m.Family != "" || m.Instrument != "" {
		t.Errorf("empty vote = %+v", m)
	}
	// 15 kicks and one snare: near-unanimous, the kick stands
	var metas []harvest.Meta
	for range 15 {
		metas = append(metas, harvest.Meta{Family: "drums", Instrument: "kick", Category: "one-shots"})
	}
	metas = append(metas, harvest.Meta{Family: "drums", Instrument: "snare", Category: "loops"})
	m = inherit(metas)
	if m.Family != "drums" || m.Instrument != "kick" || m.Category != "one-shots" {
		t.Errorf("15:1 vote = %+v", m)
	}
	// an even split falls to the family catch-all and an unsorted category
	m = inherit([]harvest.Meta{
		{Family: "drums", Instrument: "kick", Category: "one-shots"},
		{Family: "drums", Instrument: "snare", Category: "loops"},
		{Family: "bass", Instrument: "sub", Category: "one-shots"},
	})
	if m.Family != "drums" || m.Instrument != "drums" || m.Category != "" {
		t.Errorf("split vote = %+v", m)
	}
}
