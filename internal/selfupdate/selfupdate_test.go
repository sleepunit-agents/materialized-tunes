package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/version"
)

// fakeRelease serves the three GitHub endpoints the updater touches.
type fakeRelease struct {
	sha      string
	exeName  string
	exeBytes []byte
	sumsLine string // defaults to the correct line; tests override to break it
}

func (f *fakeRelease) server(t *testing.T) *httptest.Server {
	t.Helper()
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/latest"):
			fmt.Fprintf(w, `{"sha":%q,"commit":{"message":"fix: the thing\n\nbody","committer":{"date":"2026-08-30T23:00:00Z"}}}`, f.sha)
		case strings.HasSuffix(r.URL.Path, "/releases/tags/latest"):
			fmt.Fprintf(w, `{"assets":[{"name":%q,"browser_download_url":%q},{"name":"SHA256SUMS.txt","browser_download_url":%q}]}`,
				f.exeName, ts.URL+"/dl/exe", ts.URL+"/dl/sums")
		case r.URL.Path == "/dl/exe":
			w.Write(f.exeBytes)
		case r.URL.Path == "/dl/sums":
			line := f.sumsLine
			if line == "" {
				sum := sha256.Sum256(f.exeBytes)
				line = hex.EncodeToString(sum[:]) + "  " + f.exeName
			}
			fmt.Fprintln(w, line)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func withCommit(t *testing.T, sha string) {
	t.Helper()
	old := version.Commit
	version.Commit = sha
	t.Cleanup(func() { version.Commit = old })
}

func TestCheck(t *testing.T) {
	f := &fakeRelease{sha: "aaaa1111aaaa1111", exeName: "mtunes-desktop.exe"}
	ts := f.server(t)

	withCommit(t, "")
	if st := check(context.Background(), ts.URL); st.Available || st.Note == "" {
		t.Fatalf("source build should be unavailable with a note, got %+v", st)
	}

	withCommit(t, "aaaa1111aaaa1111")
	if st := check(context.Background(), ts.URL); st.Available {
		t.Fatalf("same sha should be current, got %+v", st)
	}

	withCommit(t, "bbbb2222bbbb2222")
	st := check(context.Background(), ts.URL)
	if !st.Available || st.Remote == nil || st.Remote.SHA != "aaaa111" || st.Remote.Subject != "fix: the thing" {
		t.Fatalf("newer sha should be available with remote info, got %+v", st)
	}
}

func TestApplySwapsTheExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mtunes-desktop.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeRelease{sha: "cccc3333", exeName: "mtunes-desktop.exe", exeBytes: []byte("new build")}
	ts := f.server(t)
	withCommit(t, "oldsha")

	note, err := apply(context.Background(), ts.URL, exe)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "cccc333") {
		t.Fatalf("note should name the installed sha, got %q", note)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new build" {
		t.Fatalf("exe not replaced: %q", got)
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatalf(".old should be cleaned up on platforms that allow it")
	}
	if _, err := os.Stat(filepath.Join(dir, ".mtunes-desktop.exe.update")); !os.IsNotExist(err) {
		t.Fatalf("download temp file left behind")
	}
}

func TestApplyRefusesChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "mtunes-desktop.exe")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeRelease{sha: "dddd4444", exeName: "mtunes-desktop.exe", exeBytes: []byte("new build"),
		sumsLine: strings.Repeat("0", 64) + "  mtunes-desktop.exe"}
	ts := f.server(t)
	withCommit(t, "oldsha")

	if _, err := apply(context.Background(), ts.URL, exe); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want checksum error, got %v", err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old build" {
		t.Fatalf("exe must be untouched after a failed verify: %q", got)
	}
}

func TestApplyRefusesUnknownAsset(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "my-renamed-build.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeRelease{sha: "eeee5555", exeName: "mtunes-desktop.exe", exeBytes: []byte("new")}
	ts := f.server(t)
	withCommit(t, "oldsha")

	if _, err := apply(context.Background(), ts.URL, exe); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("want no-asset error, got %v", err)
	}
}
