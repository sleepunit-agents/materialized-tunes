// Package location abstracts where source material lives. Sources are
// immutable; a location only ever needs four capabilities — list, stat
// (folded into list), hash, and read — so there is no mounted-filesystem
// requirement and no FUSE. Remote locations run listing and hashing on the
// remote machine, so cataloging a multi-terabyte library ships back a
// manifest, not the library.
package location

import (
	"context"
	"fmt"
	"io"

	"github.com/jbarket/materialized-tunes/internal/workspace"
)

// File is one source file as seen by List.
type File struct {
	Path  string // relative to the location root, forward slashes
	Size  int64
	MTime int64 // unix seconds
}

type Location interface {
	Name() string

	// List enumerates all files under the root. Hidden files and
	// directories (dot-prefixed) are skipped everywhere: vendor libraries
	// ship .DS_Store droppings, never samples.
	List(ctx context.Context) ([]File, error)

	// HashAll computes SHA-256 for the given relative paths. progress, if
	// non-nil, is called after each completed file.
	HashAll(ctx context.Context, paths []string, progress func()) (map[string]string, error)

	// ReadPrefix returns up to n leading bytes of a file — enough for
	// audio header parsing without transferring sample data.
	ReadPrefix(ctx context.Context, path string, n int64) ([]byte, error)

	// Open streams the full file (used by materialization to fill the
	// local cache).
	Open(ctx context.Context, path string) (io.ReadCloser, error)
}

func New(cfg workspace.LocationConfig) (Location, error) {
	switch cfg.Type {
	case "local":
		root, err := workspace.ExpandUser(cfg.Root)
		if err != nil {
			return nil, err
		}
		return &Local{name: cfg.Name, root: root}, nil
	case "ssh":
		if cfg.Host == "" {
			return nil, fmt.Errorf("location %q: ssh locations need a host", cfg.Name)
		}
		return &SSH{name: cfg.Name, host: cfg.Host, root: cfg.Root}, nil
	}
	return nil, fmt.Errorf("location %q: unknown type %q", cfg.Name, cfg.Type)
}
