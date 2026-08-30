// Package selfupdate keeps the running binary current against the rolling
// "latest" release, the same way the annotations snapshot stays current:
// plain HTTPS against the GitHub API, no git, no console windows. The check
// compares the commit baked into this build against what the latest tag
// points at; applying downloads the matching release asset, verifies it
// against the release's SHA256SUMS.txt, and swaps it into place with the
// Windows-safe rename dance (a running exe can't be overwritten, but it can
// be renamed aside).
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/version"
)

// RepoAPI is the app's own repo. Public since 2026-08-30, so anonymous
// release lookups just work.
const RepoAPI = "https://api.github.com/repos/sleepunit-agents/materialized-tunes"

// releaseTag is the rolling pre-release every push to main republishes.
// Tracking it (rather than versioned releases) is the point: a fix lands on
// main, the app offers it minutes later.
const releaseTag = "latest"

// Status answers "am I current" for the UI. Available means a different
// build than this one is published; Note carries the human line for every
// can't-know case (source build, offline, unmatched asset).
type Status struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"` // short, of this build
	Available bool   `json:"available"`
	Remote    *Build `json:"remote,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Build identifies what the latest tag currently points at.
type Build struct {
	SHA     string `json:"sha"` // shortened for display
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// Checks are cheap but the UI polls; don't hammer the API for a value that
// changes a few times an evening at most.
var (
	mu        sync.Mutex
	cached    *Status
	lastCheck time.Time
)

const checkMinInterval = 5 * time.Minute

// Check reports whether a newer build is published. Never an error — every
// failure mode is a Status with a Note, because "couldn't check" is a state
// the UI shows, not a state that breaks anything.
func Check(ctx context.Context) Status {
	mu.Lock()
	defer mu.Unlock()
	if cached != nil && time.Since(lastCheck) < checkMinInterval {
		return *cached
	}
	st := check(ctx, RepoAPI)
	// Only cache answers that reached the remote — a transient network blip
	// shouldn't stick for five minutes.
	if st.Remote != nil {
		cached, lastCheck = &st, time.Now()
	}
	return st
}

func check(ctx context.Context, api string) Status {
	st := Status{Version: version.Version, Commit: short(version.Commit)}
	if version.Commit == "" {
		st.Note = "source build — self-update is for released exes"
		return st
	}
	remote, err := fetchTagCommit(ctx, api)
	if err != nil {
		st.Note = "couldn't check for app updates (offline?)"
		return st
	}
	st.Remote = &Build{SHA: short(remote.sha), Date: remote.date, Subject: remote.subject}
	st.Available = remote.sha != version.Commit
	return st
}

// Apply downloads the current release build of this executable, verifies
// it, and swaps it into place. On success the new exe sits at this exe's
// path — it runs on next launch (or Restart). Returns a one-line human note.
func Apply(ctx context.Context) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return apply(ctx, RepoAPI, exe)
}

func apply(ctx context.Context, api, exe string) (string, error) {
	if version.Commit == "" {
		return "", fmt.Errorf("this is a source build — update it with your compiler, not the release feed")
	}
	exe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	name := filepath.Base(exe)

	rel, err := fetchRelease(ctx, api)
	if err != nil {
		return "", fmt.Errorf("couldn't reach the release feed (offline?): %w", err)
	}
	var exeURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			exeURL = a.URL
		case "SHA256SUMS.txt":
			sumsURL = a.URL
		}
	}
	if exeURL == "" {
		return "", fmt.Errorf("the release has no asset named %q — this binary isn't one the release feed ships", name)
	}

	// Download next to the target so the final rename never crosses volumes.
	tmp := filepath.Join(filepath.Dir(exe), "."+name+".update")
	sum, err := downloadTo(ctx, exeURL, tmp)
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("download failed: %w", err)
	}

	// Verify against the sums published alongside the exe. This also closes
	// the publish race: if the tag moved but the assets haven't been swapped
	// yet, exe and sums still come from the same publish step or the check
	// fails — we never install a half-updated release.
	if sumsURL != "" {
		want, err := fetchSum(ctx, sumsURL, name)
		if err != nil {
			os.Remove(tmp)
			return "", fmt.Errorf("couldn't verify the download: %w", err)
		}
		if want != sum {
			os.Remove(tmp)
			return "", fmt.Errorf("checksum mismatch — a new build may be mid-publish, try again in a minute")
		}
	}

	// The rename dance: a running exe can be renamed but not replaced.
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("couldn't move the running exe aside: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Rename(old, exe) // best-effort rollback
		os.Remove(tmp)
		return "", fmt.Errorf("couldn't move the new exe into place: %w", err)
	}
	os.Remove(old) // fails while we're still running it — CleanupOld gets it next launch

	mu.Lock()
	cached, lastCheck = nil, time.Time{} // next check reflects the new state
	mu.Unlock()
	return "new build installed @ " + short(rel.head), nil
}

// CleanupOld removes the renamed-aside previous exe a past update left
// behind. Best-effort: on Windows the old file stays locked until every
// process running it exits, so a failed remove just waits for next launch.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
	}
}

// Restart hands off to whatever now sits at this exe's path and exits this
// process. The caller must have flushed anything it owes the user first.
func Restart() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Start()
	os.Exit(0)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ---- GitHub over plain HTTPS -------------------------------------------

func get(ctx context.Context, url string, timeout time.Duration) (*http.Response, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mtunes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	// The cancel travels with the body: closing the response releases it.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

type tagCommit struct{ sha, date, subject string }

func fetchTagCommit(ctx context.Context, api string) (*tagCommit, error) {
	resp, err := get(ctx, api+"/commits/"+releaseTag, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
		return nil, fmt.Errorf("no sha in tag lookup")
	}
	subject := body.Commit.Message
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = subject[:i]
	}
	date := body.Commit.Committer.Date
	if len(date) > 10 {
		date = date[:10]
	}
	return &tagCommit{sha: body.SHA, date: date, subject: strings.TrimSpace(subject)}, nil
}

type release struct {
	head   string // sha the tag pointed at when we looked
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchRelease(ctx context.Context, api string) (*release, error) {
	head, err := fetchTagCommit(ctx, api)
	if err != nil {
		return nil, err
	}
	resp, err := get(ctx, api+"/releases/tags/"+releaseTag, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	rel.head = head.sha
	return &rel, nil
}

// downloadTo streams url into path (0755 — it's an executable) and returns
// the hex sha256 of what landed.
func downloadTo(ctx context.Context, url, path string) (string, error) {
	resp, err := get(ctx, url, 10*time.Minute)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchSum finds name's line in the release's SHA256SUMS.txt.
func fetchSum(ctx context.Context, url, name string) (string, error) {
	resp, err := get(ctx, url, 30*time.Second)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		// "sha  name" — sha256sum writes the name with a binary-mode marker
		// sometimes ("*name"); accept both.
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s not listed in SHA256SUMS.txt", name)
}
