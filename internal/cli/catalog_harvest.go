package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/harvest"
)

var catalogHarvestCmd = &cobra.Command{
	Use:   "harvest [location...]",
	Short: "Derive per-file bpm/key/category from filenames + annotations into the local meta cache",
	Long: `Reads what vendors already put in filenames and folders ("_C#4", "124 Bpm",
"Champion Sub - 10A", "Bass Lines 166.5/") plus the annotation layer's
category and [[dir]] maps, and writes annotations-cache/meta/<location>.jsonl
keyed by content SHA. The UI's bpm/key/cat columns read it. Runs after
every scan automatically; this re-runs it on demand (e.g. after pulling
new annotations).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		targets := ws.Config.Locations
		if len(args) > 0 {
			targets = nil
			for _, name := range args {
				lc, ok := ws.Location(name)
				if !ok {
					return fmt.Errorf("no location named %q", name)
				}
				targets = append(targets, lc)
			}
		}
		for _, lc := range targets {
			h, err := harvest.Run(ws, lc)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %d files with metadata — %d bpm, %d key, %d category, %d instrument, %d tagged\n",
				lc.Name, h.Files, h.WithBPM, h.WithKey, h.WithCategory, h.WithInstrument, h.WithTags)
		}
		return nil
	},
}

func init() {
	catalogCmd.AddCommand(catalogHarvestCmd)
}
