// Annotations sync: the workspace's annotations/ directory is a snapshot of
// a public data repo that moves on its own cadence — vendor grammar changes
// when someone observes a new pack, not when mtunes releases. So the tool
// keeps the snapshot fresh itself: download it when it's missing, freshen it
// before every scan. No git required — the repo is public, so this is plain
// HTTPS against the GitHub API (which also means no console windows flashing
// on Windows, where the GUI build has no console for child processes to
// inherit). Staleness is never an error — offline just means "using what you
// have", said in one line.
package annotations

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/proc"
)

// RepoAPI is where the annotations data lives. Public, code-free, its own
// repo precisely so it can move without a binary release.
const RepoAPI = "https://api.github.com/repos/sleepunit-agents/sample-vendor-annotations"

// headFile marks a snapshot as ours to manage and records what commit it is.
// Its absence on a non-empty directory means "hands off".
const headFile = ".mtunes-head.json"

type SyncAction string

const (
	SyncCloned  SyncAction = "cloned"  // fresh snapshot downloaded
	SyncUpdated SyncAction = "updated" // replaced with a newer snapshot
	SyncCurrent SyncAction = "current" // reached the remote, already at head
	SyncSkipped SyncAction = "skipped" // couldn't (or shouldn't) touch it — Note says why
)

type SyncResult struct {
	Action SyncAction
	Note   string // one line for humans; always set for skipped/updated
}

// Concurrent scans (the UI auto-scans several locations) must not race two
// snapshot swaps in one directory, and hourly cadences shouldn't hammer the
// API — serialize and throttle per directory.
var (
	syncMu   sync.Mutex
	lastSync = map[string]time.Time{}
)

const syncMinInterval = 10 * time.Minute

// Sync brings <wsRoot>/annotations up to date: downloads a snapshot if it's
// missing or empty, replaces it when the remote has moved. It never fails
// the caller's scan — every outcome is a SyncResult, and a directory that
// isn't ours to manage (a hand-made folder, a dirty dev checkout) is left
// alone.
func Sync(ctx context.Context, wsRoot string) SyncResult {
	return syncFrom(ctx, wsRoot, RepoAPI, false)
}

// SyncNow is Sync minus the per-process throttle — the user asked, so reach
// the remote. Still serialized against concurrent scans.
func SyncNow(ctx context.Context, wsRoot string) SyncResult {
	return syncFrom(ctx, wsRoot, RepoAPI, true)
}

func syncFrom(ctx context.Context, wsRoot, api string, force bool) SyncResult {
	syncMu.Lock()
	defer syncMu.Unlock()

	dir := filepath.Join(wsRoot, "annotations")
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	if t, ok := lastSync[dir]; !force && ok && time.Since(t) < syncMinInterval {
		return SyncResult{Action: SyncCurrent}
	}

	empty := true
	if entries, err := os.ReadDir(dir); err == nil {
		empty = len(entries) == 0
	} else if !os.IsNotExist(err) {
		return SyncResult{Action: SyncSkipped, Note: "annotations/: " + err.Error()}
	}

	local := readHead(dir)
	migrating := false
	if !empty && local == nil {
		// Non-empty and not our snapshot. The one thing we still adopt is
		// the git clone an older mtunes made itself — anything else (a
		// hand-made folder, someone's working copy) is not ours to replace.
		if !ownGitClone(dir) {
			return SyncResult{Action: SyncSkipped, Note: "annotations/ isn't managed by mtunes — using it as-is"}
		}
		if dirtyGitCheckout(ctx, dir) {
			return SyncResult{Action: SyncSkipped, Note: "annotations/ has local changes — leaving it alone"}
		}
		migrating = true
	}

	remote, err := fetchRemoteHead(ctx, api)
	if err != nil {
		if empty {
			return SyncResult{Action: SyncSkipped,
				Note: "couldn't fetch annotations (offline?) — vendor grammar unavailable until it downloads: " + err.Error()}
		}
		return SyncResult{Action: SyncSkipped,
			Note: "couldn't check for annotation updates (offline?) — using what you have: " + err.Error()}
	}

	if local != nil && local.SHA == remote.SHA {
		lastSync[dir] = time.Now()
		return SyncResult{Action: SyncCurrent}
	}

	if err := downloadSnapshot(ctx, api, remote, dir); err != nil {
		if empty {
			return SyncResult{Action: SyncSkipped,
				Note: "couldn't fetch annotations (offline?) — vendor grammar unavailable until it downloads: " + err.Error()}
		}
		return SyncResult{Action: SyncSkipped,
			Note: "couldn't download annotations update — using what you have: " + err.Error()}
	}
	lastSync[dir] = time.Now()
	switch {
	case empty:
		return SyncResult{Action: SyncCloned, Note: "annotations snapshot downloaded @ " + short(remote.SHA)}
	case migrating:
		return SyncResult{Action: SyncUpdated, Note: "annotations checkout migrated to a managed snapshot @ " + short(remote.SHA) + " (git no longer needed)"}
	default:
		return SyncResult{Action: SyncUpdated, Note: fmt.Sprintf("annotations updated %s → %s", short(local.SHA), short(remote.SHA))}
	}
}

// Head describes what the annotations snapshot is actually at — the thing a
// user needs to see to answer "did my re-scan pick up the fix". Nil when
// there's nothing managed to describe (not downloaded yet, foreign folder).
type Head struct {
	SHA     string `json:"sha"`     // full in the head file; shortened for display
	Date    string `json:"date"`    // commit date, YYYY-MM-DD
	Subject string `json:"subject"` // commit subject line
}

// CheckoutHead reads the managed snapshot's head. Never an error — a nil
// Head just means "nothing to show". (A legacy git checkout shows nil until
// the next sync migrates it.)
func CheckoutHead(_ context.Context, wsRoot string) *Head {
	h := readHead(filepath.Join(wsRoot, "annotations"))
	if h == nil {
		return nil
	}
	h.SHA = short(h.SHA)
	return h
}

func readHead(dir string) *Head {
	data, err := os.ReadFile(filepath.Join(dir, headFile))
	if err != nil {
		return nil
	}
	var h Head
	if json.Unmarshal(data, &h) != nil || h.SHA == "" {
		return nil
	}
	return &h
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ownGitClone reports whether dir is the git clone an older mtunes created
// itself — recognized by its remote URL, read straight from .git/config so
// no git binary is needed.
func ownGitClone(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	return err == nil && strings.Contains(string(data), "sample-vendor-annotations")
}

// dirtyGitCheckout: before replacing a legacy clone, make sure nobody has
// local work in it. Only answerable when git is installed; a machine without
// git can't have made local commits in it, and a checkout too broken for
// status to run is better off replaced.
func dirtyGitCheckout(ctx context.Context, dir string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := proc.Quiet(exec.CommandContext(cctx, "git", "status", "--porcelain"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// ---- GitHub over plain HTTPS -------------------------------------------

func fetchRemoteHead(ctx context.Context, api string) (*Head, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, api+"/commits/HEAD", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mtunes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HEAD lookup: %s", resp.Status)
	}
	var body struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message   string `json:"message"`
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if body.SHA == "" {
		return nil, fmt.Errorf("HEAD lookup: no sha in response")
	}
	subject := body.Commit.Message
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = subject[:i]
	}
	date := body.Commit.Committer.Date
	if len(date) > 10 {
		date = date[:10]
	}
	return &Head{SHA: body.SHA, Date: date, Subject: strings.TrimSpace(subject)}, nil
}

// downloadSnapshot fetches the tarball for head, extracts it next to dir,
// then swaps it into place — the old snapshot only disappears once the new
// one fully landed.
func downloadSnapshot(ctx context.Context, api string, head *Head, dir string) error {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, api+"/tarball/"+head.SHA, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mtunes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tarball download: %s", resp.Status)
	}

	tmp := dir + ".sync-tmp"
	os.RemoveAll(tmp)
	if err := extractTarball(resp.Body, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	hj, err := json.Marshal(head)
	if err == nil {
		err = os.WriteFile(filepath.Join(tmp, headFile), hj, 0o644)
	}
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}

	old := dir + ".old"
	os.RemoveAll(old)
	if _, statErr := os.Stat(dir); statErr == nil {
		if err := os.Rename(dir, old); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("couldn't move old snapshot aside: %w", err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		os.Rename(old, dir) // best-effort rollback
		os.RemoveAll(tmp)
		return fmt.Errorf("couldn't move new snapshot into place: %w", err)
	}
	os.RemoveAll(old)
	return nil
}

// extractTarball unpacks a GitHub source tarball into dst, stripping the
// repo-<sha>/ prefix GitHub wraps everything in. Only plain files and
// directories — the annotations repo holds nothing else.
func extractTarball(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		i := strings.IndexByte(name, '/')
		if i < 0 {
			continue // the wrapper dir itself, or pax headers
		}
		rel := name[i+1:]
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, "../") {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
