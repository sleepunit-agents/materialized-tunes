package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/location"
	"github.com/sleepunit-agents/materialized-tunes/internal/resolve"
	"github.com/sleepunit-agents/materialized-tunes/internal/scan"
)

var scanCmd = &cobra.Command{
	Use:   "scan [location...]",
	Short: "Build or refresh the catalog (incremental: unchanged files are free)",
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
		if len(targets) == 0 {
			return fmt.Errorf("no locations to scan — add one with `mtunes location add`")
		}

		// Vendor grammar moves on its own cadence; scan reads whatever the
		// checkout holds, so freshen it first (never fatal — see Sync).
		if r := annotations.Sync(cmd.Context(), ws.Root); r.Note != "" {
			fmt.Println(r.Note)
		}

		for _, lc := range targets {
			loc, err := location.New(lc)
			if err != nil {
				return err
			}
			fmt.Printf("scanning %s...\n", lc.Name)
			isTTY := fileIsTTY(os.Stderr)
			res, err := scan.Run(cmd.Context(), loc, ws.CatalogPath(lc.Name),
				func(stage string, done, total int) {
					if isTTY && (done%100 == 0 || done == total) {
						fmt.Fprintf(os.Stderr, "\r  %s %d/%d", stage, done, total)
					}
				})
			if isTTY {
				fmt.Fprint(os.Stderr, "\r\033[K")
			}
			if err != nil {
				return err
			}
			fmt.Printf("  %d files: %d added, %d changed, %d removed, %d unchanged",
				res.Total, res.Added, res.Changed, res.Removed, res.Unchanged)
			if res.AudioErrs > 0 {
				fmt.Printf(", %d audio parse errors", res.AudioErrs)
			}
			if res.Docs > 0 {
				fmt.Printf(", %d Live documents read", res.Docs)
			}
			if res.DualMonoChecked > 0 {
				fmt.Printf(", %d stereo files checked for dual-mono", res.DualMonoChecked)
			}
			fmt.Println()
			if vendors, err := annotations.Load(ws.AnnotationRoots()...); err == nil {
				if r, err := resolve.Location(cmd.Context(), ws, lc, vendors, nil); err == nil && r.Packs > 0 && r.Resolved+r.Missing+r.Failed > 0 {
					fmt.Printf("  resolved %d packs via the vendor API (%d not found, %d failed)\n", r.Resolved, r.Missing, r.Failed)
				}
			}
			if h, err := harvest.Run(ws, lc); err == nil && h.Files > 0 {
				fmt.Printf("  harvested metadata for %d files: %d bpm, %d key, %d category, %d instrument\n",
					h.Files, h.WithBPM, h.WithKey, h.WithCategory, h.WithInstrument)
			}
		}
		return nil
	},
}

func fileIsTTY(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
