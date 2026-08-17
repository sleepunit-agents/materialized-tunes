package annotations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Instrument is one entry of the shared lexicon (instruments.toml): a
// canonical id, the family it belongs to, and the words vendors actually
// write for it. Order is significant — the file lists specific before
// generic so "bass drum" resolves to kick before "bass" can claim it.
type Instrument struct {
	ID      string   `toml:"id" json:"id"`
	Family  string   `toml:"family" json:"family,omitempty"`
	Aliases []string `toml:"aliases" json:"aliases,omitempty"`
	Avoid   []string `toml:"avoid" json:"avoid,omitempty"` // phrases that contain an alias but mean something else
}

// Lexicon is the ordered instrument vocabulary plus its compiled matchers.
type Lexicon struct {
	Instruments []Instrument
	patterns    []*regexp.Regexp // per instrument: whole-word alternation of aliases
	avoids      []*regexp.Regexp // per instrument: nil when none
}

// LoadInstruments reads <root>/instruments.toml. A missing file yields an
// empty lexicon — instrument tagging is optional, like every annotation.
func LoadInstruments(root string) *Lexicon {
	lx := &Lexicon{}
	data, err := os.ReadFile(filepath.Join(root, "instruments.toml"))
	if err != nil {
		return lx
	}
	var f struct {
		Instrument []Instrument `toml:"instrument"`
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return lx
	}
	lx.Instruments = f.Instrument
	lx.compile()
	return lx
}

func (lx *Lexicon) compile() {
	lx.patterns = make([]*regexp.Regexp, len(lx.Instruments))
	lx.avoids = make([]*regexp.Regexp, len(lx.Instruments))
	for i, ins := range lx.Instruments {
		lx.patterns[i] = wordAlternation(ins.Aliases)
		lx.avoids[i] = wordAlternation(ins.Avoid)
	}
}

// wordAlternation builds `\b(a|b|c)\b` over normalized phrases, longest
// first so "bass drum" wins over "bass" within one instrument too.
func wordAlternation(phrases []string) *regexp.Regexp {
	if len(phrases) == 0 {
		return nil
	}
	sorted := append([]string(nil), phrases...)
	for i := range sorted {
		sorted[i] = Normalize(sorted[i])
	}
	// longest first
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && len(sorted[j]) > len(sorted[j-1]); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	var quoted []string
	for _, p := range sorted {
		if p != "" {
			quoted = append(quoted, regexp.QuoteMeta(p))
		}
	}
	if len(quoted) == 0 {
		return nil
	}
	return regexp.MustCompile(`(^|[^a-z0-9])(` + strings.Join(quoted, "|") + `)($|[^a-z0-9])`)
}

var (
	orderPrefixRe = regexp.MustCompile(`^\d+\s*[.)-]\s*`)
	nonWordRe     = regexp.MustCompile(`[^a-z0-9]+`)
)

// Normalize turns a path segment or filename stem into the form the lexicon
// matches against: lowercase, vendor order prefixes ("01. ") dropped,
// everything non-alphanumeric collapsed to single spaces.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = orderPrefixRe.ReplaceAllString(s, "")
	s = nonWordRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Match returns the instrument a single normalized segment names, or "".
// vendorFirst, when non-nil, is consulted before the shared lexicon so a
// vendor can fix its own oddities ("CH" = closed hat on SFM).
func (lx *Lexicon) Match(segment string, vendorFirst []Instrument) (id, family string) {
	id, family, _ = lx.match(segment, vendorFirst)
	return id, family
}

// match also reports the winner's rank — its position in the lexicon, which
// is how Resolve compares labels found in different parts of a path.
// Vendor overrides rank alongside the shared entry they name.
func (lx *Lexicon) match(segment string, vendorFirst []Instrument) (id, family string, rank int) {
	norm := Normalize(segment)
	if norm == "" {
		return "", "", 0
	}
	pad := " " + norm + " "
	for _, ins := range vendorFirst {
		if hit(pad, wordAlternation(ins.Aliases), wordAlternation(ins.Avoid)) {
			return ins.ID, lx.familyOf(ins.ID, ins.Family), lx.rankOf(ins.ID)
		}
	}
	for i, ins := range lx.Instruments {
		if hit(pad, lx.patterns[i], lx.avoids[i]) {
			return ins.ID, ins.Family, i
		}
	}
	return "", "", 0
}

func (lx *Lexicon) rankOf(id string) int {
	for i, ins := range lx.Instruments {
		if ins.ID == id {
			return i
		}
	}
	return len(lx.Instruments) // unknown id: least specific
}

func hit(padded string, pat, avoid *regexp.Regexp) bool {
	if pat == nil || !pat.MatchString(padded) {
		return false
	}
	if avoid != nil && avoid.MatchString(padded) {
		return false
	}
	return true
}

// familyOf lets a vendor override name an id without repeating its family.
func (lx *Lexicon) familyOf(id, given string) string {
	if given != "" {
		return given
	}
	for _, ins := range lx.Instruments {
		if ins.ID == id {
			return ins.Family
		}
	}
	return ""
}

// Resolve reads the most specific label a vendor wrote for one file. Every
// label on the path counts — the filename stem and each directory — and the
// most specific one wins (earliest in the lexicon), not the nearest.
// Vendors put machine names in filenames, so "04. Rimshot/Rimshot TOM 31"
// has to read as a rimshot, while "Drums/Kick 01" still reads as a kick.
// dirs are the path segments within the pack; stem drops the extension.
func (lx *Lexicon) Resolve(stem string, dirs []string, vendorFirst []Instrument) (id, family string) {
	best := -1
	for _, seg := range append([]string{stem}, dirs...) {
		gotID, gotFamily, rank := lx.match(seg, vendorFirst)
		if gotID == "" {
			continue
		}
		if best < 0 || rank < best {
			best, id, family = rank, gotID, gotFamily
		}
	}
	return id, family
}
