package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/location"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// testWorkspace builds a workspace with one local location "src" rooted in
// its own directory. Jobs against files that exist there use the local
// in-place path; jobs against missing files fail in cache.Ensure — which
// is exactly the per-file failure skip-on-fail exists for.
func testWorkspace(t *testing.T, files map[string]string) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = append(ws.Config.Locations,
		workspace.LocationConfig{Name: "src", Type: "local", Root: dir})
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws2, err := workspace.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws2
}

func TestRunOneResumeReusesPlannedSizeOutput(t *testing.T) {
	target := t.TempDir()
	content := "already rendered by a previous run"
	out := filepath.Join(target, "kits", "bd.wav")
	os.MkdirAll(filepath.Dir(out), 0o755)
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Source doesn't exist anywhere — if resume tried to render, this job
	// could only fail. Success proves the existing output was reused.
	j := job{loc: "src", srcPath: "missing.wav", srcSHA: "00", outRel: "kits/bd.wav",
		planned: int64(len(content))}
	d, err := runOne(context.Background(), nil, j, t.TempDir(), target)
	if err != nil {
		t.Fatalf("resume should have short-circuited before any source access: %v", err)
	}
	if d.outBytes != int64(len(content)) || d.outSHA == "" {
		t.Errorf("reused output must be sized and hashed for the lock: %+v", d)
	}
}

func TestRunJobsSkipsFailuresAndKeepsGoing(t *testing.T) {
	ws := testWorkspace(t, map[string]string{"a.raw": "aaa"})

	// One job's source is missing → hash-check fails in cache.Ensure →
	// skip. The other resumes from an existing planned-size output (no
	// ffmpeg in the loop). The run must finish with one of each.
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "ok.wav"), []byte("done"), 0o644)

	jobs := []job{
		{loc: "src", srcPath: "gone.raw", srcSHA: strings.Repeat("0", 64), outRel: "gone.wav", planned: 99},
		{loc: "src", srcPath: "a.raw", srcSHA: strings.Repeat("0", 64), outRel: "ok.wav", planned: 4},
	}
	results, skips, err := runJobs(context.Background(), ws, jobs, target, nil)
	if err != nil {
		t.Fatalf("one bad file must not kill the run: %v", err)
	}
	if len(results) != 1 || results[0].outRel != "ok.wav" {
		t.Errorf("results = %+v, want just ok.wav", results)
	}
	if len(skips) != 1 || skips[0].OutRel != "gone.wav" {
		t.Errorf("skips = %+v, want just gone.wav", skips)
	}
}

func TestRunJobsAbortsWhenFailureIsSystemic(t *testing.T) {
	ws := testWorkspace(t, nil)

	var jobs []job
	for i := range maxSkips + 10 {
		jobs = append(jobs, job{loc: "src", srcPath: fmt.Sprintf("gone-%d.raw", i),
			srcSHA: strings.Repeat("0", 64), outRel: fmt.Sprintf("out-%d.wav", i), planned: 9})
	}
	_, _, err := runJobs(context.Background(), ws, jobs, t.TempDir(), nil)
	if err == nil {
		t.Fatal("everything failing is systemic and must abort, not skip forever")
	}
	if !strings.Contains(err.Error(), "systemic") {
		t.Errorf("error should name the diagnosis: %v", err)
	}
}

func TestRunOneCopyPassthroughNeedsNoFFmpeg(t *testing.T) {
	content := "RIFF....WAVEfmt pretend-pcm-bytes"
	ws := testWorkspace(t, map[string]string{"kits/bd.wav": content})
	lc, _ := ws.Location("src")
	loc, err := location.New(lc)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()

	sum := sha256.Sum256([]byte(content))
	j := job{loc: "src", srcPath: "kits/bd.wav", srcSHA: hex.EncodeToString(sum[:]), outRel: "out/bd.wav", copy: true,
		planned: int64(len(content))}
	d, err := runOne(context.Background(), loc, j, t.TempDir(), target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "out", "bd.wav"))
	if err != nil || string(got) != content {
		t.Fatalf("copy must reproduce source bytes: %q %v", got, err)
	}
	if d.reused || d.outBytes != int64(len(content)) || d.outSHA == "" {
		t.Errorf("copied output must be sized and hashed for the lock: %+v", d)
	}
	if _, err := os.Stat(filepath.Join(target, "out", "bd.wav.mtunes-part")); err == nil {
		t.Error("temp file must not linger")
	}
}

func TestBatchJobsChunksTranscodesAndIsolatesCopies(t *testing.T) {
	t.Setenv("MTUNES_BATCH", "3")
	var jobs []job
	for i := range 7 {
		jobs = append(jobs, job{srcPath: fmt.Sprintf("s%d.wav", i), outRel: fmt.Sprintf("o%d.wav", i), args: []string{"-ar", "48000"}})
	}
	jobs = append(jobs, job{srcPath: "c.wav", outRel: "c.wav", copy: true})
	units := batchJobs(jobs)
	var sizes []int
	for _, u := range units {
		sizes = append(sizes, len(u))
	}
	if fmt.Sprint(sizes) != "[3 3 1 1]" {
		t.Errorf("unit sizes = %v, want [3 3 1 1] (chunks of 3, copy alone)", sizes)
	}
}

// A bad source inside a batch must fail only itself: the batch ffmpeg
// fails as a whole, the per-file retry attributes the error, and the
// good entries in the same batch still render.
func TestRunUnitBatchFailureFallsBackPerFile(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	gen := func(name string) {
		out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=0.02",
			"-ac", "2", "-c:a", "pcm_s16le", filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	gen("good1.wav")
	gen("good2.wav")
	os.WriteFile(filepath.Join(dir, "bad.wav"), []byte("RIFF this is not audio"), 0o644)

	ws := testWorkspace(t, nil)
	ws.Config.Locations[0].Root = dir
	locs := map[string]location.Location{}
	l, err := location.New(ws.Config.Locations[0])
	if err != nil {
		t.Fatal(err)
	}
	locs["src"] = l

	args := []string{"-ar", "48000", "-c:a", "pcm_s24le", "-map_metadata", "-1", "-bitexact"}
	var unit []job
	for _, n := range []string{"good1.wav", "bad.wav", "good2.wav"} {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		sum := sha256.Sum256(b)
		unit = append(unit, job{loc: "src", srcPath: n, srcSHA: hex.EncodeToString(sum[:]), outRel: "out/" + n, args: args})
	}
	target := t.TempDir()
	results, fails := runUnit(context.Background(), locs, unit, t.TempDir(), target)
	if len(fails) != 1 || fails[0].OutRel != "out/bad.wav" {
		t.Errorf("fails = %+v, want just out/bad.wav", fails)
	}
	if len(results) != 2 {
		t.Errorf("results = %+v, want the two good files", results)
	}
	for _, d := range results {
		if d.outBytes == 0 || d.outSHA == "" {
			t.Errorf("result not finished: %+v", d)
		}
	}
}
