// Package cache is the content-addressed local store of source files,
// keyed by SHA-256. A remote file crosses the network at most once, ever;
// the cache is disposable and refillable by definition. Local sources are
// not copied into the cache — they are used in place, but always integrity-
// checked against the cataloged hash first, because locks can only pin
// bytes that were verified.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/location"
)

// Ensure returns a local path whose contents are verified to be wantSHA.
func Ensure(ctx context.Context, loc location.Location, relPath, wantSHA, cacheDir string) (string, error) {
	if lp, ok := loc.(interface{ LocalPath(string) string }); ok {
		path := lp.LocalPath(relPath)
		got, err := hashFile(path)
		if os.IsNotExist(err) {
			return "", fmt.Errorf("source %s:%s no longer exists — local sources are used in place, not cached, so this lock entry is unrestorable unless the file comes back", loc.Name(), relPath)
		}
		if err != nil {
			return "", err
		}
		if got != wantSHA {
			return "", fmt.Errorf("%s: content changed since scan (sha mismatch) — rescan the location", relPath)
		}
		return path, nil
	}

	objPath := filepath.Join(cacheDir, wantSHA[:2], wantSHA)
	if _, err := os.Stat(objPath); err == nil {
		return objPath, nil // verified on arrival; content-addressed names don't lie
	}
	if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
		return "", err
	}

	// A failed pull is retried: a hash mismatch here is far more often
	// in-flight corruption than a stale catalog (the same bytes hashing
	// clean on the server proves nothing changed), and open/copy errors
	// are transient network by nature. Only a repeatable failure surfaces.
	var lastErr error
	for attempt := 1; attempt <= pullAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt-1) * pullBackoff):
			}
		}
		if lastErr = pullOnce(ctx, loc, relPath, wantSHA, objPath); lastErr == nil {
			return objPath, nil
		}
	}
	return "", fmt.Errorf("%w (after %d attempts)", lastErr, pullAttempts)
}

const pullAttempts = 3

var pullBackoff = 2 * time.Second // var so tests can zero it

func pullOnce(ctx context.Context, loc location.Location, relPath, wantSHA, objPath string) error {
	src, err := loc.Open(ctx, relPath)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(objPath), ".pull-*")
	if err != nil {
		src.Close()
		return err
	}
	defer os.Remove(tmp.Name())

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), src)
	// Close before judging the bytes: for remote sources Close reaps the
	// transport and returns its exit status, which is the only way to tell
	// a stream that was cut short (clean EOF mid-file) from one that
	// completed. Without it, a failed session gets misreported below as a
	// catalog mismatch.
	closeErr := src.Close()
	if copyErr != nil {
		tmp.Close()
		return fmt.Errorf("pulling %s: %w", relPath, copyErr)
	}
	if closeErr != nil {
		tmp.Close()
		return fmt.Errorf("pulling %s: %w", relPath, closeErr)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("%s: pulled content does not match cataloged sha — transfer corruption, or rescan the location", relPath)
	}
	return os.Rename(tmp.Name(), objPath)
}

// Status reports object count and total bytes in the cache.
func Status(cacheDir string) (files int, bytes int64, err error) {
	err = filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile is exported for output verification after transcodes.
func HashFile(path string) (string, error) { return hashFile(path) }
