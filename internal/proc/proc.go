// Package proc holds the per-platform tweaks for child processes mtunes
// spawns (ffmpeg, ssh). The GUI build on Windows is the reason it exists:
// a windowsgui executable has no console, so every exec'd console program
// gets a brand-new one — one flashing cmd window per transcode, thousands
// per materialize, and a conhost.exe spin-up each time that eats more
// wall-clock than the ffmpeg run for small samples.
package proc

import "os/exec"

// Quiet marks cmd so it runs without creating a console window. No-op
// outside Windows.
func Quiet(cmd *exec.Cmd) *exec.Cmd {
	quiet(cmd)
	return cmd
}
