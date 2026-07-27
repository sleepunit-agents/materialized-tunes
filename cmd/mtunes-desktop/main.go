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
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

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

	// Native menus: standard app/edit menus (Edit is what gives the webview
	// working copy/paste on macOS), Go — the main screens with ⌘1–4 — and
	// Recipes — one entry per recipe, alphabetical. Menu clicks reach the
	// frontend over the Wails event bus; the browser build keeps its own
	// bare-key shortcuts.
	var ctx context.Context
	emit := func(event, arg string) func(*menu.CallbackData) {
		return func(_ *menu.CallbackData) {
			if ctx != nil {
				wruntime.EventsEmit(ctx, event, arg)
			}
		}
	}
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())
	appMenu.Append(menu.EditMenu())
	goMenu := appMenu.AddSubmenu("Go")
	for i, sc := range []struct{ label, key string }{
		{"Library", "library"}, {"Recipe", "recipe"},
		{"Materialize", "run"}, {"Cards", "cards"},
	} {
		goMenu.AddText(sc.label, keys.CmdOrCtrl(fmt.Sprintf("%d", i+1)), emit("open-screen", sc.key))
	}
	recipesMenu := appMenu.AddSubmenu("Recipes")
	for _, name := range viewNames(ws) {
		recipesMenu.AddText(name, nil, emit("open-view", name))
	}

	err = wails.Run(&options.App{
		Title:            "Materialized Tunes",
		Width:            1440,
		Height:           900,
		MinWidth:         1100,
		MinHeight:        700,
		BackgroundColour: &options.RGBA{R: 0x11, G: 0x13, B: 0x15, A: 255},
		Menu:             appMenu,
		OnStartup:        func(c context.Context) { ctx = c },
		AssetServer: &assetserver.Options{
			Assets:  ui.Assets(),
			Handler: apiOnly(api), // anything not a static asset: /api/*
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
			About: &mac.AboutInfo{
				Title:   "Materialized Tunes",
				Message: "sample libraries as materialized views",
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mtunes-desktop:", err)
		os.Exit(1)
	}
}

func viewNames(ws *workspace.Workspace) []string {
	files, _ := filepath.Glob(filepath.Join(ws.Root, "views", "*.toml"))
	var names []string
	for _, f := range files {
		names = append(names, strings.TrimSuffix(filepath.Base(f), ".toml"))
	}
	sort.Strings(names)
	return names
}

// apiOnly guards the fallback handler: static files come from the Wails
// asset server; only API (and cache-backed media) routes fall through.
func apiOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
