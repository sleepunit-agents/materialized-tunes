package annotations

// Source records which tier answered for one facet of one file and what
// it fired on — the "why" behind a harvested category or instrument
// (SPEC §19.2). Harvest stores one per facet in the meta cache; the plan's
// review surface shows it; a correction targets the level that lied.
type Source struct {
	// Tier is which layer spoke — one of the Tier* constants below, listed
	// in the order harvest consults them.
	Tier string `json:"tier"`
	// Segment is the path segment the tier read: a directory name, the
	// filename stem, or for a [[dir]] pin the in-pack directory path the
	// entry governed.
	Segment string `json:"segment,omitempty"`
	// Word is what matched inside the segment: the alias or code, the
	// vendor's glob, or the [[dir]] entry's own path.
	Word string `json:"word,omitempty"`
	// Echo marks a segment that only restates the pack's name, consulted
	// after every real label stayed silent (see harvest.labelDirs).
	Echo bool `json:"echo,omitempty"`
	// Doc is set on the document tier: the Live document whose folder
	// spoke for this file (catalog path). Segment and Word are the
	// document's folder and the alias that matched inside it.
	Doc string `json:"doc,omitempty"`
}

// Tiers, in the order harvest consults them for each facet.
const (
	// Category tiers.
	TierDir              = "dir"               // a pack [[dir]] entry (Word = its path); also the instrument pin tier
	TierDedicatedPack    = "dedicated-pack"    // vendor [[category]] dedicated_packs glob over the pack dir
	TierVendorCategory   = "vendor-category"   // vendor [[category]] match glob over a directory name
	TierCategories       = "categories"        // shared categories.toml alias
	TierDocument         = "document"          // nothing on the file's own path spoke; a Live document that references it sits in a labelled folder (Doc = the document)
	TierDocumentConflict = "document-conflict" // documents referencing the file sit in folders that disagree — none spoke
	TierMultisample      = "multisample"       // no word anywhere; the directory has the chromatic multisample shape
	TierDirDefault       = "dir-default"       // a pack [[dir]] default_category / default_instrument: nothing else spoke

	// Instrument tiers. Alias tiers come before code tiers: a code speaks
	// only for a segment no alias of any instrument claimed.
	TierPackInstrument   = "pack-instrument"   // pack [[instrument]] alias
	TierVendorInstrument = "vendor-instrument" // vendor [[instrument]] alias
	TierOverride         = "override"          // an override block with no declared scope
	TierCompound         = "compound"          // the segment named two instruments of one family; the family catch-all answers
	TierLexicon          = "lexicon"           // shared instruments.toml alias
	TierPackCode         = "pack-code"         // pack [[instrument]] code
	TierVendorCode       = "vendor-code"       // vendor [[instrument]] code
	TierOverrideCode     = "override-code"
	TierLexiconCode      = "lexicon-code" // shared instruments.toml code
	TierDemoted          = "demoted"      // the word's category disagreed with the file's; its family catch-all stands in
)

// Describe renders a source for humans: "vendor [[category]] "*Hits*" on "01. Individual Hits"".
func (s Source) Describe() string {
	if s.Tier == "" {
		return "nothing spoke"
	}
	label := map[string]string{
		TierDir:              "pack [[dir]]",
		TierDedicatedPack:    "vendor dedicated_packs",
		TierVendorCategory:   "vendor [[category]]",
		TierCategories:       "categories.toml",
		TierDocument:         "folder of a Live document referencing the file",
		TierDocumentConflict: "Live documents referencing the file disagree",
		TierMultisample:      "multisample shape of the directory",
		TierDirDefault:       "pack [[dir]] default",
		TierPackInstrument:   "pack [[instrument]]",
		TierVendorInstrument: "vendor [[instrument]]",
		TierOverride:         "override",
		TierCompound:         "compound segment → family catch-all",
		TierLexicon:          "instruments.toml",
		TierPackCode:         "pack [[instrument]] code",
		TierVendorCode:       "vendor [[instrument]] code",
		TierOverrideCode:     "override code",
		TierLexiconCode:      "instruments.toml code",
		TierDemoted:          "word demoted (its category disagrees) → family catch-all",
	}[s.Tier]
	if label == "" {
		label = s.Tier
	}
	out := label
	if s.Word != "" {
		out += " " + quote(s.Word)
	}
	if s.Segment != "" {
		out += " on " + quote(s.Segment)
		if s.Echo {
			out += " (pack-name echo)"
		}
	}
	if s.Doc != "" {
		out += " in " + quote(s.Doc)
	}
	return out
}

func quote(s string) string { return "\"" + s + "\"" }
