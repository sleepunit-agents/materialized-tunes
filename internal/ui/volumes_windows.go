package ui

import (
	"golang.org/x/sys/windows"
)

// listVolumes enumerates drive letters. Name is the volume label when the
// drive has one ("CARGO"), else the bare letter; Path is the drive root
// ("E:\"), which is exactly what a card materialization wants as --to.
func listVolumes() []Volume {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []Volume
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rootp, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		switch windows.GetDriveType(rootp) {
		case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_REMOTE:
		default:
			continue // CD-ROM, RAM disk, unknown, no root dir
		}
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(rootp, &free, &total, &totalFree); err != nil || total == 0 {
			continue
		}
		name := root[:2]
		label := make([]uint16, windows.MAX_PATH+1)
		if err := windows.GetVolumeInformation(rootp, &label[0], uint32(len(label)), nil, nil, nil, nil, 0); err == nil {
			if l := windows.UTF16ToString(label); l != "" {
				name = l + " (" + root[:2] + ")"
			}
		}
		out = append(out, Volume{Name: name, Path: root, Capacity: int64(total), Free: int64(free)})
	}
	return out
}
