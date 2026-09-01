// Discover — the registry with the ownership filter flipped (SPEC §11.6).
// One browser over the annotations registry: the library is the registry
// filtered to what you hold; discover is everything else. Identity is
// unconditional, pointers are gated by acquisition class, and nothing here
// ever fetches bytes — the pointer is a page the vendor wants the customer
// on, and mtunes only links out.
package browse

import (
	"sort"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// DiscoverRow is one registry identity not held in any location — the thin
// card. Registry-level identity only, by design: no per-file browsing, no
// auditioning. The asymmetry against an owned pack's rich row is the
// ownership boundary made visible.
type DiscoverRow struct {
	Vendor     string `json:"vendor"`
	VendorSlug string `json:"vendor_slug"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`

	Description   string   `json:"description,omitempty"`
	Image         string   `json:"image,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	SamplesListed int      `json:"samples_listed,omitempty"`
	Discontinued  bool     `json:"discontinued,omitempty"`

	// Acquisition — the gated pointer. Class "" means the pack has no
	// [acquisition] record yet: recognized, but not listed as acquirable.
	// Orphans and unclassified packs carry no URL here, ever.
	Class   string `json:"class,omitempty"`   // vendor-free | vendor-paid | distributor | orphan | ""
	URL     string `json:"url,omitempty"`     // acquisition pointer (a page, never a file)
	Via     string `json:"via,omitempty"`     // distributor display name, iff class = "distributor"
	Gate    string `json:"gate,omitempty"`    // what the page asks for: none | email | account | purchase
	License string `json:"license,omitempty"` // ceiling on claims — the UI may show less, never more

	// HaveFraction: how much of this pack's manifest is already present
	// somewhere in the library (content-derived, no assertion needed). 1.0
	// means you already hold every byte — the freebie-subset case.
	HaveFraction float64 `json:"have_fraction,omitempty"`

	Relations []Related `json:"relations,omitempty"`
}

// Obtainable reports whether the row belongs in the default discover view —
// acquirable classes only. Orphans and unclassified identities sit behind
// the "recognized, not sourced" toggle instead (SPEC §11.6: never lead with
// "go get Jungle Warfare 3" when the honest answer is "not with our help").
func (r *DiscoverRow) Obtainable() bool {
	switch r.Class {
	case "vendor-free", "vendor-paid", "distributor":
		return r.URL != ""
	}
	return false
}

// Related is a relation hint against another registry pack, from either
// direction: Inverse=false means this pack asserts the relation ("this is a
// sampler-of X"), Inverse=true means the other pack asserts it toward this
// one ("Y is a sampler-of this"). Owned says whether the other side is in
// the library — the pair that makes discovery smart: an owned sampler of an
// unowned full pack is the upgrade path; an owned superset of an unowned
// freebie means skip it.
type Related struct {
	Type    string `json:"type"`
	Pack    string `json:"pack"` // display name of the other pack
	Key     string `json:"key"`  // "<vendor slug>/<pack slug>"
	Owned   bool   `json:"owned"`
	Inverse bool   `json:"inverse,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Discover lists registry identities not matched by anything in the
// catalog. Read-only over annotations + catalogs; no network. Marketplace
// vendors (resolver-backed, no repo packs) contribute nothing — the
// registry is the union of the community's shelves, not a vendor crawl.
func Discover(ws *workspace.Workspace) ([]DiscoverRow, error) {
	vendors, err := annotations.Load(ws.AnnotationRoots()...)
	if err != nil {
		return nil, err
	}
	bySlug := annotations.BySlug(vendors)

	rows, err := Rows(ws, nil, "")
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, r := range rows {
		if r.Slug != "" && r.VendorSlug != "" {
			owned[r.VendorSlug+"/"+r.Slug] = true
		}
	}

	// Every audio SHA in the library, for content-derived containment.
	shas := map[string]bool{}
	for _, lc := range ws.Config.Locations {
		entries, err := catalog.Load(ws.CatalogPath(lc.Name))
		if err != nil {
			continue // an unscanned location holds nothing yet
		}
		for _, ce := range entries {
			if isAudioPath(ce.Path) {
				shas[ce.SHA256] = true
			}
		}
	}

	// Index for relation resolution, both directions.
	type ref struct {
		vendor *annotations.Vendor
		pack   *annotations.Pack
	}
	byKey := map[string]ref{}
	for i := range vendors {
		v := &vendors[i]
		for j := range v.Packs {
			byKey[v.Slug+"/"+v.Packs[j].Slug] = ref{v, &v.Packs[j]}
		}
	}
	inverse := map[string][]Related{} // target key -> assertions pointing at it
	for key, r := range byKey {
		for _, rel := range r.pack.Relations {
			inverse[rel.Pack] = append(inverse[rel.Pack], Related{
				Type: rel.Type, Pack: r.pack.Name, Key: key,
				Owned: owned[key], Inverse: true, Note: rel.Note,
			})
		}
	}

	var out []DiscoverRow
	for i := range vendors {
		v := &vendors[i]
		for j := range v.Packs {
			p := &v.Packs[j]
			key := v.Slug + "/" + p.Slug
			if owned[key] {
				continue
			}
			row := DiscoverRow{
				Vendor: v.Name, VendorSlug: v.Slug,
				Name: p.Name, Slug: p.Slug,
				Description: p.Meta.Description, Image: p.Meta.Image,
				Tags: p.Tags, SamplesListed: p.SamplesListed,
				Discontinued: p.Discontinued,
			}
			if a := p.Acquisition; a != nil {
				row.Class, row.Gate, row.License = a.Class, a.Gate, a.License
				switch a.Class {
				case "vendor-free", "vendor-paid":
					row.URL = a.URL
				case "distributor":
					row.URL = a.URL
					if dv := bySlug[a.Via]; dv != nil {
						row.Via = dv.Name
					} else {
						row.Via = a.Via
					}
				}
				// orphan: no pointer, by rule — [pack] url stays archival
			}
			if _, frac := v.MatchIdentity(p, shas); frac > 0 {
				row.HaveFraction = frac
			}
			for _, rel := range p.Relations {
				h := Related{Type: rel.Type, Pack: rel.Pack, Key: rel.Pack, Owned: owned[rel.Pack], Note: rel.Note}
				if other, ok := byKey[rel.Pack]; ok {
					h.Pack = other.pack.Name
				}
				row.Relations = append(row.Relations, h)
			}
			row.Relations = append(row.Relations, inverse[key]...)
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
