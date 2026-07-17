package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/materialize"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

var (
	matTarget string
	matForce  bool
)

var materializeCmd = &cobra.Command{
	Use:   "materialize <view>",
	Short: "Render a view to a target directory and write its lockfile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		p, err := plan.Build(ws, args[0])
		if err != nil {
			return err
		}
		p.Render(os.Stdout, false)
		if len(p.Errors) > 0 && !matForce {
			return fmt.Errorf("plan has %d error(s) — fix them or pass --force", len(p.Errors))
		}
		if len(p.Entries) == 0 {
			return fmt.Errorf("nothing selected — nothing to materialize")
		}
		if entries, err := os.ReadDir(matTarget); err == nil && len(entries) > 0 {
			fmt.Printf("\nnote: %s is not empty; existing files are overwritten, extras are left alone\n", matTarget)
		}

		fmt.Println()
		out, err := materialize.Materialize(cmd.Context(), ws, p, matTarget, progressLine("materialize"))
		clearProgress()
		if err != nil {
			return err
		}
		for _, w := range out.Warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		fmt.Printf("wrote %d files (%s) to %s\nlock: %s\n",
			out.Written, plan.HumanBytes(out.Bytes), matTarget, out.LockPath)
		return nil
	},
}

var restoreTarget string

var restoreCmd = &cobra.Command{
	Use:   "restore <lock-file-or-view>",
	Short: "Re-materialize a past lockfile exactly (a view name means its newest lock)",
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
		fmt.Printf("restoring %s (%s, %d files, %s)\n",
			l.View, l.Created.Local().Format("2006-01-02 15:04"), l.Totals.Files, plan.HumanBytes(l.Totals.Bytes))

		out, err := materialize.Restore(cmd.Context(), ws, l, lockPath, restoreTarget, progressLine("restore"))
		clearProgress()
		if err != nil {
			return err
		}
		for _, w := range out.Warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		fmt.Printf("wrote %d files (%s) to %s\n", out.Written, plan.HumanBytes(out.Bytes), restoreTarget)
		return nil
	},
}

func progressLine(verb string) func(int, int) {
	if !fileIsTTY(os.Stderr) {
		return nil
	}
	return func(done, total int) {
		fmt.Fprintf(os.Stderr, "\r  %s %d/%d", verb, done, total)
	}
}

func clearProgress() {
	if fileIsTTY(os.Stderr) {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

func init() {
	materializeCmd.Flags().StringVar(&matTarget, "to", "", "target directory (card mount or staging folder)")
	materializeCmd.Flags().BoolVar(&matForce, "force", false, "materialize despite plan errors")
	materializeCmd.MarkFlagRequired("to")
	restoreCmd.Flags().StringVar(&restoreTarget, "to", "", "target directory")
	restoreCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(materializeCmd, restoreCmd)
}
