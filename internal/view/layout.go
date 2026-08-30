package view

import (
	"fmt"
	"regexp"
	"strings"
)

// Layout is a parsed output-layout template: how every selected file's
// output path is built when the recipe says `layout = "..."`. Segments
// are separated by "/", each one literal text and/or {tokens}:
//
//	{vendor}      the vendor's display name (annotations) or the location
//	{pack}        the pack directory name
//	{family}      harvested instrument family — Drums, Bass, Keys, …
//	{instrument}  harvested instrument — Kick, Rim, Sub, Pad, …
//	{category}    harvested category — One-Shots, Loops, Kits, FX, …
//	{path}        the file's path within its pack (after format-tree strip)
//	{file}        the file name alone (intra-pack folders dropped)
//
// The last segment must be {path} or {file} and neither may appear
// anywhere else. A segment whose tokens all come up empty is dropped, so
// "{family}/{instrument}/{category}/{pack}/{file}" still places a kick
// with no loop/one-shot signal under Drums/Kick/<pack>/. A file with no
// instrument at all cannot be placed by a template that asks for one; the
// planner sends it to Unsorted (see plan) instead of guessing.
type Layout struct {
	Template string
	Segments []Segment
}

// Segment is one path level: an ordered mix of literal runs and tokens.
type Segment struct {
	Parts []Part
}

// Part is either literal text (Token == "") or a token name.
type Part struct {
	Literal string
	Token   string
}

// Layout tokens.
const (
	TokVendor     = "vendor"
	TokPack       = "pack"
	TokFamily     = "family"
	TokInstrument = "instrument"
	TokCategory   = "category"
	TokPath       = "path"
	TokFile       = "file"
)

var layoutTokens = map[string]bool{
	TokVendor: true, TokPack: true, TokFamily: true, TokInstrument: true,
	TokCategory: true, TokPath: true, TokFile: true,
}

var tokenRe = regexp.MustCompile(`\{([^{}/]*)\}`)

// ParseLayout validates and parses a template. "" is the mirror layout —
// no template — and returns (nil, nil).
func ParseLayout(tpl string) (*Layout, error) {
	tpl = strings.TrimSpace(tpl)
	if tpl == "" {
		return nil, nil
	}
	if strings.HasPrefix(tpl, "/") || strings.HasSuffix(tpl, "/") {
		return nil, fmt.Errorf("layout %q: must not start or end with /", tpl)
	}
	if strings.Contains(tpl, "\\") {
		return nil, fmt.Errorf("layout %q: use / between folders", tpl)
	}
	l := &Layout{Template: tpl}
	segs := strings.Split(tpl, "/")
	leafSeen := ""
	for i, s := range segs {
		if s == "" || s == "." || s == ".." {
			return nil, fmt.Errorf("layout %q: empty or dot segment", tpl)
		}
		var seg Segment
		pos := 0
		for _, m := range tokenRe.FindAllStringSubmatchIndex(s, -1) {
			if m[0] > pos {
				seg.Parts = append(seg.Parts, Part{Literal: s[pos:m[0]]})
			}
			tok := s[m[2]:m[3]]
			if !layoutTokens[tok] {
				return nil, fmt.Errorf("layout %q: unknown token {%s} (known: {vendor} {pack} {family} {instrument} {category} {path} {file})", tpl, tok)
			}
			if tok == TokPath || tok == TokFile {
				if i != len(segs)-1 || leafSeen != "" || len(seg.Parts) > 0 {
					return nil, fmt.Errorf("layout %q: {%s} must be the whole last segment", tpl, tok)
				}
				leafSeen = tok
			}
			seg.Parts = append(seg.Parts, Part{Token: tok})
			pos = m[1]
		}
		if pos < len(s) {
			if leafSeen != "" && i == len(segs)-1 {
				return nil, fmt.Errorf("layout %q: {%s} must be the whole last segment", tpl, leafSeen)
			}
			seg.Parts = append(seg.Parts, Part{Literal: s[pos:]})
		}
		if strings.ContainsAny(s, "{}") && len(tokenRe.FindAllString(s, -1)) != strings.Count(s, "{") {
			return nil, fmt.Errorf("layout %q: unbalanced braces in %q", tpl, s)
		}
		l.Segments = append(l.Segments, seg)
	}
	if leafSeen == "" {
		return nil, fmt.Errorf("layout %q: must end with {path} or {file}", tpl)
	}
	return l, nil
}

// Uses reports whether any segment names the token.
func (l *Layout) Uses(tok string) bool {
	if l == nil {
		return false
	}
	for _, seg := range l.Segments {
		for _, p := range seg.Parts {
			if p.Token == tok {
				return true
			}
		}
	}
	return false
}

// NeedsMeta reports whether the template reads harvested metadata.
func (l *Layout) NeedsMeta() bool {
	return l.Uses(TokFamily) || l.Uses(TokInstrument) || l.Uses(TokCategory)
}

// NeedsInstrument reports whether a file without an instrument label has
// nowhere to go under this template.
func (l *Layout) NeedsInstrument() bool {
	return l.Uses(TokFamily) || l.Uses(TokInstrument)
}

// Render builds the output path from token values. A segment with at least
// one token, all of whose tokens are empty, is omitted; the leaf segment
// is always kept.
func (l *Layout) Render(vals map[string]string) string {
	out := make([]string, 0, len(l.Segments))
	for i, seg := range l.Segments {
		var sb strings.Builder
		tokens, filled := 0, 0
		for _, p := range seg.Parts {
			if p.Token == "" {
				sb.WriteString(p.Literal)
				continue
			}
			tokens++
			if v := vals[p.Token]; v != "" {
				filled++
				sb.WriteString(v)
			}
		}
		if tokens > 0 && filled == 0 && i != len(l.Segments)-1 {
			continue
		}
		if s := sb.String(); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "/")
}

// LayoutPreset is a named template the UI offers; the recipe stores only
// the template string, so hand-written ones are equal citizens.
type LayoutPreset struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Template string `json:"template"`
	Example  string `json:"example"`
}

var LayoutPresets = []LayoutPreset{
	{ID: "mirror", Label: "Vendor / Pack (mirror the source)", Template: "",
		Example: "SPLICE/Grit - Tech House/one_shots/kicks/GTH_Kick_03.wav"},
	{ID: "by-instrument", Label: "Family / Instrument / Loop-or-Shot / Pack", Template: "{family}/{instrument}/{category}/{pack}/{file}",
		Example: "Drums/Kick/One-Shots/Grit - Tech House/GTH_Kick_03.wav"},
	{ID: "instrument-vendor", Label: "Instrument / Vendor / Pack", Template: "{instrument}/{vendor}/{pack}/{file}",
		Example: "Kick/Splice/Grit - Tech House/GTH_Kick_03.wav"},
	{ID: "family-pack", Label: "Family / Pack (keep pack folders)", Template: "{family}/{pack}/{path}",
		Example: "Drums/Grit - Tech House/one_shots/kicks/GTH_Kick_03.wav"},
}
