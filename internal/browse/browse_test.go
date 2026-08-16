package browse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRTFToText(t *testing.T) {
	// The shape TextEdit writes: header groups to drop, \'hh escapes, \par breaks.
	rtf := `{\rtf1\ansi\ansicpg1252\cocoartf2709
{\fonttbl\f0\fswiss\fcharset0 Helvetica;}
{\colortbl;\red255\green255\blue255;}
{\*\expandedcolortbl;;}
\paperw11900\paperh16840\margl1440\margr1440\vieww11520\viewh8400\viewkind0
\pard\tx720\pardirnatural\partightenfactor0

\f0\fs24 \cf0 360 From Mars - About\
\
The 360\'92s twelve-bit crunch \'96 sampled through a Neve.\par
Second paragraph {\b bold} text \\ backslash.}`
	got := rtfToText(rtf)
	if !strings.HasPrefix(got, "360 From Mars - About") {
		t.Errorf("title line lost: %q", got)
	}
	for _, want := range []string{"twelve-bit crunch", "’", "–", "Second paragraph bold text \\ backslash"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, bad := range []string{"fonttbl", "Helvetica", "colortbl", `\cf0`, "{", "}"} {
		if strings.Contains(got, bad) {
			t.Errorf("leaked %q in %q", bad, got)
		}
	}
}

func TestReadAboutMarkdown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "X - About.md")
	os.WriteFile(p, []byte(`# Frost — Pluck Pack

**Vendor:** Polyend (Palette series)
**Product page:** https://polyend.com/product/frost/

100+ synthesized plucks.

---
*Docs assembled by mtunes.*
`), 0o644)
	title, desc, url := ReadAbout(p)
	if title != "Frost — Pluck Pack" {
		t.Errorf("title = %q", title)
	}
	if url != "https://polyend.com/product/frost/" {
		t.Errorf("url = %q", url)
	}
	if !strings.Contains(desc, "Vendor: Polyend") || !strings.Contains(desc, "100+ synthesized plucks.") || strings.Contains(desc, "assembled by") || strings.Contains(desc, "**") {
		t.Errorf("desc = %q", desc)
	}
}

func TestSplitCatalogRef(t *testing.T) {
	loc, path, ok := SplitCatalogRef("catalog:archives/Zero-G/Jungle Warfare Vol 1/Docs/Artwork - Jungle Warfare Vol 1.jpg")
	if !ok || loc != "archives" || path != "Zero-G/Jungle Warfare Vol 1/Docs/Artwork - Jungle Warfare Vol 1.jpg" {
		t.Errorf("got %q %q %v", loc, path, ok)
	}
	if _, _, ok := SplitCatalogRef("https://example.com/x.jpg"); ok {
		t.Error("URL must not parse as a catalog ref")
	}
	if _, _, ok := SplitCatalogRef("catalog:noslash"); ok {
		t.Error("ref without a path must not parse")
	}
}
