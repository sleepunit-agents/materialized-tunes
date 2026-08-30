package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/cache"
	"github.com/sleepunit-agents/materialized-tunes/internal/lock"
	"github.com/sleepunit-agents/materialized-tunes/internal/materialize"
)

var verifyCard string

var verifyCmd = &cobra.Command{
	Use:   "verify [lock-file-or-view] --card <path>",
	Short: "Check a card/folder against its lockfile, hash by hash",
	Long: "With no lock argument, the card's " + materialize.CardMetaFile +
		" identifies the lock. Device-written extras (.ot files etc.) are reported, never flagged.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}

		var lockPath string
		if len(args) == 1 {
			if lockPath, err = lock.Resolve(ws.Root, args[0]); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(filepath.Join(verifyCard, materialize.CardMetaFile))
			if err != nil {
				return fmt.Errorf("no %s on %s — name the lock explicitly", materialize.CardMetaFile, verifyCard)
			}
			var meta materialize.CardMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				return err
			}
			lockPath = filepath.Join(ws.Root, "locks", meta.View, meta.Lock)
			fmt.Printf("card identifies as view %s, lock %s\n", meta.View, meta.Lock)
		}
		l, err := lock.Read(lockPath)
		if err != nil {
			return err
		}

		want := map[string]string{}
		for _, e := range l.Entries {
			want[e.Output.Path] = e.Output.SHA256
		}

		var missing, mismatched, extras []string
		seen := map[string]bool{}
		err = filepath.WalkDir(verifyCard, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasPrefix(d.Name(), ".") && path != verifyCard {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(verifyCard, path)
			rel = filepath.ToSlash(rel)
			wantSHA, ok := want[rel]
			if !ok {
				extras = append(extras, rel)
				return nil
			}
			seen[rel] = true
			got, err := cache.HashFile(path)
			if err != nil {
				return err
			}
			if got != wantSHA {
				mismatched = append(mismatched, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for p := range want {
			if !seen[p] {
				missing = append(missing, p)
			}
		}

		fmt.Printf("%d files in lock: %d verified", len(want), len(seen)-len(mismatched))
		if len(mismatched) > 0 {
			fmt.Printf(", %d MISMATCHED", len(mismatched))
		}
		if len(missing) > 0 {
			fmt.Printf(", %d MISSING", len(missing))
		}
		fmt.Println()
		for _, p := range mismatched {
			fmt.Printf("  ✗ mismatch: %s\n", p)
		}
		for _, p := range missing {
			fmt.Printf("  ✗ missing: %s\n", p)
		}
		if len(extras) > 0 {
			fmt.Printf("  ℹ %d files not in lock (device-written or foreign):\n", len(extras))
			for _, p := range extras {
				fmt.Printf("      %s\n", p)
			}
		}
		if len(mismatched)+len(missing) > 0 {
			return fmt.Errorf("verification failed")
		}
		return nil
	},
}

func init() {
	verifyCmd.Flags().StringVar(&verifyCard, "card", "", "card mount or output directory to verify")
	verifyCmd.MarkFlagRequired("card")
	rootCmd.AddCommand(verifyCmd)
}
