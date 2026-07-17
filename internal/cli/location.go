package cli

import (
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/workspace"
)

var locationCmd = &cobra.Command{
	Use:   "location",
	Short: "Manage source locations (where immutable source material lives)",
}

var (
	locType string
	locRoot string
	locHost string
)

var locationAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a source location",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		name := args[0]
		if _, exists := ws.Location(name); exists {
			return fmt.Errorf("location %q already exists", name)
		}
		lc := workspace.LocationConfig{Name: name, Type: locType, Root: locRoot, Host: locHost}
		switch lc.Type {
		case "local":
			if lc.Host != "" {
				return fmt.Errorf("--host is only for ssh locations")
			}
		case "ssh":
			if lc.Host == "" {
				return fmt.Errorf("ssh locations need --host")
			}
		default:
			return fmt.Errorf("--type must be local or ssh")
		}
		if lc.Root == "" {
			return fmt.Errorf("--root is required")
		}
		ws.Config.Locations = append(ws.Config.Locations, lc)
		if err := ws.SaveConfig(); err != nil {
			return err
		}
		fmt.Printf("added %s (%s) — run `mtunes scan %s` to catalog it\n", name, lc.Type, name)
		return nil
	},
}

var locationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List source locations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		if len(ws.Config.Locations) == 0 {
			fmt.Println("no locations yet — add one with `mtunes location add`")
			return nil
		}
		for _, lc := range ws.Config.Locations {
			where := lc.Root
			if lc.Type == "ssh" {
				where = lc.Host + ":" + lc.Root
			}
			fmt.Printf("%-20s %-6s %s\n", lc.Name, lc.Type, where)
		}
		return nil
	},
}

var locationRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a source location (its catalog file is kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		name := args[0]
		before := len(ws.Config.Locations)
		ws.Config.Locations = slices.DeleteFunc(ws.Config.Locations, func(lc workspace.LocationConfig) bool {
			return lc.Name == name
		})
		if len(ws.Config.Locations) == before {
			return fmt.Errorf("no location named %q", name)
		}
		if err := ws.SaveConfig(); err != nil {
			return err
		}
		fmt.Printf("removed %s (catalog file kept at %s)\n", name, ws.CatalogPath(name))
		return nil
	},
}

func init() {
	locationAddCmd.Flags().StringVar(&locType, "type", "local", "location type: local or ssh")
	locationAddCmd.Flags().StringVar(&locRoot, "root", "", "root directory of the source material")
	locationAddCmd.Flags().StringVar(&locHost, "host", "", "ssh host (resolved via ~/.ssh/config)")
	locationCmd.AddCommand(locationAddCmd, locationListCmd, locationRemoveCmd)
}
