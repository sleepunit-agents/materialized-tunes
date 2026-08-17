package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/resolve"
)

var catalogResolveCmd = &cobra.Command{
	Use:   "resolve [location...]",
	Short: "Identify marketplace packs (Splice) via the vendor's public API; cache locally",
	Long: `Marketplaces list more packs every day, so the annotations repo ships their
grammar but no per-pack files. For a location whose vendor declares a
resolver, this asks the vendor's API about each pack dir it hasn't seen
(name, slug, provider, product URL, cover pointer, tags) and caches the
answer in annotations-cache/resolve/<vendor>/. Cached answers are reused;
delisted packs are remembered and re-asked after a month. Runs after every
scan of such a location; this re-runs it on demand.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
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
			isTTY := fileIsTTY(os.Stderr)
			res, err := resolve.Location(cmd.Context(), ws, lc, vendors, func(done, total int) {
				if isTTY {
					fmt.Fprintf(os.Stderr, "\r  %s resolve %d/%d", lc.Name, done, total)
				}
			})
			if isTTY {
				fmt.Fprint(os.Stderr, "\r\033[K")
			}
			if err != nil {
				return err
			}
			if res.Packs == 0 {
				continue
			}
			fmt.Printf("%s: %d packs — %d resolved now, %d cached, %d not on the vendor, %d failed\n",
				lc.Name, res.Packs, res.Resolved, res.Cached, res.Missing, res.Failed)
		}
		return nil
	},
}

func init() {
	catalogCmd.AddCommand(catalogResolveCmd)
}
