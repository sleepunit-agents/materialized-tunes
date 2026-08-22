// Package transcode builds and runs the ffmpeg invocations that render
// sources into device format. Args are deterministic and recorded in
// lockfiles verbatim, so a restore replays exactly what a materialization
// did. -bitexact keeps output headers minimal and reproducible.
package transcode

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/jbarket/materialized-tunes/internal/proc"
	"strings"
)

// BuildArgs returns the ffmpeg arguments between input and output for one
// entry. These are what the lockfile records.
func BuildArgs(inChannels int, deviceChannels, downmix string, inRate, outRate, bitDepth int) []string {
	var filters []string
	if deviceChannels == "mono" && inChannels == 2 {
		var pan string
		switch downmix {
		case "sum-3db":
			pan = "pan=mono|c0=0.7071*c0+0.7071*c1"
		case "sum":
			pan = "pan=mono|c0=c0+c1"
		case "left":
			pan = "pan=mono|c0=c0"
		case "right":
			pan = "pan=mono|c0=c1"
		}
		filters = append(filters, pan)
	}
	if inRate != outRate {
		filters = append(filters, "aresample=resampler=soxr:precision=28")
	}

	var args []string
	if len(filters) > 0 {
		args = append(args, "-af", strings.Join(filters, ","))
	}
	codec := "pcm_s16le"
	if bitDepth == 24 {
		codec = "pcm_s24le"
	}
	args = append(args,
		"-ar", fmt.Sprint(outRate),
		"-c:a", codec,
		"-map_metadata", "-1",
		"-bitexact",
	)
	return args
}

// Run executes one transcode: ffmpeg -i in <args> out.
func Run(ctx context.Context, inPath string, args []string, outPath string) error {
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-i", inPath}, args...)
	full = append(full, outPath)
	cmd := proc.Quiet(exec.CommandContext(ctx, "ffmpeg", full...))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", inPath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Version reports the ffmpeg version string for lockfile tooling records.
func Version(ctx context.Context) string {
	out, err := proc.Quiet(exec.CommandContext(ctx, "ffmpeg", "-version")).Output()
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimPrefix(line, "ffmpeg version ")
	if fields := strings.Fields(line); len(fields) > 0 {
		return fields[0]
	}
	return "unknown"
}
