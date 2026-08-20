package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/browse"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

var (
	packsDevice   string
	packsLocation string
	packsJSON     bool
	packsDiscover bool
	packsAll      bool
)

var catalogPacksCmd = &cobra.Command{
	Use:   "packs",
	Short: "List packs — the browsing unit — grouped from the catalog; annotations make them recognizable",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		if packsDiscover {
			return runDiscover(ws)
		}
		var dev *profile.Device
		if packsDevice != "" {
			if dev, err = profile.LoadDevice(ws.Root, packsDevice); err != nil {
				return err
			}
		}
		rows, err := browse.Rows(ws, dev, packsLocation)
		if err != nil {
			return err
		}

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

// runDiscover lists registry identities you don't hold (SPEC §11.6).
// Obtainable classes only by default; --all adds the "recognized, not
// sourced" tail (orphans, unclassified) — reference material, never a
// storefront, so it stays behind the flag.
func runDiscover(ws *workspace.Workspace) error {
	rows, err := browse.Discover(ws)
	if err != nil {
		return err
	}
	if !packsAll {
		kept := rows[:0]
		for _, r := range rows {
			if r.Obtainable() {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	if packsJSON {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	for _, r := range rows {
		line := fmt.Sprintf("%-18s %-52s", r.Vendor, r.Name)
		if r.Obtainable() {
			line += fmt.Sprintf(" %-12s %s", r.Class, r.URL)
		} else {
			line += " recognized, not sourced"
		}
		if r.HaveFraction >= 0.999 {
			line += "   [content already in library]"
		} else if r.HaveFraction > 0 {
			line += fmt.Sprintf("   [%d%% of content already in library]", int(r.HaveFraction*100))
		}
		for _, rel := range r.Relations {
			if rel.Owned && rel.Inverse {
				line += fmt.Sprintf("   [you own its %s: %s]", rel.Type, rel.Pack)
			}
		}
		fmt.Println(line)
	}
	return nil
}

func init() {
	catalogPacksCmd.Flags().StringVar(&packsDevice, "device", "", "apply the device lens: eligible counts and converted sizes")
	catalogPacksCmd.Flags().StringVar(&packsLocation, "location", "", "limit to one location")
	catalogPacksCmd.Flags().BoolVar(&packsJSON, "json", false, "machine output, one array")
	catalogPacksCmd.Flags().BoolVar(&packsDiscover, "discover", false, "flip the filter: annotated packs NOT in your library, acquirable classes first")
	catalogPacksCmd.Flags().BoolVar(&packsAll, "all", false, "with --discover: include the recognized-not-sourced tail (orphans, unclassified)")
	catalogCmd.AddCommand(catalogPacksCmd)
}
