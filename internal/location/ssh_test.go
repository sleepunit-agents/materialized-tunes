package location

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSSH puts a stand-in ssh binary on PATH that executes the remote
// command locally, so the SSH location's process plumbing (pipes, exit
// status, stderr, master priming) runs for real without a network. The
// script speaks just enough ssh: `-O check` succeeds iff the master marker
// file exists, a `true` remote command is the master prime (it creates the
// marker and logs itself), anything else runs under sh.
func fakeSSH(t *testing.T, extra string) (primeLog, masterMarker string) {
	t.Helper()
	dir := t.TempDir()
	primeLog = filepath.Join(dir, "prime.log")
	masterMarker = filepath.Join(dir, "master.sock")
	script := `#!/bin/sh
mode=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift 2 ;;
    -O) mode="$2"; shift 2 ;;
    *) break ;;
  esac
done
host="$1"; shift
if [ "$mode" = "check" ]; then
  [ -e "$MTUNES_TEST_MASTER" ]
  exit $?
fi
if [ "$1" = "true" ]; then
  echo prime >> "$MTUNES_TEST_PRIMELOG"
  : > "$MTUNES_TEST_MASTER"
  exit 0
fi
` + extra + `
exec sh -c "$1"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MTUNES_TEST_PRIMELOG", primeLog)
	t.Setenv("MTUNES_TEST_MASTER", masterMarker)
	return primeLog, masterMarker
}

func writeTree(t *testing.T, root string, n int) map[string]string {
	t.Helper()
	// Names exercise the quoting path the real library needs: spaces,
	// dots, and single quotes, like "04. Pads/New Dawn/093 A'B.wav".
	sums := make(map[string]string, n)
	for i := range n {
		rel := fmt.Sprintf("Junos From Mars/WAV/04. Pads/New Dawn/%03d It's A Pad %d.wav", i, i)
		buf := make([]byte, 8192+i*17)
		if _, err := rand.Read(buf); err != nil {
			t.Fatal(err)
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, buf, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(buf)
		sums[rel] = hex.EncodeToString(sum[:])
	}
	return sums
}

// TestSSHConcurrentOpenStress pulls many files through Open from many
// goroutines at once — the materialize worker shape, scaled up — and
// verifies every stream arrives complete and un-interleaved, and that
// exactly one master prime happened no matter how many sessions raced
// to start first.
func TestSSHConcurrentOpenStress(t *testing.T) {
	primeLog, _ := fakeSSH(t, "")
	root := t.TempDir()
	sums := writeTree(t, root, 60)

	s := &SSH{name: "stress", host: "testhost", root: root}
	ctx := context.Background()

	paths := make(chan string, len(sums))
	for rel := range sums {
		paths <- rel
	}
	close(paths)

	var wg sync.WaitGroup
	errs := make(chan error, len(sums))
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range paths {
				rc, err := s.Open(ctx, rel)
				if err != nil {
					errs <- fmt.Errorf("%s: open: %w", rel, err)
					continue
				}
				h := sha256.New()
				_, copyErr := io.Copy(h, rc)
				closeErr := rc.Close()
				switch {
				case copyErr != nil:
					errs <- fmt.Errorf("%s: read: %w", rel, copyErr)
				case closeErr != nil:
					errs <- fmt.Errorf("%s: close: %w", rel, closeErr)
				case hex.EncodeToString(h.Sum(nil)) != sums[rel]:
					errs <- fmt.Errorf("%s: content mismatch — stream truncated or interleaved", rel)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	log, err := os.ReadFile(primeLog)
	if err != nil {
		t.Fatalf("no master was ever primed: %v", err)
	}
	if got := strings.Count(string(log), "prime"); got != 1 {
		t.Errorf("master primed %d times, want exactly 1 — concurrent sessions raced to establish it", got)
	}
}

// TestSSHMasterReprimedAfterExpiry simulates the ControlPersist master
// dying mid-run (a long cached/transcode-only stretch): once the liveness
// window lapses, the next session must re-establish it — once.
func TestSSHMasterReprimedAfterExpiry(t *testing.T) {
	primeLog, masterMarker := fakeSSH(t, "")
	root := t.TempDir()
	sums := writeTree(t, root, 1)

	s := &SSH{name: "expiry", host: "testhost", root: root}
	ctx := context.Background()
	var rel string
	for r := range sums {
		rel = r
	}

	pull := func() {
		t.Helper()
		rc, err := s.Open(ctx, rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			t.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			t.Fatal(err)
		}
	}

	pull()
	// Master dies; verdict still trusted — sessions fall back direct, no re-prime.
	if err := os.Remove(masterMarker); err != nil {
		t.Fatal(err)
	}
	pull()
	if log, _ := os.ReadFile(primeLog); strings.Count(string(log), "prime") != 1 {
		t.Fatalf("re-primed inside the trust window: %q", log)
	}
	// Window lapses; next session must notice and re-establish, alone.
	s.masterMu.Lock()
	s.checkedAt = time.Now().Add(-2 * masterRecheck)
	s.masterMu.Unlock()
	pull()
	if log, _ := os.ReadFile(primeLog); strings.Count(string(log), "prime") != 2 {
		t.Fatalf("master not re-primed after expiry: %q", log)
	}
}

// TestSSHOpenSurfacesTruncatedStream models the observed field failure: a
// session that emits part of the file and dies. The bytes end in a clean
// EOF, so Close's exit status is the only honest signal — it must carry
// the ssh failure and its stderr, not vanish.
func TestSSHOpenSurfacesTruncatedStream(t *testing.T) {
	// After the master-handling branches, emit 10 bytes and die like a
	// mux client whose master went away mid-stream.
	fakeSSH(t, `if [ -n "$MTUNES_TEST_TRUNCATE" ]; then
  sh -c "$1" | head -c 10
  echo "mux_client_request_session: session request failed" >&2
  exit 255
fi`)
	t.Setenv("MTUNES_TEST_TRUNCATE", "1")
	root := t.TempDir()
	sums := writeTree(t, root, 1)

	s := &SSH{name: "trunc", host: "testhost", root: root}
	var rel string
	for r := range sums {
		rel = r
	}

	rc, err := s.Open(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	data, copyErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if copyErr != nil {
		t.Fatalf("truncation shows up as clean EOF, not a read error: %v", copyErr)
	}
	if len(data) != 10 {
		t.Fatalf("read %d bytes, want the truncated 10", len(data))
	}
	if closeErr == nil {
		t.Fatal("Close must surface the dead session's exit status")
	}
	if !strings.Contains(closeErr.Error(), "session request failed") {
		t.Errorf("Close error should carry ssh's stderr: %v", closeErr)
	}
}
