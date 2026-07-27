// mtunes-desktop wraps the embedded UI in a native window via Wails.
// Same assets, same API handlers as `mtunes ui` — the Wails asset server
// serves the frontend and falls through to the ui package's HTTP handler
// for /api/*, so the two entry points can never drift apart.
//
//	Build (macOS): CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
//	  go build -tags desktop,production -o mtunes-desktop ./cmd/mtunes-desktop
//
// (Wails v2.13 references UTType; recent SDKs need the framework linked
// explicitly outside the wails CLI's own build.)
// Dev:   wails dev (from this directory) for hot reload, if ever needed.
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/jbarket/materialized-tunes/internal/ui"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

func main() {
	// Finder launches don't inherit shell env, so fall back to the
	// conventional workspace location when MTUNES_WORKSPACE is unset.
	root := os.Getenv("MTUNES_WORKSPACE")
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cand := filepath.Join(home, "mtunes-library")
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				root = cand
			}
		}
	}
	if root == "" {
		fmt.Fprintln(os.Stderr, "mtunes-desktop: set MTUNES_WORKSPACE (no ~/mtunes-library found)")
		os.Exit(1)
	}
	ws, err := workspace.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mtunes-desktop:", err)
		os.Exit(1)
	}

	api := ui.Handler(ws)

	err = wails.Run(&options.App{
		Title:            "mtunes",
		Width:            1440,
		Height:           900,
		MinWidth:         1100,
		MinHeight:        700,
		BackgroundColour: &options.RGBA{R: 0x11, G: 0x13, B: 0x15, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  ui.Assets(),
			Handler: apiOnly(api), // anything not a static asset: /api/*
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "mtunes",
				Message: "sample libraries as materialized views",
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mtunes-desktop:", err)
		os.Exit(1)
	}
}

// apiOnly guards the fallback handler: static files come from the Wails
// asset server; only API (and cache-backed media) routes fall through.
func apiOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			h.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
