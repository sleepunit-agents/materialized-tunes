package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/materialize"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

var (
	migTarget string
	migDryRun bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <view>",
	Short: "Move a materialized target to the recipe's new layout by renaming, not re-rendering",
	Long: `After a layout (or ` + "`as`" + `) change, every locked file still has the right
bytes — only the paths changed. migrate renames them into place on the
target (near-instant on one volume), re-rewrites Ableton companions so
their sample refs follow, and writes a new lock. Nothing is transcoded.
Entries a rename can't satisfy — new selections, changed sources, changed
transforms — are left for a follow-up materialize, which resumes cheaply.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		p, err := plan.Build(ws, args[0])
		if err != nil {
			return err
		}
		if len(p.Errors) > 0 {
			for _, e := range p.Errors {
				fmt.Printf("  ✗ %s\n", e)
			}
			return fmt.Errorf("plan has %d error(s) — migrating onto a broken plan moves files to paths that won't survive the fix", len(p.Errors))
		}
		lockPath, err := lock.Resolve(ws.Root, args[0])
		if err != nil {
			return fmt.Errorf("%s has never been materialized — nothing to migrate (%v)", args[0], err)
		}
		l, err := lock.Read(lockPath)
		if err != nil {
			return err
		}
		m := lock.PlanMigration(l, p)
		fmt.Printf("lock %s (%s, %d files) vs recipe today:\n",
			l.View, l.Created.Local().Format("2006-01-02 15:04"), l.Totals.Files)
		fmt.Printf("  %d to move (rename in place), %d companions to rewrite, %d already where the recipe wants them\n",
			len(m.Moves), len(m.Companions), m.Stay)
		if m.Pending > 0 {
			fmt.Printf("  %d entries need a materialize (new, changed at source, or new transform) — migrate leaves them\n", m.Pending)
		}
		if m.Orphans > 0 {
			fmt.Printf("  %d locked files are no longer selected by the recipe — left where they are\n", m.Orphans)
		}
		if m.Work() == 0 {
			fmt.Println("nothing to migrate")
			if m.Pending > 0 {
				fmt.Printf("run `mtunes materialize %s` for the %d pending entries\n", args[0], m.Pending)
			}
			return nil
		}
		if migDryRun {
			for _, mv := range m.Moves {
				fmt.Printf("  %s → %s\n", mv.Old, mv.New)
			}
			for _, cm := range m.Companions {
				if cm.Old == cm.New {
					fmt.Printf("  %s (refs rewritten in place)\n", cm.Old)
				} else {
					fmt.Printf("  %s → %s (rewritten)\n", cm.Old, cm.New)
				}
			}
			return nil
		}
		target := migTarget
		if target == "" {
			target = expandHome(p.View.Target)
			if target == "" {
				return fmt.Errorf("no target: pass --to, or set target = \"...\" in the view")
			}
			fmt.Printf("target %s (from view)\n", target)
		}

		fmt.Println()
		out, err := materialize.Migrate(cmd.Context(), ws, l, p, m, target, progressLine("migrate"))
		clearProgress()
		if err != nil {
			return err
		}
		for _, w := range out.Warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		printSkips(out.Skipped)
		fmt.Printf("moved %d files, rewrote %d companions in %s\n", out.Renamed, out.Rewritten, target)
		if out.LockPath != "" {
			fmt.Printf("lock: %s\n", out.LockPath)
		}
		if out.Pending > 0 || len(out.Skipped) > 0 {
			fmt.Printf("run `mtunes materialize %s` to render the %d entries migrate could not move\n",
				args[0], out.Pending+len(out.Skipped))
		}
		return nil
	},
}

func init() {
	migrateCmd.Flags().StringVar(&migTarget, "to", "", "target directory holding the last materialize; defaults to the view's target")
	migrateCmd.Flags().BoolVar(&migDryRun, "dry-run", false, "list the renames without touching the target")
	rootCmd.AddCommand(migrateCmd)
}
