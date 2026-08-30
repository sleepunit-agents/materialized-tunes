package annotations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// git runs git in dir with a throwaway identity, failing the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=test", "-c", "user.email=test@test",
		"-c", "commit.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// sourceRepo builds a stand-in for sample-vendor-annotations with one commit.
func sourceRepo(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	git(t, src, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "tags.toml"), []byte("[[tag]]\nid = \"drums\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", ".")
	git(t, src, "commit", "--quiet", "-m", "one")
	return src
}

func resetThrottle() {
	syncMu.Lock()
	lastSync = map[string]time.Time{}
	syncMu.Unlock()
}

func TestSyncClonesMissingCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	src := sourceRepo(t)
	ws := t.TempDir()

	r := syncFrom(context.Background(), ws, src)
	if r.Action != SyncCloned {
		t.Fatalf("want cloned, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(ws, "annotations", "tags.toml")); err != nil {
		t.Fatalf("clone left no tags.toml: %v", err)
	}
}

func TestSyncFastForwards(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	src := sourceRepo(t)
	ws := t.TempDir()
	if r := syncFrom(context.Background(), ws, src); r.Action != SyncCloned {
		t.Fatalf("setup clone: %s (%s)", r.Action, r.Note)
	}

	// Remote moves; a throttled re-sync must not see it, a fresh one must.
	if err := os.WriteFile(filepath.Join(src, "instruments.toml"), []byte("[[instrument]]\nid = \"kick\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", ".")
	git(t, src, "commit", "--quiet", "-m", "two")

	if r := syncFrom(context.Background(), ws, src); r.Action != SyncCurrent {
		t.Fatalf("throttle: want current, got %s (%s)", r.Action, r.Note)
	}
	resetThrottle()
	r := syncFrom(context.Background(), ws, src)
	if r.Action != SyncUpdated {
		t.Fatalf("want updated, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(ws, "annotations", "instruments.toml")); err != nil {
		t.Fatalf("pull left no instruments.toml: %v", err)
	}

	resetThrottle()
	if r := syncFrom(context.Background(), ws, src); r.Action != SyncCurrent {
		t.Fatalf("at head: want current, got %s (%s)", r.Action, r.Note)
	}
}

func TestSyncLeavesNonGitDirAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ws := t.TempDir()
	dir := filepath.Join(ws, "annotations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tags.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := syncFrom(context.Background(), ws, "unused")
	if r.Action != SyncSkipped {
		t.Fatalf("want skipped, got %s (%s)", r.Action, r.Note)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "tags.toml")); string(data) != "x" {
		t.Fatal("non-git checkout was touched")
	}
}

// A bare copied annotations/ inside a VERSIONED workspace must not resolve
// to the workspace's own repo (rev-parse walks up) and pull that instead.
func TestSyncIgnoresEnclosingWorkspaceRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	ws := t.TempDir()
	git(t, ws, "init", "--quiet", "-b", "main")
	dir := filepath.Join(ws, "annotations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tags.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := syncFrom(context.Background(), ws, "unused")
	if r.Action != SyncSkipped {
		t.Fatalf("want skipped, got %s (%s)", r.Action, r.Note)
	}
}

func TestSyncSkipsDivergedCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	src := sourceRepo(t)
	ws := t.TempDir()
	if r := syncFrom(context.Background(), ws, src); r.Action != SyncCloned {
		t.Fatalf("setup clone: %s (%s)", r.Action, r.Note)
	}
	dir := filepath.Join(ws, "annotations")

	// Local commit + different remote commit = no fast-forward.
	if err := os.WriteFile(filepath.Join(dir, "local.toml"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--quiet", "-m", "local")
	if err := os.WriteFile(filepath.Join(src, "remote.toml"), []byte("r"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, src, "add", ".")
	git(t, src, "commit", "--quiet", "-m", "remote")

	resetThrottle()
	r := syncFrom(context.Background(), ws, src)
	if r.Action != SyncSkipped {
		t.Fatalf("want skipped, got %s (%s)", r.Action, r.Note)
	}
	if _, err := os.Stat(filepath.Join(dir, "local.toml")); err != nil {
		t.Fatal("diverged checkout lost local work")
	}
}
