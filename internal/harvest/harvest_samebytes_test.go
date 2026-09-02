package harvest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameBytes(t *testing.T) {
	d := t.TempDir()
	a, b, c := filepath.Join(d, "a"), filepath.Join(d, "b"), filepath.Join(d, "c")
	os.WriteFile(a, []byte("same content\n"), 0o644)
	os.WriteFile(b, []byte("same content\n"), 0o644)
	os.WriteFile(c, []byte("same contenT\n"), 0o644)
	if !sameBytes(a, b) {
		t.Error("identical files must compare equal")
	}
	if sameBytes(a, c) {
		t.Error("a one-byte difference must compare unequal")
	}
	if sameBytes(a, filepath.Join(d, "missing")) {
		t.Error("a missing file is never the same")
	}
}
