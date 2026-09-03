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
	"log"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sleepunit-agents/materialized-tunes/internal/ui"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
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
	// The exe is a GUI-subsystem build: no console, so stderr is nowhere.
	// The log lives in the workspace (Setup names it) — and when the
	// workspace itself won't load, beside the exe, so there is always a
	// file that says why the window never opened.
	ws, err := workspace.Load(root)
	if err != nil {
		if exe, e := os.Executable(); e == nil {
			os.WriteFile(filepath.Join(filepath.Dir(exe), "mtunes-desktop-error.log"),
				[]byte(fmt.Sprintf("mtunes-desktop: workspace %s: %v\n", root, err)), 0o644)
		}
		fmt.Fprintln(os.Stderr, "mtunes-desktop:", err)
		os.Exit(1)
	}
	ui.OpenLog(ws.Root, false)

	srv := ui.New(ws)
	api := srv.Handler()
	desk := &Desktop{srv: srv}

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

	// Windows gets the frameless product window: the page draws the title
	// bar (drag region, — ▢ ✕) and the rail carries the modes, so the
	// native menu — whose only job on Windows was the Ctrl+1–4
	// accelerators — would be a stray strip under nothing. macOS keeps its
	// traffic lights (hidden-inset title bar) and the app/Edit menus the
	// webview needs for copy/paste; Linux keeps the native frame — WebKitGTK
	// frameless loses edge resizing. (redesign P1, 2026-09-03)
	frameless := goruntime.GOOS == "windows"
	var winMenu *menu.Menu
	if !frameless {
		winMenu = appMenu
	}
	err = wails.Run(&options.App{
		Title:            "Materialized Tunes",
		Width:            1440,
		Height:           900,
		MinWidth:         1100,
		MinHeight:        700,
		Frameless:        frameless,
		BackgroundColour: &options.RGBA{R: 0x11, G: 0x13, B: 0x15, A: 255},
		Menu:             winMenu,
		Windows: &windows.Options{
			DisableWindowIcon: true,
		},
		OnStartup: func(c context.Context) { ctx = c; desk.ctx = c },
		Bind:      []interface{}{desk},
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
		log.Printf("mtunes-desktop: %v", err)
		fmt.Fprintln(os.Stderr, "mtunes-desktop:", err)
		os.Exit(1)
	}
	log.Printf("exit: window closed")
}

// Desktop is what the page reaches through window.go.main.Desktop — the
// surfaces a webview cannot provide itself. A browser downloads a
// Content-Disposition: attachment response; the Wails webview has no
// download path, and window.open on wails.localhost lands in the OS
// browser where the origin means nothing (Jonathan, "dump opened a
// browser window … which obviously no work", v0.9.41). So the two "hand
// me a file" chips ask Go for the bytes and a native save dialog.
type Desktop struct {
	srv *ui.Server
	ctx context.Context
}

// SaveDump writes the plan dump for view where the user points the save
// dialog; returns the path, or "" when they cancelled.
func (d *Desktop) SaveDump(view string) (string, error) {
	name, text, err := d.srv.DumpText(view)
	if err != nil {
		return "", err
	}
	return d.save("Save plan dump", name, "Text (*.txt)", "*.txt", text)
}

// SaveLocalExport writes the annotations.local zip the same way.
func (d *Desktop) SaveLocalExport() (string, error) {
	name, b, err := d.srv.LocalExport()
	if err != nil {
		return "", err
	}
	return d.save("Save local layer", name, "Zip (*.zip)", "*.zip", b)
}

func (d *Desktop) save(title, name, filterName, pattern string, b []byte) (string, error) {
	if d.ctx == nil {
		return "", fmt.Errorf("window not ready")
	}
	dir, _ := os.UserHomeDir()
	if dl := filepath.Join(dir, "Downloads"); dir != "" {
		if st, err := os.Stat(dl); err == nil && st.IsDir() {
			dir = dl
		}
	}
	path, err := wruntime.SaveFileDialog(d.ctx, wruntime.SaveDialogOptions{
		Title:                title,
		DefaultDirectory:     dir,
		DefaultFilename:      name,
		CanCreateDirectories: true,
		Filters:              []wruntime.FileFilter{{DisplayName: filterName, Pattern: pattern}},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	log.Printf("saved %s (%d bytes)", path, len(b))
	return path, nil
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
