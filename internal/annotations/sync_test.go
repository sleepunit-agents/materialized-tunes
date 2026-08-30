package annotations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRemote stands in for the GitHub API: /commits/HEAD and /tarball/<sha>,
// serving whatever files it currently holds.
type fakeRemote struct {
	mu      sync.Mutex
	sha     string
	subject string
	files   map[string]string
	srv     *httptest.Server
}

func newFakeRemote(t *testing.T) *fakeRemote {
	t.Helper()
	f := &fakeRemote{
		sha:     strings.Repeat("a", 40),
		subject: "one",
		files:   map[string]string{"tags.toml": "[[tag]]\nid = \"drums\"\n"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/commits/HEAD", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"sha": f.sha,
			"commit": map[string]any{
				"message":   f.subject + "\n\nbody",
				"committer": map[string]any{"date": "2026-08-30T04:05:06Z"},
			},
		})
	})
	mux.HandleFunc("/tarball/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/"+f.sha) {
			http.NotFound(w, r)
			return
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		prefix := "owner-repo-" + f.sha[:7] + "/"
		tw.WriteHeader(&tar.Header{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755})
		for name, body := range f.files {
			tw.WriteHeader(&tar.Header{Name: prefix + name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
			tw.Write([]byte(body))
		}
		tw.Close()
		gz.Close()
		w.Write(buf.Bytes())
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// advance moves the remote to a new commit adding the given file.
func (f *fakeRemote) advance(name, body, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = body
	f.subject = subject
	f.sha = fmt.Sprintf("%040d", len(f.files))
}

func resetThrottle() {
	syncMu.Lock()
	lastSync = map[string]time.Time{}
	syncMu.Unlock()
}

func TestSyncDownloadsMissingSnapshot(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()

	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncCloned {
		t.Fatalf("want cloned, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(ws, "annotations", "tags.toml")); err != nil {
		t.Fatalf("download left no tags.toml: %v", err)
	}
	if h := readHead(filepath.Join(ws, "annotations")); h == nil || h.SHA != remote.sha {
		t.Fatalf("head file off: %+v", h)
	}
}

func TestSyncPicksUpRemoteMoves(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCloned {
		t.Fatalf("setup: %s (%s)", r.Action, r.Note)
	}

	// Remote moves; a throttled re-sync must not see it, a fresh one must.
	remote.advance("instruments.toml", "[[instrument]]\nid = \"kick\"\n", "two")

	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCurrent {
		t.Fatalf("throttle: want current, got %s (%s)", r.Action, r.Note)
	}
	resetThrottle()
	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncUpdated {
		t.Fatalf("want updated, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(ws, "annotations", "instruments.toml")); err != nil {
		t.Fatalf("update left no instruments.toml: %v", err)
	}

	resetThrottle()
	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCurrent {
		t.Fatalf("at head: want current, got %s (%s)", r.Action, r.Note)
	}
}

// The manual "update now" path must reach the remote even when a scan just
// synced — force is the whole point of the button.
func TestSyncNowBypassesThrottle(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCloned {
		t.Fatalf("setup: %s (%s)", r.Action, r.Note)
	}
	remote.advance("instruments.toml", "[[instrument]]\nid = \"kick\"\n", "two")

	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCurrent {
		t.Fatalf("throttle: want current, got %s (%s)", r.Action, r.Note)
	}
	if r := syncFrom(context.Background(), ws, remote.srv.URL, true); r.Action != SyncUpdated {
		t.Fatalf("forced: want updated, got %s (%s)", r.Action, r.Note)
	}
}

func TestCheckoutHead(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	if h := CheckoutHead(context.Background(), ws); h != nil {
		t.Fatalf("no snapshot yet, want nil head, got %+v", h)
	}
	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCloned {
		t.Fatalf("setup: %s (%s)", r.Action, r.Note)
	}
	h := CheckoutHead(context.Background(), ws)
	if h == nil {
		t.Fatal("want head after download, got nil")
	}
	if h.SHA != remote.sha[:7] || h.Date != "2026-08-30" || h.Subject != "one" {
		t.Fatalf("head fields off: %+v", h)
	}
}

// A hand-made annotations/ folder (no head file, no git clone of ours) is
// not ours to replace.
func TestSyncLeavesForeignDirAlone(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	dir := filepath.Join(ws, "annotations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tags.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncSkipped {
		t.Fatalf("want skipped, got %s (%s)", r.Action, r.Note)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "tags.toml")); string(data) != "x" {
		t.Fatal("foreign dir was touched")
	}
}

// The git clone an older mtunes made itself gets adopted: replaced by a
// managed snapshot, .git and all.
func TestSyncMigratesLegacyClone(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	dir := filepath.Join(ws, "annotations")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://github.com/sleepunit-agents/sample-vendor-annotations.git\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tags.toml"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncUpdated || !strings.Contains(r.Note, "migrated") {
		t.Fatalf("want migration update, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git survived the migration")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "tags.toml")); !strings.Contains(string(data), "drums") {
		t.Fatalf("migrated snapshot has stale content: %q", data)
	}
	if h := CheckoutHead(context.Background(), ws); h == nil {
		t.Fatal("migrated snapshot has no head")
	}
}

// A legacy clone with local work in it stays untouched.
func TestSyncSkipsDirtyLegacyClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	remote := newFakeRemote(t)
	ws := t.TempDir()
	dir := filepath.Join(ws, "annotations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--quiet", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/sleepunit-agents/sample-vendor-annotations.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "local.toml"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncSkipped || !strings.Contains(r.Note, "local changes") {
		t.Fatalf("want skipped for local changes, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(dir, "local.toml")); err != nil {
		t.Fatal("dirty clone lost local work")
	}
}

// Offline (or the API down) never breaks an existing snapshot.
func TestSyncOfflineKeepsExisting(t *testing.T) {
	remote := newFakeRemote(t)
	ws := t.TempDir()
	if r := syncFrom(context.Background(), ws, remote.srv.URL, false); r.Action != SyncCloned {
		t.Fatalf("setup: %s (%s)", r.Action, r.Note)
	}
	remote.srv.Close()
	resetThrottle()

	r := syncFrom(context.Background(), ws, remote.srv.URL, false)
	if r.Action != SyncSkipped || !strings.Contains(r.Note, "using what you have") {
		t.Fatalf("want offline skip, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(ws, "annotations", "tags.toml")); err != nil {
		t.Fatal("offline sync damaged the snapshot")
	}
}
