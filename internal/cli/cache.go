package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/cache"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage the content-addressed source cache (always safe to clear)",
}

var cacheStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Cache object count and size",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		files, bytes, err := cache.Status(filepath.Join(ws.Root, "cache", "objects"))
		if err != nil {
			return err
		}
		fmt.Printf("%d objects, %s\n", files, plan.HumanBytes(bytes))
		return nil
	},
}

var cacheClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Delete all cached objects (they re-pull on demand)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		dir := filepath.Join(ws.Root, "cache", "objects")
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return os.MkdirAll(dir, 0o755)
	},
}

func init() {
	cacheCmd.AddCommand(cacheStatusCmd, cacheClearCmd)
	rootCmd.AddCommand(cacheCmd)
}
