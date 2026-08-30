package cli

import (
	"fmt"
	"sort"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
)

var (
	lsDevice     string
	lsLocation   string
	lsGlob       string
	lsJSON       bool
	lsIneligible bool
)

// lsEntry is the machine shape for one catalog row — catalog.Entry plus
// where it lives and (under --device) why it can't ride.
type lsEntry struct {
	Location string `json:"location"`
	catalog.Entry
	Ineligible string `json:"ineligible,omitempty"`
}

var catalogLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List cataloged files; --device applies the device lens (only what can ride)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		var dev *profile.Device
		if lsDevice != "" {
			if dev, err = profile.LoadDevice(ws.Root, lsDevice); err != nil {
				return err
			}
		}
		if lsIneligible && dev == nil {
			return fmt.Errorf("--ineligible only makes sense with --device")
		}
		if lsGlob != "" && !doublestar.ValidatePattern(lsGlob) {
			return fmt.Errorf("bad glob %q", lsGlob)
		}

		var rows []lsEntry
		for _, lc := range ws.Config.Locations {
			if lsLocation != "" && lc.Name != lsLocation {
				continue
			}
			entries, err := catalog.Load(ws.CatalogPath(lc.Name))
			if err != nil {
				return err
			}
			for path, ce := range entries {
				if lsGlob != "" {
					if ok, _ := doublestar.Match(lsGlob, path); !ok {
						continue
					}
				}
				row := lsEntry{Location: lc.Name, Entry: ce}
				if dev != nil {
					row.Ineligible = plan.Eligibility(dev, ce)
					if lsIneligible == (row.Ineligible == "") {
						continue // lens: keep eligible by default, ineligible with the flag
					}
				}
				rows = append(rows, row)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Location != rows[j].Location {
				return rows[i].Location < rows[j].Location
			}
			return rows[i].Path < rows[j].Path
		})

		if lsJSON {
			return emitJSON(rows)
		}
		for _, r := range rows {
			line := fmt.Sprintf("%s:%s", r.Location, r.Path)
			if a := r.Audio; a != nil {
				line += fmt.Sprintf("  [%s %dch/%d/%d %.2fs]", a.Format, a.Channels, a.SampleRate, a.BitDepth, a.DurationS)
			}
			if r.Ineligible != "" {
				line += "  ✗ " + r.Ineligible
			}
			fmt.Println(line)
		}
		if !lsJSON {
			fmt.Printf("— %d files\n", len(rows))
		}
		return nil
	},
}

func init() {
	catalogLsCmd.Flags().StringVar(&lsDevice, "device", "", "device lens: show only files eligible for this device")
	catalogLsCmd.Flags().StringVar(&lsLocation, "location", "", "restrict to one location")
	catalogLsCmd.Flags().StringVar(&lsGlob, "glob", "", "restrict to paths matching this glob")
	catalogLsCmd.Flags().BoolVar(&lsJSON, "json", false, "emit JSON")
	catalogLsCmd.Flags().BoolVar(&lsIneligible, "ineligible", false, "invert the lens: show only what CAN'T ride, with reasons")
	catalogCmd.AddCommand(catalogLsCmd)
}
