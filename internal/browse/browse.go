// Package browse computes pack summaries — the browsing unit shared by
// `catalog packs` and the UI server. Pure aggregation over catalogs plus
// the annotation layer; no filesystem writes.
package browse

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

// Row is one pack summary — annotated when the location names a vendor.
type Row struct {
	Location string `json:"location"`
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Tier     string `json:"tier"` // "vendor" | "top-level-dirs"

	Slug          string   `json:"slug,omitempty"`
	URL           string   `json:"url,omitempty"`
	Image         string   `json:"image,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	SamplesListed int      `json:"samples_listed,omitempty"`
	Tags          []string `json:"tags,omitempty"`

	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`

	Eligible       int   `json:"eligible,omitempty"`        // device lens only
	ConvertedBytes int64 `json:"converted_bytes,omitempty"` // device lens only

	Match         string  `json:"match,omitempty"` // "exact" | "partial"
	MatchFraction float64 `json:"match_fraction,omitempty"`
}

// Rows aggregates pack summaries across locations. dev applies the device
// lens (nil = off); location filters to one location ("" = all).
func Rows(ws *workspace.Workspace, dev *profile.Device, location string) ([]Row, error) {
	vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
	if err != nil {
		return nil, err
	}
	bySlug := annotations.BySlug(vendors)

	var rows []Row
	for _, lc := range ws.Config.Locations {
		if location != "" && lc.Name != location {
			continue
		}
		entries, err := catalog.Load(ws.CatalogPath(lc.Name))
		if err != nil {
			return nil, err
		}

		groups := map[string][]catalog.Entry{}
		for _, ce := range entries {
			top, rest, found := strings.Cut(ce.Path, "/")
			if !found || rest == "" {
				continue // top-level files (sibling archives) are not pack content
			}
			groups[top] = append(groups[top], ce)
		}

		vendor := bySlug[lc.Vendor]
		for top, ces := range groups {
			row := Row{Location: lc.Name, Dir: top, Name: top, Tier: "top-level-dirs"}
			if vendor != nil {
				row.Tier = "vendor"
				if p := vendor.PackByDir(top); p != nil {
					row.Name, row.Slug, row.URL = p.Name, p.Slug, p.URL
					row.Image = p.Meta.Image
					row.Provider = p.Provider
					row.SamplesListed = p.SamplesListed
					row.Tags = p.Tags
					shas := make(map[string]bool, len(ces))
					for _, ce := range ces {
						if isAudioPath(ce.Path) {
							shas[ce.SHA256] = true
						}
					}
					row.Match, row.MatchFraction = vendor.MatchIdentity(p, shas)
				}
			}
			for _, ce := range ces {
				row.Files++
				row.Bytes += ce.Size
				if dev != nil && plan.Eligibility(dev, ce) == "" {
					row.Eligible++
					row.ConvertedBytes += plan.ConvertedBytes(dev, ce)
				}
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Location != rows[j].Location {
			return rows[i].Location < rows[j].Location
		}
		return rows[i].Dir < rows[j].Dir
	})
	return rows, nil
}

func isAudioPath(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".wav") || strings.HasSuffix(l, ".aif") || strings.HasSuffix(l, ".aiff")
}
