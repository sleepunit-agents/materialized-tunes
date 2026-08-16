package ui

import (
	"os"
	"path/filepath"
	"syscall"
)

// listVolumes walks the places desktop Linux mounts removable media
// (/media/<user>, /run/media/<user>, /mnt) plus the root filesystem.
func listVolumes() []Volume {
	var out []Volume
	if v, ok := statfsVolume("/", "/"); ok {
		out = append(out, v)
	}
	roots := []string{"/mnt", "/media"}
	if u := os.Getenv("USER"); u != "" {
		roots = append(roots, filepath.Join("/media", u), filepath.Join("/run/media", u))
	}
	for _, root := range roots {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name())
			if v, ok := statfsVolume(e.Name(), p); ok {
				out = append(out, v)
			}
		}
	}
	return out
}

func statfsVolume(name, p string) (Volume, bool) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil {
		return Volume{}, false
	}
	capacity := int64(fs.Blocks) * int64(fs.Bsize)
	if capacity == 0 {
		return Volume{}, false
	}
	return Volume{name, p, capacity, int64(fs.Bavail) * int64(fs.Bsize)}, true
}
