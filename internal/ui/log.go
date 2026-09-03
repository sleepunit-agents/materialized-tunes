package ui

// The desktop build is a GUI subsystem exe: it has no console, so anything
// written to stderr — a swallowed catalog error, a panic in a handler, the
// reason the window came up empty — went nowhere. "I think something might
// be fucked ... we may need some kind of debug build to get you some logs"
// (Jonathan, 2026-09-02). There is no debug build; every build keeps a log
// in the workspace, and the app names it on Setup.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/version"
)

var (
	logMu   sync.Mutex
	logPath string
)

// OpenLog routes the process log to <root>/logs/mtunes.log (and stderr
// too when tee is set — the CLI has a console, the desktop exe doesn't).
// The previous log rolls to mtunes.log.1 when it has grown past a few
// megabytes, so the file a user is asked to send is bounded and the
// launch before this one is still there. Returns the path, "" when the
// file could not be opened (the log then stays on stderr).
func OpenLog(root string, tee bool) string {
	dir := filepath.Join(root, "logs")
	p := filepath.Join(dir, "mtunes.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("log: %v (logging to stderr only)", err)
		return ""
	}
	if st, err := os.Stat(p); err == nil && st.Size() > 4<<20 {
		os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("log: %v (logging to stderr only)", err)
		return ""
	}
	var w io.Writer = f
	if tee {
		w = io.MultiWriter(f, os.Stderr)
	}
	log.SetOutput(w)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	logMu.Lock()
	logPath = p
	logMu.Unlock()
	commit := version.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	log.Printf("launch: mtunes %s (%s) %s/%s %s pid %d workspace %s",
		version.Version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version(), os.Getpid(), root)
	return p
}

// LogPath is where the process log lives, "" when OpenLog hasn't run or
// couldn't open the file.
func LogPath() string {
	logMu.Lock()
	defer logMu.Unlock()
	return logPath
}

// guard runs fn on the current goroutine and turns a panic into a log
// line with the stack instead of a dead process. Background work — the
// launch re-harvest, a scan, a plan build, a materialize run — runs under
// it: one bad file must not take the window down with it, and the reason
// must be on disk.
func guard(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in %s: %v\n%s", what, r, debug.Stack())
		}
	}()
	fn()
}

// statusWriter records what a handler answered so the log can say so.
// Error bodies are small JSON; the first couple of KB is kept for the line.
type statusWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && w.body.Len() < 2048 {
		w.body.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// logged wraps the API: every request that fails, takes over a second,
// or panics leaves a line — method, path, status, duration, the error
// body. A panic answers 500 with the message instead of dropping the
// connection, so the frontend can show it rather than a blank screen.
func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			dur := time.Since(start).Round(time.Millisecond)
			if p := recover(); p != nil {
				log.Printf("PANIC %s %s after %s: %v\n%s", r.Method, r.URL.Path, dur, p, debug.Stack())
				if sw.status == 0 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("internal error in %s: %v — see the log on Setup", r.URL.Path, p)})
				}
				return
			}
			if sw.status >= 400 || dur >= time.Second {
				log.Printf("%s %s → %d in %s %s", r.Method, r.URL.RequestURI(), sw.status, dur, bytes.TrimSpace(sw.body.Bytes()))
			}
		}()
		h.ServeHTTP(sw, r)
	})
}

// clientLog is POST /api/log: the page's own failures — a throw in a
// render, a promise nobody caught, a sound the element could not play —
// land in the same file as the server's, as CLIENT lines. Until this the
// desktop build's only record of a page-side break was the red panel on
// screen: the log Jonathan sent had nothing in it because nothing on the
// page could write to it ("The play() request was interrupted by a new
// load request", Fix, 2026-09-03). The body carries the page's trace —
// the last forty things it did — so the line says what led up to it.
func (s *Server) clientLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, fmt.Errorf("POST only"))
		return
	}
	var in struct {
		Where   string   `json:"where"`
		Message string   `json:"message"`
		Stack   string   `json:"stack"`
		Fatal   bool     `json:"fatal"`
		Screen  string   `json:"screen"`
		Player  string   `json:"player"`
		Trace   []string `json:"trace"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	kind := "error"
	if in.Fatal {
		kind = "FATAL"
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "CLIENT %s in %s: %s", kind, in.Where, strings.TrimSpace(in.Message))
	if in.Screen != "" || in.Player != "" {
		fmt.Fprintf(&b, " (screen %s, playing %q)", in.Screen, in.Player)
	}
	if st := strings.TrimSpace(in.Stack); st != "" && st != strings.TrimSpace(in.Message) {
		fmt.Fprintf(&b, "\n  stack: %s", strings.ReplaceAll(st, "\n", "\n         "))
	}
	if len(in.Trace) > 0 {
		b.WriteString("\n  trace (oldest first):")
		for _, t := range in.Trace {
			fmt.Fprintf(&b, "\n    %s", t)
		}
	}
	log.Print(b.String())
	jsonOut(w, map[string]bool{"ok": true})
}
