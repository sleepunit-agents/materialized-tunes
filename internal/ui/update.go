package ui

import (
	"net/http"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/selfupdate"
)

// ---- app self-update ----------------------------------------------------
//
// GET answers "is a newer build published" (throttled — the frontend polls).
// POST installs it: download, verify, swap the exe, and — unless the caller
// says otherwise — relaunch into the new build. One click from "fix pushed
// to main" to "running the fix" is the whole point: it's what makes the
// push-test-report loop with a live tester fast.

func (s *Server) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonOut(w, selfupdate.Check(r.Context()))
		return
	}
	note, err := selfupdate.Apply(r.Context())
	if err != nil {
		jsonErr(w, 500, err)
		return
	}
	restart := r.URL.Query().Get("restart") != "0"
	status := "installed"
	if restart {
		status = "restarting"
	}
	jsonOut(w, map[string]string{"status": status, "note": note})
	if restart {
		// Let the response reach the frontend before the process swaps out.
		go guard("self-update", func() {
			time.Sleep(400 * time.Millisecond)
			selfupdate.Restart()
		})
	}
}
