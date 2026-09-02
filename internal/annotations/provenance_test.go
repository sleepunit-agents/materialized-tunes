package annotations

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// observed on [[dir]] / [[instrument]] decodes whether the hand wrote it
// quoted (the correction tool's form) or as a bare TOML date (the form
// [pack] and [acquisition] observed take). One bare date used to fail the
// whole vendor file.
func TestProvenanceObservedDecodesQuotedAndBareDates(t *testing.T) {
	var v struct {
		Instruments []Instrument `toml:"instrument"`
		Dirs        []Dir        `toml:"dir"`
	}
	src := `
[[instrument]]
id = "synth"
aliases = ["basics"]
observed = 2026-09-02
note = "bare"

[[instrument]]
id = "hat"
aliases = ["ch"]
observed = "2026-09-01"

[[dir]]
path = "WAV"
observed = 2026-08-30

[[dir]]
path = "Docs"
`
	if _, err := toml.Decode(src, &v); err != nil {
		t.Fatal(err)
	}
	if got := v.Instruments[0].Observed; got != "2026-09-02" {
		t.Errorf("bare date on [[instrument]]: got %q", got)
	}
	if got := v.Instruments[1].Observed; got != "2026-09-01" {
		t.Errorf("quoted date on [[instrument]]: got %q", got)
	}
	if got := v.Dirs[0].Observed; got != "2026-08-30" {
		t.Errorf("bare date on [[dir]]: got %q", got)
	}
	if got := v.Dirs[1].Observed; got != "" {
		t.Errorf("absent observed: got %q", got)
	}
	if _, err := toml.Decode("[[dir]]\npath = \"x\"\nobserved = 42\n", &v); err == nil {
		t.Errorf("an integer observed should be refused")
	}
}
