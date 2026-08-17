package annotations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTagMapCanonical(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tags.toml"), []byte(`
drop = ["ableton*", "wav-samples"]
[aliases]
"80s-drum-samples" = ["80s", "drums"]
"hi-hats" = ["hat"]
`), 0o644)
	tm := LoadTagMap(dir)
	got := tm.Canonical([]string{"80s Drum Samples", "Hi Hats", "Ableton Live", "WAV Samples", "House", "house", "Drum & Bass"})
	want := []string{"80s", "drums", "hat", "house", "drum-bass"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
	// no tags.toml: mechanical only
	if got := LoadTagMap(t.TempDir()).Canonical([]string{"Hi Hats"}); len(got) != 1 || got[0] != "hi-hats" {
		t.Errorf("mechanical-only: %v", got)
	}
}
