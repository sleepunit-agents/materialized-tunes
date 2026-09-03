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
	// Codes are the drum-machine abbreviations — "bd", "sd", "cp", "hh" —
	// that vendors write when they write nothing longer. Two letters are
	// also what a Splice pack code or a genre tag looks like
	// ("FF_CP_124_drum_loop_venice_shaker" is a shaker loop from the pack
	// Club Progressive; "AU_PC_94_drum_loop_full_cp" is cyberpunk), so a
	// code only speaks for a segment when no alias of any instrument does.
	// Where it speaks it ranks as its instrument, like an alias would: the
	// "909 CP 01" in a Drum Hits folder is still a clap.
	Codes []string `toml:"codes" json:"codes,omitempty"`
	// Category names the kind of recording this word describes when it
	// describes one at all: a break is a loop by definition. A file whose
	// category is already known to be something else can't be one, so
	// there the word is a title ("Beat" is a kit's name, "Break Chop" is
	// a hit cut from a break) — it speaks only for its family, through
	// the family's catch-all entry, and every lower entry gets its turn.
	// On the catch-all's OWN word the demotion has nowhere to go, so the
	// word is silent instead: "kit" is the drums catch-all's word for a set
	// of hits, and a kit that is already loops is a construction kit — a
	// song's stems — which names no family at all; each stem reads its own
	// name. Empty means the word describes a sound whatever its length,
	// which is nearly every entry. Shared-lexicon-only, like split and
	// display; where an id has several entries, overrides inherit the
	// first's.
	Category string `toml:"category" json:"category,omitempty"`
	// Split exempts one entry from its family's flat rendering: the family
	// stays flat for everything else, this instrument keeps its own folder.
	// For a real named instrument sitting in a family that is flat because
	// the REST of it is jargon — an upright bass among the 808s and reeses.
	Split bool `toml:"split" json:"split,omitempty"`
	// Display overrides the folder name derived from the id. Ids are slugs
	// and the derivation keeps their hyphens (it also renders categories,
	// where "one-shots" → "One-Shots" is right); a multi-word instrument
	// name wants the space back: "upright-bass" → "Upright Bass".
	Display string `toml:"display" json:"display,omitempty"`
	// Scope says whose block this is when it is an override: "pack" or
	// "vendor". Set by the loader, never by the file; it only names the
	// tier in a Source.
	Scope string `toml:"-" json:"-"`

	Provenance
}

// tier names the Source tier an override block answers as.
func (ins Instrument) tier(code bool) string {
	switch ins.Scope {
	case "pack":
		if code {
			return TierPackCode
		}
		return TierPackInstrument
	case "vendor":
		if code {
			return TierVendorCode
		}
		return TierVendorInstrument
	}
	if code {
		return TierOverrideCode
	}
	return TierOverride
}

// Family is one [[family]] block of the shared lexicon: knowledge about a
// whole family rather than one instrument. flat = true marks a family whose
// layout tree does not split by instrument — bass is bass; the sub/reese/wub
// taxonomy in vendor naming isn't reliable enough to fight samples over.
// The instrument entries still resolve and still land in harvest metadata;
// flat only stops the folder split, and a single instrument can opt back
// out of it with `split` (see Instrument.Split). Split is a property of the
// shared lexicon: a vendor-local entry introducing a brand-new id can't set
// it, only pin one that already exists.
type Family struct {
	ID   string `toml:"id" json:"id"`
	Flat bool   `toml:"flat" json:"flat,omitempty"`
}

// Lexicon is the ordered instrument vocabulary plus its compiled matchers.
type Lexicon struct {
	Instruments []Instrument
	Families    []Family
	patterns    []*regexp.Regexp  // per instrument: whole-word alternation of aliases
	codes       []*regexp.Regexp  // per instrument: whole-word alternation of codes, nil when none
	avoids      []*regexp.Regexp  // per instrument: nil when none
	flat        map[string]bool   // family id → flat
	split       map[string]bool   // instrument id → keeps its folder in a flat family
	display     map[string]string // instrument id → folder name override
	category    map[string]string // instrument id → the category its word implies, "" for most
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
		Family     []Family     `toml:"family"`
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return lx
	}
	lx.Instruments, lx.Families = f.Instrument, f.Family
	lx.compile()
	return lx
}

// FlatFamily reports whether the lexicon marks a family flat — rendered
// without an instrument level by layout templates. Safe on a nil lexicon.
func (lx *Lexicon) FlatFamily(id string) bool {
	return lx != nil && id != "" && lx.flat[id]
}

// SplitsFlat reports whether this instrument keeps its own folder level even
// though its family is flat. Safe on a nil lexicon.
func (lx *Lexicon) SplitsFlat(id string) bool {
	return lx != nil && id != "" && lx.split[id]
}

// DisplayName is the folder name the lexicon wants for an instrument id, or
// "" to let the caller derive one from the id. Safe on a nil lexicon.
func (lx *Lexicon) DisplayName(id string) string {
	if lx == nil || id == "" {
		return ""
	}
	return lx.display[id]
}

func (lx *Lexicon) compile() {
	lx.patterns = make([]*regexp.Regexp, len(lx.Instruments))
	lx.codes = make([]*regexp.Regexp, len(lx.Instruments))
	lx.avoids = make([]*regexp.Regexp, len(lx.Instruments))
	for i, ins := range lx.Instruments {
		lx.patterns[i] = wordAlternation(ins.Aliases)
		lx.codes[i] = wordAlternation(ins.Codes)
		lx.avoids[i] = wordAlternation(ins.Avoid)
	}
	lx.flat = map[string]bool{}
	for _, f := range lx.Families {
		if f.Flat {
			lx.flat[f.ID] = true
		}
	}
	lx.split, lx.display, lx.category = map[string]bool{}, map[string]string{}, map[string]string{}
	for _, ins := range lx.Instruments {
		if ins.Split {
			lx.split[ins.ID] = true
		}
		if ins.Display != "" {
			lx.display[ins.ID] = ins.Display
		}
		if ins.Category != "" && lx.category[ins.ID] == "" {
			lx.category[ins.ID] = ins.Category
		}
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
	letterDigitRe = regexp.MustCompile(`([a-z])([0-9])`)
	digitLetterRe = regexp.MustCompile(`([0-9])([a-z])`)
	camelRe       = regexp.MustCompile(`([a-z])([A-Z])`)
	acronymRe     = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

// Normalize turns a path segment or filename stem into the form the lexicon
// matches against: lowercase, vendor order prefixes ("01. ") dropped,
// everything non-alphanumeric collapsed to single spaces, and a boundary
// opened wherever a letter meets a digit. Vendors glue take numbers onto
// the word ("808_Kick02", "Snare01", "BD3"), and a whole-word match that
// sees "kick02" sees no kick at all — the folder word then decides, and a
// kit called "Beat" filed its kicks as breaks. Aliases pass through the
// same function, so "80s", "8bit" and "TR808" still meet themselves.
//
// Camel case is opened the same way, before lowercasing loses it: vendors
// glue words as readily as numbers — SFM's VP330 names every patch folder
// "StringsLowGlide" / "EnsembleMaleVibe1", Loopmasters writes
// "FAI_CrispShaker_13", Polyend "CowBell" — and 37,844 stems in the house
// listing carry a lower-to-upper boundary. A run of capitals stays one
// word up to the last, which starts the next ("FMBell" is FM Bell, "SDSV"
// is SDSV); an all-caps or all-lowercase segment is untouched, so "BD",
// "SS" and "hihat" read as before (2026-09-02).
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	s = camelRe.ReplaceAllString(s, "$1 $2")
	s = acronymRe.ReplaceAllString(s, "$1 $2")
	s = strings.ToLower(s)
	s = orderPrefixRe.ReplaceAllString(s, "")
	s = nonWordRe.ReplaceAllString(s, " ")
	s = letterDigitRe.ReplaceAllString(s, "$1 $2")
	s = digitLetterRe.ReplaceAllString(s, "$1 $2")
	return strings.TrimSpace(s)
}

// Match returns the instrument a single normalized segment names, or "".
// vendorFirst, when non-nil, is consulted before the shared lexicon so a
// vendor can fix its own oddities ("CH" = closed hat on SFM).
func (lx *Lexicon) Match(segment string, vendorFirst []Instrument) (id, family string) {
	id, family, _, _ = lx.match(segment, vendorFirst, "")
	return id, family
}

// match also reports the winner's rank — its position in the lexicon, which
// is how Resolve compares labels found in different parts of a path.
// Vendor overrides rank alongside the shared entry they name.
//
// Words are read before codes: every alias, vendor and shared, gets its
// chance at the segment, and only a segment no word claims falls through
// to the abbreviations. Within a segment that is the whole rule — a code
// beside a longer label is a pack code or a genre tag, not the vendor
// naming the sound twice. Across segments a code that did speak ranks as
// its instrument, so Resolve treats "909 CP 01" under Drum Hits exactly
// as it would "909 Clap 01".
//
// category is what the file is already known to be ("" when nothing said).
// An entry whose word implies a different category (break implies loops)
// is a title on such a file, not a label: it is passed over so lower
// entries get their turn, and if none of them speak it stands in for its
// family alone — the catch-all entry, ranked where catch-alls rank, so a
// real label anywhere else on the path still wins.
func (lx *Lexicon) match(segment string, vendorFirst []Instrument, category string) (id, family string, rank int, src Source) {
	norm := Normalize(segment)
	if norm == "" {
		return "", "", 0, Source{}
	}
	pad := " " + norm + " "
	demoted := ""        // family of the first word the category ruled out
	var demotedBy Source // and the word it was
	note := func(fam, word string) {
		if demoted == "" {
			demoted = fam
			demotedBy = Source{Tier: TierDemoted, Word: word}
		}
	}
	for _, ins := range vendorFirst {
		if w, ok := hit(pad, wordAlternation(ins.Aliases), wordAlternation(ins.Avoid)); ok {
			if lx.gated(ins.ID, category) {
				note(lx.familyOf(ins.ID, ins.Family), w)
				continue
			}
			return ins.ID, lx.familyOf(ins.ID, ins.Family), lx.rankOf(ins.ID), Source{Tier: ins.tier(false), Word: w}
		}
	}
	if conjunctionRe.MatchString(segment) {
		if id, family, rank, ok := lx.compound(pad); ok {
			return id, family, rank, Source{Tier: TierCompound, Word: norm}
		}
	}
	for i, ins := range lx.Instruments {
		if w, ok := hit(pad, lx.patterns[i], lx.avoids[i]); ok {
			if gatedEntry(ins, category) {
				if ins.ID != ins.Family { // a catch-all's own word has no family to fall back on
					note(ins.Family, w)
				}
				continue
			}
			return ins.ID, ins.Family, i, Source{Tier: TierLexicon, Word: w}
		}
	}
	for _, ins := range vendorFirst {
		if w, ok := hit(pad, wordAlternation(ins.Codes), wordAlternation(ins.Avoid)); ok {
			if lx.gated(ins.ID, category) {
				note(lx.familyOf(ins.ID, ins.Family), w)
				continue
			}
			return ins.ID, lx.familyOf(ins.ID, ins.Family), lx.rankOf(ins.ID), Source{Tier: ins.tier(true), Word: w}
		}
	}
	for i, ins := range lx.Instruments {
		if w, ok := hit(pad, lx.codes[i], lx.avoids[i]); ok {
			if gatedEntry(ins, category) {
				if ins.ID != ins.Family { // a catch-all's own word has no family to fall back on
					note(ins.Family, w)
				}
				continue
			}
			return ins.ID, ins.Family, i, Source{Tier: TierLexiconCode, Word: w}
		}
	}
	if demoted != "" {
		if r := lx.rankOf(demoted); r < len(lx.Instruments) {
			return demoted, demoted, r, demotedBy
		}
	}
	return "", "", 0, Source{}
}

// gatedEntry reports whether a shared entry's word is ruled out on a file
// of the given category: the entry names a category of its own and the
// file is known to be a different one. Each entry carries its own — an id
// may have several entries, and only one of them may be scoped ("kit" is a
// one-shots word for drums; "drum" is not).
func gatedEntry(ins Instrument, category string) bool {
	return category != "" && ins.Category != "" && ins.Category != category
}

// gated is gatedEntry for a vendor or pack override, which carries no
// category of its own and inherits the first shared entry's by id.
func (lx *Lexicon) gated(id, category string) bool {
	if category == "" {
		return false
	}
	want := lx.category[id]
	return want != "" && want != category
}

// conjunctionRe spots a segment that may be naming more than one thing —
// "Cymbals and Hats", "Kicks & Snares", "Texture + FX", "Clap, Snare". It is
// checked against the RAW segment because Normalize collapses "&" and "+" to
// spaces. A false positive costs one extra scan and nothing else: compound
// declines unless two different instruments actually turn up.
var conjunctionRe = regexp.MustCompile(`(?i)(^|[^a-z0-9])and([^a-z0-9]|$)|[&+,]`)

// compound reads a segment that names more than one thing. A folder holding
// two kinds of sound is not a label for either of them: with plain
// first-hit-wins, "Clap and Snare" files every clap under Snare and
// "Cymbals and Hats" files every crash under Hat — no error, nothing
// unsorted, just wrong. So no instrument may claim a file from such a
// segment.
//
// What survives is only what the winners agree on: their shared family,
// reported through that family's own catch-all entry ("drums", "synth", …).
// Those entries sit at the END of their section by design, so any specific
// label elsewhere on the path — the filename Polyend and Splice reliably
// write — still outranks the folder and decides. When the families disagree
// too the segment says nothing at all, and a file whose name says nothing
// either lands in _Unsorted: visible, rather than silently mis-filed.
//
// ok is false when the segment names at most one thing after all, and the
// caller falls through to the ordinary path. Two ways that happens: an alias
// written for the whole phrase is a deliberate pin ("Claves and Guiro" is one
// alias of clave, not a compound), and a hit swallowed by a longer one is the
// same label seen twice ("bass drum" is a kick, not a kick plus a bass).
func (lx *Lexicon) compound(pad string) (id, family string, rank int, ok bool) {
	type span struct{ i, lo, hi int }
	var kept []span
	for i := range lx.Instruments {
		p := lx.patterns[i]
		if _, ok := hit(pad, p, lx.avoids[i]); !ok {
			continue
		}
		at := p.FindStringIndex(pad)
		covered := false
		for _, k := range kept {
			if at[0] >= k.lo && at[1] <= k.hi {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, span{i, at[0], at[1]})
		}
	}
	ids, fam := map[string]bool{}, ""
	for _, k := range kept {
		ins := lx.Instruments[k.i]
		ids[ins.ID] = true
		switch {
		case fam == "":
			fam = ins.Family
		case fam != ins.Family:
			fam = "\x00" // families disagree; the segment can only stay silent
		}
	}
	if len(ids) < 2 {
		return "", "", 0, false
	}
	// A family speaks through its own catch-all instrument (id == family id).
	// A family without one has nothing to say at this depth.
	if r := lx.rankOf(fam); r < len(lx.Instruments) {
		return fam, fam, r, true
	}
	return "", "", 0, true
}

func (lx *Lexicon) rankOf(id string) int {
	for i, ins := range lx.Instruments {
		if ins.ID == id {
			return i
		}
	}
	return len(lx.Instruments) // unknown id: least specific
}

// hit reports whether a pattern claims the padded segment and which of
// its phrases did — the alternation's second group, the word a Source
// records. An avoid phrase anywhere in the segment vetoes the match.
func hit(padded string, pat, avoid *regexp.Regexp) (word string, ok bool) {
	if pat == nil {
		return "", false
	}
	m := pat.FindStringSubmatch(padded)
	if m == nil {
		return "", false
	}
	if avoid != nil && avoid.MatchString(padded) {
		return "", false
	}
	return m[2], true
}

// FamilyOf reports the family of a known instrument id — for callers that
// get an id from an annotation pin rather than a text match. Vendor
// overrides are consulted first, so a pinned vendor-local id resolves too.
func (lx *Lexicon) FamilyOf(id string, vendorFirst []Instrument) string {
	for _, ins := range vendorFirst {
		if ins.ID == id {
			return lx.familyOf(id, ins.Family)
		}
	}
	return lx.familyOf(id, "")
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
	return lx.ResolveIn("", stem, dirs, vendorFirst)
}

// ResolveIn is Resolve for a file whose category is already known: a word
// that implies a different category (see Instrument.Category) is a title
// there, not a label, and speaks only for its family. "" means unknown and
// every word stands.
func (lx *Lexicon) ResolveIn(category, stem string, dirs []string, vendorFirst []Instrument) (id, family string) {
	id, family, _ = lx.ResolveInSrc(category, stem, dirs, vendorFirst)
	return id, family
}

// ResolveInSrc is ResolveIn that also says why: the tier, segment and
// word the winning label came from. src is zero when nothing spoke.
func (lx *Lexicon) ResolveInSrc(category, stem string, dirs []string, vendorFirst []Instrument) (id, family string, src Source) {
	best := -1
	for _, seg := range append([]string{stem}, dirs...) {
		gotID, gotFamily, rank, gotSrc := lx.match(seg, vendorFirst, category)
		if gotID == "" {
			continue
		}
		if best < 0 || rank < best {
			best, id, family, src = rank, gotID, gotFamily, gotSrc
			src.Segment = seg
		}
	}
	return id, family, src
}
