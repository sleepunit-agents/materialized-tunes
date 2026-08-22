package transcode

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A batched ffmpeg must produce exactly the bytes a standalone Run does —
// the lockfile pins output hashes, and resume matches on planned size, so
// any drift between the two paths would silently break both.
func TestBatchMatchesSingleBytes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()

	// Synthesize inputs covering each transform class.
	type src struct{ name, gen string }
	srcs := []src{
		{"a.wav", "sine=frequency=440:sample_rate=44100:duration=0.05"},
		{"b.wav", "sine=frequency=880:sample_rate=48000:duration=0.05"},
		{"c.wav", "anoisesrc=sample_rate=96000:duration=0.05:seed=1"},
	}
	for _, s := range srcs {
		out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", s.gen, "-ac", "2", "-c:a", "pcm_s16le", filepath.Join(dir, s.name)).CombinedOutput()
		if err != nil {
			t.Fatalf("gen %s: %v: %s", s.name, err, out)
		}
	}
	cases := []struct {
		in   string
		args []string
	}{
		{"a.wav", BuildArgs(2, "mono", "sum-3db", 44100, 48000, 24)}, // fold + resample + depth
		{"b.wav", BuildArgs(2, "stereo", "", 48000, 48000, 24)},      // depth only
		{"c.wav", BuildArgs(2, "mono", "left", 96000, 44100, 16)},    // fold + downsample
	}

	var items []Item
	single := map[string]string{}
	for i, c := range cases {
		in := filepath.Join(dir, c.in)
		one := filepath.Join(dir, fmt.Sprintf("single-%d.wav", i))
		if err := Run(ctx, in, c.args, one); err != nil {
			t.Fatal(err)
		}
		single[c.in] = hash(t, one)
		items = append(items, Item{In: in, Args: c.args, Out: filepath.Join(dir, fmt.Sprintf("batch-%d.wav", i))})
	}
	if err := RunBatch(ctx, items); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		if got := hash(t, items[i].Out); got != single[c.in] {
			t.Errorf("%s: batch output %s != single %s", c.in, got[:12], single[c.in][:12])
		}
	}
}

func hash(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
