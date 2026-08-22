//go:build windows

package proc

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW: the child gets no console at all. Stdout/stderr still
// work through the pipes Go sets up, which is all we ever read.
const createNoWindow = 0x08000000

func quiet(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
