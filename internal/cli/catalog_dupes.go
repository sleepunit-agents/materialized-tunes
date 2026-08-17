package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

var (
	dupesJSON bool
	dupesTop  int
)

// dupeGroup is one set of paths holding identical audio bytes.
type dupeGroup struct {
	SHA256 string   `json:"sha256"`
	Bytes  int64    `json:"bytes"`
	Paths  []string `json:"paths"` // "location:path"
}

var catalogDupesCmd = &cobra.Command{
	Use:   "dupes [location...]",
	Short: "Report identical audio files (same content SHA) across the catalog",
	Long: `Vendors ship the same WAV in several format trees and again inside kit
folders; the same sample turns up in factory content and in the vendor's
own pack. This lists those groups — across locations, since a duplicate
on the workstation and on the laptop is still one sample. Views can drop
them at plan time with dedup = "content"; this report is for seeing what
that would touch.`,
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
		bySHA := map[string]*dupeGroup{}
		audioFiles := 0
		for _, lc := range targets {
			entries, err := catalog.Load(ws.CatalogPath(lc.Name))
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e.Audio == nil {
					continue
				}
				audioFiles++
				g := bySHA[e.SHA256]
				if g == nil {
					g = &dupeGroup{SHA256: e.SHA256, Bytes: e.Size}
					bySHA[e.SHA256] = g
				}
				g.Paths = append(g.Paths, lc.Name+":"+e.Path)
			}
		}
		var groups []dupeGroup
		var extraFiles int
		var extraBytes int64
		for _, g := range bySHA {
			if len(g.Paths) < 2 {
				continue
			}
			sort.Strings(g.Paths)
			groups = append(groups, *g)
			extraFiles += len(g.Paths) - 1
			extraBytes += int64(len(g.Paths)-1) * g.Bytes
		}
		sort.Slice(groups, func(i, j int) bool {
			if len(groups[i].Paths) != len(groups[j].Paths) {
				return len(groups[i].Paths) > len(groups[j].Paths)
			}
			return groups[i].Paths[0] < groups[j].Paths[0]
		})
		if dupesJSON {
			return emitJSON(map[string]any{
				"audio_files": audioFiles, "groups": len(groups),
				"extra_files": extraFiles, "extra_bytes": extraBytes, "dupes": groups,
			})
		}
		fmt.Printf("%d audio files, %d duplicate groups: %d redundant copies, %s\n",
			audioFiles, len(groups), extraFiles, plan.HumanBytes(extraBytes))
		show := groups
		if dupesTop > 0 && len(show) > dupesTop {
			show = show[:dupesTop]
		}
		for _, g := range show {
			fmt.Printf("\n  ×%d  %s\n", len(g.Paths), plan.HumanBytes(g.Bytes))
			for _, p := range g.Paths {
				fmt.Printf("      %s\n", p)
			}
		}
		if len(show) < len(groups) {
			fmt.Printf("\n  … %d more groups (--top 0 for all, --json for everything)\n", len(groups)-len(show))
		}
		return nil
	},
}

func init() {
	catalogDupesCmd.Flags().BoolVar(&dupesJSON, "json", false, "machine-readable output")
	catalogDupesCmd.Flags().IntVar(&dupesTop, "top", 20, "groups to print (0 = all)")
	catalogCmd.AddCommand(catalogDupesCmd)
}
