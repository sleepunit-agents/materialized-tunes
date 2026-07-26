package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
)

var (
	packsDevice   string
	packsLocation string
	packsJSON     bool
)

// packRow is one pack summary — the browsing unit, derived from catalog
// paths plus (when the location names a vendor) the annotation layer.
type packRow struct {
	Location string `json:"location"`
	Dir      string `json:"dir"`
	Name     string `json:"name"`
	Tier     string `json:"tier"` // "vendor" | "top-level-dirs"

	Slug        string `json:"slug,omitempty"`
	URL         string `json:"url,omitempty"`
	Image       string `json:"image,omitempty"`
	Description string `json:"description,omitempty"`

	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`

	Eligible       int   `json:"eligible,omitempty"`        // --device only
	ConvertedBytes int64 `json:"converted_bytes,omitempty"` // --device only

	Match         string  `json:"match,omitempty"` // "exact" | "partial"
	MatchFraction float64 `json:"match_fraction,omitempty"`
}

var catalogPacksCmd = &cobra.Command{
	Use:   "packs",
	Short: "List packs — the browsing unit — grouped from the catalog; annotations make them recognizable",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		var dev *profile.Device
		if packsDevice != "" {
			if dev, err = profile.LoadDevice(ws.Root, packsDevice); err != nil {
				return err
			}
		}
		vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
		if err != nil {
			return err
		}
		bySlug := annotations.BySlug(vendors)

		var rows []packRow
		for _, lc := range ws.Config.Locations {
			if packsLocation != "" && lc.Name != packsLocation {
				continue
			}
			entries, err := catalog.Load(ws.CatalogPath(lc.Name))
			if err != nil {
				return err
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
				row := packRow{Location: lc.Name, Dir: top, Name: top, Tier: "top-level-dirs"}
				if vendor != nil {
					row.Tier = "vendor"
					if p := vendor.PackByDir(top); p != nil {
						row.Name, row.Slug, row.URL = p.Name, p.Slug, p.URL
						row.Image, row.Description = p.Meta.Image, p.Meta.Description
						shas := make(map[string]bool, len(ces))
						for _, ce := range ces {
							if strings.HasSuffix(strings.ToLower(ce.Path), ".wav") ||
								strings.HasSuffix(strings.ToLower(ce.Path), ".aif") ||
								strings.HasSuffix(strings.ToLower(ce.Path), ".aiff") {
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

		if packsJSON {
			return json.NewEncoder(os.Stdout).Encode(rows)
		}
		for _, r := range rows {
			line := fmt.Sprintf("%-9s %-52s %6d files %9s", r.Location, r.Name, r.Files, plan.HumanBytes(r.Bytes))
			if dev != nil {
				line += fmt.Sprintf("   %s: %d eligible, %s", dev.Name, r.Eligible, plan.HumanBytes(r.ConvertedBytes))
			}
			switch r.Match {
			case "exact":
				line += "   [complete]"
			case "partial":
				line += fmt.Sprintf("   [%d%% of pack]", int(r.MatchFraction*100))
			}
			fmt.Println(line)
		}
		return nil
	},
}

func init() {
	catalogPacksCmd.Flags().StringVar(&packsDevice, "device", "", "apply the device lens: eligible counts and converted sizes")
	catalogPacksCmd.Flags().StringVar(&packsLocation, "location", "", "limit to one location")
	catalogPacksCmd.Flags().BoolVar(&packsJSON, "json", false, "machine output, one array")
	catalogCmd.AddCommand(catalogPacksCmd)
}
