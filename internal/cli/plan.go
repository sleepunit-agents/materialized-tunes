package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/plan"
)

var planVerbose bool

var planCmd = &cobra.Command{
	Use:   "plan <view>",
	Short: "Pre-flight a view: exact sizes, fit check, collisions — nothing copied",
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
		p.Render(os.Stdout, planVerbose)
		if len(p.Errors) > 0 {
			return fmt.Errorf("plan has %d error(s)", len(p.Errors))
		}
		return nil
	},
}

func init() {
	planCmd.Flags().BoolVarP(&planVerbose, "verbose", "v", false, "list every output file")
	rootCmd.AddCommand(planCmd)
}
