package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

var diffCmd = &cobra.Command{
	Use:   "diff <lock-file-or-view>",
	Short: "Staleness report: what re-running the recipe today would change vs the lock",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		lockPath, err := lock.Resolve(ws.Root, args[0])
		if err != nil {
			return err
		}
		l, err := lock.Read(lockPath)
		if err != nil {
			return err
		}
		p, err := plan.Build(ws, l.View)
		if err != nil {
			return err
		}

		shaByLoc := map[string]map[string]string{}
		for _, e := range l.Entries {
			if _, done := shaByLoc[e.Source.Location]; done {
				continue
			}
			entries, err := catalog.Load(ws.CatalogPath(e.Source.Location))
			if err != nil {
				return err
			}
			m := make(map[string]string, len(entries))
			for path, ce := range entries {
				m[path] = ce.SHA256
			}
			shaByLoc[e.Source.Location] = m
		}

		d := lock.Compute(l, p, shaByLoc)
		if diffJSON {
			return emitJSON(struct {
				Lock    string     `json:"lock"`
				View    string     `json:"view"`
				Created string     `json:"created"`
				Files   int        `json:"files"`
				InSync  bool       `json:"in_sync"`
				Diff    *lock.Diff `json:"diff"`
			}{lockPath, l.View, l.Created.UTC().Format("2006-01-02T15:04:05Z"), l.Totals.Files, d.Clean(), d})
		}
		fmt.Printf("lock %s (%s, %d files) vs recipe today:\n",
			l.View, l.Created.Local().Format("2006-01-02 15:04"), l.Totals.Files)
		if d.Clean() {
			fmt.Println("  in sync — a re-materialization would reproduce this lock")
			return nil
		}
		section := func(label string, items []string) {
			if len(items) == 0 {
				return
			}
			fmt.Printf("  %s (%d):\n", label, len(items))
			for _, it := range items {
				fmt.Printf("    %s\n", it)
			}
		}
		section("would ADD", d.Added)
		section("would DROP (deselected by recipe)", d.Deselected)
		section("would DROP (gone from source!)", d.GoneFromSrc)
		section("content changed at source", d.ContentDrift)
		section("transform changed (profile edits)", d.NewTransform)
		return nil
	},
}

var diffJSON bool

func init() {
	diffCmd.Flags().BoolVar(&diffJSON, "json", false, "emit JSON")
	rootCmd.AddCommand(diffCmd)
}
