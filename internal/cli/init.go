package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

var initGit bool

var initCmd = &cobra.Command{
	Use:   "init <dir>",
	Short: "Scaffold a workspace (the directory that IS your library definition)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := workspace.Init(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("workspace ready at %s\n", ws.Root)

		// A workspace without the annotations checkout classifies nothing —
		// clone it now so the first scan works out of the box.
		if r := annotations.Sync(cmd.Context(), ws.Root); r.Note != "" {
			fmt.Println(r.Note)
		}

		if initGit {
			if _, err := os.Stat(filepath.Join(ws.Root, ".git")); os.IsNotExist(err) {
				git := exec.Command("git", "init")
				git.Dir = ws.Root
				if out, err := git.CombinedOutput(); err != nil {
					return fmt.Errorf("git init: %w: %s", err, out)
				}
				fmt.Println("initialized git repository")
			}
		} else {
			fmt.Println("tip: this directory deserves history — run with --git or `git init` it yourself")
		}
		fmt.Printf("tip: export %s=%s to skip --workspace\n", workspace.EnvVar, ws.Root)
		return nil
	},
}

func init() {
	initCmd.Flags().BoolVar(&initGit, "git", false, "also initialize a git repository")
}
