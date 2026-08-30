// Annotations sync: the workspace's annotations/ checkout is a clone of a
// public data repo that moves on its own cadence — vendor grammar changes
// when someone observes a new pack, not when mtunes releases. So the tool
// keeps the checkout fresh itself: clone it when it's missing, fast-forward
// it before every scan. Staleness is never an error — offline, diverged, or
// no git just means "using what you have", said in one line.
package annotations

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RepoURL is where the annotations data lives. Public, code-free, its own
// repo precisely so it can move without a binary release.
const RepoURL = "https://github.com/sleepunit-agents/sample-vendor-annotations.git"

type SyncAction string

const (
	SyncCloned  SyncAction = "cloned"  // fresh checkout created
	SyncUpdated SyncAction = "updated" // fast-forwarded to a new head
	SyncCurrent SyncAction = "current" // reached the remote, already at head
	SyncSkipped SyncAction = "skipped" // couldn't (or shouldn't) touch it — Note says why
)

type SyncResult struct {
	Action SyncAction
	Note   string // one line for humans; always set for skipped/updated
}

// Concurrent scans (the UI auto-scans several locations) must not race two
// git processes in one checkout, and hourly cadences shouldn't hammer the
// remote — serialize and throttle per directory.
var (
	syncMu   sync.Mutex
	lastSync = map[string]time.Time{}
)

const syncMinInterval = 10 * time.Minute

// Sync brings <wsRoot>/annotations up to date: clones it if it's missing or
// empty, fast-forward-pulls it otherwise. It never fails the caller's scan —
// every outcome is a SyncResult, and a checkout that isn't ours to manage
// (a symlinked working copy without git, local divergence) is left alone.
func Sync(ctx context.Context, wsRoot string) SyncResult {
	return syncFrom(ctx, wsRoot, RepoURL)
}

func syncFrom(ctx context.Context, wsRoot, repoURL string) SyncResult {
	syncMu.Lock()
	defer syncMu.Unlock()

	dir := filepath.Join(wsRoot, "annotations")
	if t, ok := lastSync[dir]; ok && time.Since(t) < syncMinInterval {
		return SyncResult{Action: SyncCurrent}
	}

	if _, err := exec.LookPath("git"); err != nil {
		return SyncResult{Action: SyncSkipped, Note: "git not found — keep annotations/ current yourself"}
	}

	empty := true
	if entries, err := os.ReadDir(dir); err == nil {
		empty = len(entries) == 0
	} else if !os.IsNotExist(err) {
		return SyncResult{Action: SyncSkipped, Note: "annotations/: " + err.Error()}
	}

	if empty {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if out, err := gitRun(cctx, wsRoot, "clone", "--quiet", repoURL, dir); err != nil {
			return SyncResult{Action: SyncSkipped,
				Note: "couldn't fetch annotations (offline?) — vendor grammar unavailable until it clones: " + firstLine(out, err)}
		}
		lastSync[dir] = time.Now()
		return SyncResult{Action: SyncCloned, Note: "annotations checkout created"}
	}

	// Existing, non-empty. Only manage it if it's actually a git checkout —
	// and its OWN checkout: rev-parse walks up, and a bare copied folder
	// inside a versioned workspace would otherwise resolve to the workspace
	// repo, which is emphatically not ours to pull.
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	top, err := gitRun(pctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, dir) {
		return SyncResult{Action: SyncSkipped, Note: "annotations/ isn't a git checkout — using it as-is"}
	}
	before, _ := gitRun(pctx, dir, "rev-parse", "--short", "HEAD")
	if out, err := gitRun(pctx, dir, "pull", "--ff-only", "--quiet"); err != nil {
		return SyncResult{Action: SyncSkipped,
			Note: "couldn't update annotations (offline, or local changes) — using what you have: " + firstLine(out, err)}
	}
	lastSync[dir] = time.Now()
	after, _ := gitRun(pctx, dir, "rev-parse", "--short", "HEAD")
	if before != after {
		return SyncResult{Action: SyncUpdated, Note: fmt.Sprintf("annotations updated %s → %s", before, after)}
	}
	return SyncResult{Action: SyncCurrent}
}

// samePath: git prints toplevel with forward slashes and the two spellings
// may differ by symlink or case (Windows), so compare the actual files.
func samePath(a, b string) bool {
	sa, err1 := os.Stat(filepath.FromSlash(a))
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never let git block a scan on an interactive credential prompt; the
	// repo is public, so anonymous https always works.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func firstLine(out string, err error) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	if out == "" {
		return err.Error()
	}
	return out
}
