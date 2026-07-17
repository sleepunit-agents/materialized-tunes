// Package cli wires the mtunes command tree.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/workspace"
)

var workspaceFlag string

var rootCmd = &cobra.Command{
	Use:           "mtunes",
	Short:         "Sample libraries as materialized views over an immutable source library",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&workspaceFlag, "workspace", "w", "",
		"workspace directory (or set "+workspace.EnvVar+")")
	rootCmd.AddCommand(initCmd, locationCmd, scanCmd, catalogCmd)
}

func openWorkspace() (*workspace.Workspace, error) {
	root, err := workspace.Resolve(workspaceFlag)
	if err != nil {
		return nil, err
	}
	return workspace.Load(root)
}
