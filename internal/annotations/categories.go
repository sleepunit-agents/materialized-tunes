package annotations

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// SharedCategory is one entry of the shared category lexicon
// (categories.toml): a canonical category id and the words vendors write
// for it, matched as whole words like the instrument lexicon.
type SharedCategory struct {
	ID      string   `toml:"id" json:"id"`
	Aliases []string `toml:"aliases" json:"aliases,omitempty"`
	Avoid   []string `toml:"avoid" json:"avoid,omitempty"`
}

// CategoryLexicon is the shared cross-vendor category vocabulary — the
// fallback tier behind a vendor's own [[category]] rules, and the only
// tier for vendors with no annotation at all.
type CategoryLexicon struct {
	Categories []SharedCategory
	patterns   []*regexp.Regexp
	avoids     []*regexp.Regexp
}

// LoadCategories reads <root>/categories.toml. A missing file yields an
// empty lexicon — category tagging is optional, like every annotation.
func LoadCategories(root string) *CategoryLexicon {
	cx := &CategoryLexicon{}
	data, err := os.ReadFile(filepath.Join(root, "categories.toml"))
	if err != nil {
		return cx
	}
	var f struct {
		Category []SharedCategory `toml:"category"`
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return cx
	}
	cx.Categories = f.Category
	cx.patterns = make([]*regexp.Regexp, len(cx.Categories))
	cx.avoids = make([]*regexp.Regexp, len(cx.Categories))
	for i, c := range cx.Categories {
		cx.patterns[i] = wordAlternation(c.Aliases)
		cx.avoids[i] = wordAlternation(c.Avoid)
	}
	return cx
}

// Resolve reads the category a file's own naming implies when the vendor's
// annotation rules said nothing. Directories are checked deepest-first —
// the folder is the deliberate label — and the filename stem last, so it
// only speaks when no folder did. Within one segment, entries win in
// lexicon order (specific before generic).
func (cx *CategoryLexicon) Resolve(stem string, dirs []string) string {
	id, _ := cx.ResolveSrc(stem, dirs)
	return id
}

// ResolveSrc is Resolve that also says which segment and alias answered.
func (cx *CategoryLexicon) ResolveSrc(stem string, dirs []string) (string, Source) {
	if len(cx.Categories) == 0 {
		return "", Source{}
	}
	segs := make([]string, 0, len(dirs)+1)
	for i := len(dirs) - 1; i >= 0; i-- {
		segs = append(segs, dirs[i])
	}
	segs = append(segs, stem)
	for _, s := range segs {
		norm := Normalize(s)
		if norm == "" {
			continue
		}
		pad := " " + norm + " "
		for i, c := range cx.Categories {
			if w, ok := hit(pad, cx.patterns[i], cx.avoids[i]); ok {
				return c.ID, Source{Tier: TierCategories, Segment: s, Word: w}
			}
		}
	}
	return "", Source{}
}
