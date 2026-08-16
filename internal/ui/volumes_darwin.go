package ui

import (
	"os"
	"path/filepath"
	"syscall"
)

// listVolumes enumerates /Volumes — every mounted disk on macOS shows up
// there, including the boot volume by name.
func listVolumes() []Volume {
	var out []Volume
	entries, _ := os.ReadDir("/Volumes")
	for _, e := range entries {
		p := filepath.Join("/Volumes", e.Name())
		if v, ok := statfsVolume(e.Name(), p); ok {
			out = append(out, v)
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
