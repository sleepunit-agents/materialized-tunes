package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/jbarket/materialized-tunes/internal/catalog"
)

var catalogCmd = &cobra.Command{
	Use:   "catalog",
	Short: "Inspect the source catalog",
}

type locationStatus struct {
	Name       string         `json:"name"`
	Files      int            `json:"files"`
	Bytes      int64          `json:"bytes"`
	LastScan   *time.Time     `json:"last_scan,omitempty"`
	Formats    map[string]int `json:"formats,omitempty"`
	AudioErrs  int            `json:"audio_errors,omitempty"`
	NotScanned bool           `json:"not_scanned,omitempty"`
}

var catalogStatusJSON bool

var catalogStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Per-location summary of what has been cataloged",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		if catalogStatusJSON {
			var out []locationStatus
			for _, lc := range ws.Config.Locations {
				entries, err := catalog.Load(ws.CatalogPath(lc.Name))
				if err != nil {
					return err
				}
				st := locationStatus{Name: lc.Name, Files: len(entries), Formats: map[string]int{}}
				if len(entries) == 0 {
					st.NotScanned = true
					st.Formats = nil
				}
				var last time.Time
				for _, e := range entries {
					st.Bytes += e.Size
					if e.ScannedAt.After(last) {
						last = e.ScannedAt
					}
					switch {
					case e.Audio != nil:
						st.Formats[e.Audio.Format]++
					case e.AudioErr != "":
						st.AudioErrs++
					default:
						st.Formats["other"]++
					}
				}
				if !last.IsZero() {
					st.LastScan = &last
				}
				out = append(out, st)
			}
			return emitJSON(out)
		}
		if len(ws.Config.Locations) == 0 {
			fmt.Println("no locations yet — add one with `mtunes location add`")
			return nil
		}
		for _, lc := range ws.Config.Locations {
			entries, err := catalog.Load(ws.CatalogPath(lc.Name))
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Printf("%s: not scanned yet (`mtunes scan %s`)\n", lc.Name, lc.Name)
				continue
			}

			var bytes int64
			var lastScan time.Time
			formats := map[string]int{}
			audioErrs := 0
			for _, e := range entries {
				bytes += e.Size
				if e.ScannedAt.After(lastScan) {
					lastScan = e.ScannedAt
				}
				switch {
				case e.Audio != nil:
					formats[e.Audio.Format]++
				case e.AudioErr != "":
					audioErrs++
				default:
					formats["other"]++
				}
			}

			fmt.Printf("%s: %d files, %s, last scan %s\n",
				lc.Name, len(entries), humanBytes(bytes), lastScan.Local().Format("2006-01-02 15:04"))
			names := make([]string, 0, len(formats))
			for f := range formats {
				names = append(names, f)
			}
			sort.Strings(names)
			for _, f := range names {
				fmt.Printf("  %-6s %d\n", f, formats[f])
			}
			if audioErrs > 0 {
				fmt.Printf("  %-6s %d (unparseable audio headers)\n", "errors", audioErrs)
			}
		}
		return nil
	},
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func init() {
	catalogStatusCmd.Flags().BoolVar(&catalogStatusJSON, "json", false, "emit JSON")
	catalogCmd.AddCommand(catalogStatusCmd)
}
