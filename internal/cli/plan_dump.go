package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/correct"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/version"
)

var planDumpJSON bool

var planDumpCmd = &cobra.Command{
	Use:   "dump <view>",
	Short: "Every folder waiting for a decision, every file in it, and the why per facet",
	Long: `Builds the view's plan and prints its whole decision surface: each source
folder the Plan screen's queues would show (acked folders included, marked),
every file in it, the majority category and instrument with the tier that
answered, and each file's own answer where it differs. What the Plan
screen's "dump" chip downloads; --json is the same for tools.`,
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
		metas := map[string]map[string]harvest.Meta{}
		meta := func(loc string) map[string]harvest.Meta {
			m, ok := metas[loc]
			if !ok {
				m = harvest.LoadMeta(ws, loc)
				metas[loc] = m
			}
			return m
		}
		d := plan.BuildDump(p, meta, correct.Acks(ws))
		d.App = version.Version
		if planDumpJSON {
			return emitJSON(d)
		}
		return d.WriteText(os.Stdout)
	},
}

func init() {
	planDumpCmd.Flags().BoolVar(&planDumpJSON, "json", false, "machine-readable output")
	planCmd.AddCommand(planDumpCmd)
}
