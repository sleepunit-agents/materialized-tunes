package cli

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/ui"
)

var uiAddr string

var uiCmd = &cobra.Command{
	Use:   "ui",
	Short: "Serve the browser UI (embedded; localhost only)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", uiAddr)
		if err != nil {
			return err
		}
		url := "http://" + ln.Addr().String()
		if p := ui.OpenLog(ws.Root, true); p != "" {
			fmt.Printf("mtunes ui → %s  (ctrl-c to stop; log %s)\n", url, p)
		} else {
			fmt.Printf("mtunes ui → %s  (ctrl-c to stop)\n", url)
		}
		go func() {
			time.Sleep(300 * time.Millisecond)
			if runtime.GOOS == "darwin" {
				exec.Command("open", url).Start()
			}
		}()
		return http.Serve(ln, ui.Handler(ws))
	},
}

func init() {
	uiCmd.Flags().StringVar(&uiAddr, "addr", "127.0.0.1:7315", "listen address (keep it loopback)")
	rootCmd.AddCommand(uiCmd)
}
