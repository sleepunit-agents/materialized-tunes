package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/browse"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
)

var (
	packsDevice   string
	packsLocation string
	packsJSON     bool
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

func init() {
	catalogPacksCmd.Flags().StringVar(&packsDevice, "device", "", "apply the device lens: eligible counts and converted sizes")
	catalogPacksCmd.Flags().StringVar(&packsLocation, "location", "", "limit to one location")
	catalogPacksCmd.Flags().BoolVar(&packsJSON, "json", false, "machine output, one array")
	catalogCmd.AddCommand(catalogPacksCmd)
}
