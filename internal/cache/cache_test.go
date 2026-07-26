package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/jbarket/materialized-tunes/internal/location"
)

// flakyLoc serves content from a script of payloads: attempt N returns
// payloads[N] (clamped to the last). Corrupt-then-clean scripts model
// in-flight transfer corruption.
type flakyLoc struct {
	payloads [][]byte
	opens    int
}

func (f *flakyLoc) Name() string                                { return "flaky" }
func (f *flakyLoc) List(context.Context) ([]location.File, error) { return nil, nil }
func (f *flakyLoc) HashAll(context.Context, []string, func()) (map[string]string, error) {
	return nil, nil
}
func (f *flakyLoc) ReadPrefix(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (f *flakyLoc) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	i := f.opens
	if i >= len(f.payloads) {
		i = len(f.payloads) - 1
	}
	f.opens++
	return io.NopCloser(bytes.NewReader(f.payloads[i])), nil
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func init() { pullBackoff = 0 }

func TestEnsureRetriesTransientCorruption(t *testing.T) {
	good := []byte("the real sample bytes")
	loc := &flakyLoc{payloads: [][]byte{[]byte("corrupted in flight"), good}}

	path, err := Ensure(context.Background(), loc, "a.wav", shaOf(good), t.TempDir())
	if err != nil {
		t.Fatalf("second attempt should have succeeded: %v", err)
	}
	if loc.opens != 2 {
		t.Errorf("opens = %d, want 2 (one corrupt, one clean)", loc.opens)
	}
	got, _ := hashFile(path)
	if got != shaOf(good) {
		t.Errorf("cached object hash = %s, want the clean content", got)
	}
}

func TestEnsurePersistentMismatchFailsAfterRetries(t *testing.T) {
	loc := &flakyLoc{payloads: [][]byte{[]byte("wrong bytes, every time")}}

	_, err := Ensure(context.Background(), loc, "a.wav", shaOf([]byte("expected")), t.TempDir())
	if err == nil {
		t.Fatal("persistent mismatch must fail")
	}
	if loc.opens != pullAttempts {
		t.Errorf("opens = %d, want %d", loc.opens, pullAttempts)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error should say how hard it tried: %v", err)
	}
}
