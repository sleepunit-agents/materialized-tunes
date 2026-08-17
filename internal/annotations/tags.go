package annotations

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bmatcuk/doublestar/v4"
)

// TagMap is the repo's tags.toml: the canonical vocabulary rules. Mirrors
// tools/tagmap.py — mechanical slug first, then aliases, then drops.
type TagMap struct {
	Drop    []string
	Aliases map[string][]string
}

var tagSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// LoadTagMap reads <root>/tags.toml. A missing file yields an empty map
// (mechanical normalization still applies).
func LoadTagMap(root string) *TagMap {
	tm := &TagMap{Aliases: map[string][]string{}}
	data, err := os.ReadFile(filepath.Join(root, "tags.toml"))
	if err != nil {
		return tm
	}
	var f struct {
		Drop    []string            `toml:"drop"`
		Aliases map[string][]string `toml:"aliases"`
	}
	if toml.Unmarshal(data, &f) == nil {
		tm.Drop = f.Drop
		if f.Aliases != nil {
			tm.Aliases = f.Aliases
		}
	}
	return tm
}

// Slug is the mechanical normalization: lowercase, non-alphanumerics
// collapse to "-".
func Slug(s string) string {
	return strings.Trim(tagSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// Canonical translates vendor tag phrasing into ordered, deduped canonical
// tags: slug → drop check → alias expansion.
func (tm *TagMap) Canonical(vendorTags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range vendorTags {
		s := Slug(raw)
		if s == "" {
			continue
		}
		dropped := false
		for _, pat := range tm.Drop {
			if ok, _ := doublestar.Match(pat, s); ok {
				dropped = true
				break
			}
		}
		if dropped {
			continue
		}
		cs, ok := tm.Aliases[s]
		if !ok {
			cs = []string{s}
		}
		for _, c := range cs {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
