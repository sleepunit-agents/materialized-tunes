package view

import (
	"strings"
	"testing"
)

func TestParseLayout(t *testing.T) {
	good := []string{
		"{family}/{instrument}/{category}/{pack}/{file}",
		"{instrument}/{vendor}/{pack}/{file}",
		"{vendor}/{pack}/{path}",
		"Samples/{family}/{path}",
		"{family}-{instrument}/{file}",
	}
	for _, g := range good {
		if _, err := ParseLayout(g); err != nil {
			t.Errorf("%q: unexpected error %v", g, err)
		}
	}
	bad := map[string]string{
		"{family}/{instrument}":     "must end with",
		"{path}/{family}":           "whole last segment",
		"{family}/x{file}":          "whole last segment",
		"{family}/{file}/{path}":    "whole last segment",
		"{genre}/{file}":            "unknown token",
		"/{family}/{file}":          "must not start",
		"{family}//{file}":          "empty or dot",
		"{family}/../{file}":        "empty or dot",
		"{family/{file}":            "unbalanced",
		"{family}\\{file}":          "use /",
		"{family}/{instrument}/{f}": "unknown token",
	}
	for tpl, want := range bad {
		_, err := ParseLayout(tpl)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", tpl, err, want)
		}
	}
	if l, err := ParseLayout("  "); l != nil || err != nil {
		t.Errorf("blank: %v %v", l, err)
	}
}

func TestLayoutRender(t *testing.T) {
	l, err := ParseLayout("{family}/{instrument}/{category}/{pack}/{file}")
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]string{"family": "Drums", "instrument": "Kick", "category": "One-Shots", "pack": "Grit", "file": "k.wav"}
	if got := l.Render(full); got != "Drums/Kick/One-Shots/Grit/k.wav" {
		t.Errorf("full: %q", got)
	}
	noCat := map[string]string{"family": "Drums", "instrument": "Kick", "pack": "Grit", "file": "k.wav"}
	if got := l.Render(noCat); got != "Drums/Kick/Grit/k.wav" {
		t.Errorf("empty category segment should drop: %q", got)
	}
	if !l.NeedsMeta() || !l.NeedsInstrument() || !l.Uses(TokFile) || l.Uses(TokPath) {
		t.Errorf("token queries wrong")
	}
	mixed, _ := ParseLayout("by-{instrument}/{path}")
	if got := mixed.Render(map[string]string{"instrument": "Rim", "path": "a/b.wav"}); got != "by-Rim/a/b.wav" {
		t.Errorf("mixed literal: %q", got)
	}
	var nilL *Layout
	if nilL.Uses(TokFile) || nilL.NeedsMeta() {
		t.Errorf("nil layout should use nothing")
	}
}
