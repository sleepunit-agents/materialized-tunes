package location

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type Local struct {
	name string
	root string
}

func (l *Local) Name() string { return l.name }

func (l *Local) List(ctx context.Context) ([]File, error) {
	var files []File
	err := filepath.WalkDir(l.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(d.Name(), ".") && path != l.root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(l.root, path)
		if err != nil {
			return err
		}
		files = append(files, File{
			Path:  filepath.ToSlash(rel),
			Size:  info.Size(),
			MTime: info.ModTime().Unix(),
		})
		return nil
	})
	return files, err
}

func (l *Local) HashAll(ctx context.Context, paths []string, progress func()) (map[string]string, error) {
	type result struct {
		path string
		sum  string
		err  error
	}
	jobs := make(chan string)
	results := make(chan result)

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8 // hashing is disk-bound long before it is CPU-bound
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				sum, err := l.hashOne(p)
				select {
				case results <- result{p, sum, err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range paths {
			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	sums := make(map[string]string, len(paths))
	for r := range results {
		if r.err != nil {
			return nil, r.err
		}
		sums[r.path] = r.sum
		if progress != nil {
			progress()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

func (l *Local) hashOne(rel string) (string, error) {
	f, err := os.Open(filepath.Join(l.root, filepath.FromSlash(rel)))
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

func (l *Local) ReadPrefix(ctx context.Context, rel string, n int64) ([]byte, error) {
	f, err := os.Open(filepath.Join(l.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil // short files are fine; parsers handle truncation
	}
	return buf[:read], err
}

func (l *Local) Open(ctx context.Context, rel string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(l.root, filepath.FromSlash(rel)))
}

// LocalPath exposes the on-disk path so callers (the cache) can use local
// sources in place instead of copying them into the object store.
func (l *Local) LocalPath(rel string) string {
	return filepath.Join(l.root, filepath.FromSlash(rel))
}
