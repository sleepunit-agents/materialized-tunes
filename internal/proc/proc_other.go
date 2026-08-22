//go:build !windows

package proc

import "os/exec"

func quiet(*exec.Cmd) {}
