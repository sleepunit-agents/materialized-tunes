package transcode

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jbarket/materialized-tunes/internal/proc"
)

// Item is one input→output pair for a batched run.
type Item struct {
	In   string
	Args []string // per-entry args exactly as BuildArgs returns them (and the lock records)
	Out  string
}

// Batch limits. One ffmpeg process per spawn is the dominant cost for short
// samples (conhost + Defender rescan on Windows is tens of ms; the transcode
// of a drum hit is single-digit ms), so amortizing the spawn across many
// outputs is the whole win. Windows caps a command line at 32767 chars; we
// stay well under with headroom for quoting.
const (
	MaxBatchItems = 64
	MaxBatchChars = 24000
)

// ItemChars approximates the argv length one item contributes to a batch.
func ItemChars(it Item) int {
	n := len(it.In) + len(it.Out) + 24 // -i, -map N:a:0, spaces/quotes
	for _, a := range it.Args {
		n += len(a) + 3
	}
	return n
}

// RunBatch executes one ffmpeg with N inputs and N outputs:
//
//	ffmpeg -i in0 -i in1 … -map 0:a:0 <args0> out0 -map 1:a:0 <args1> out1 …
//
// Every option in args is per-output in ffmpeg (-af, -ar, -c:a,
// -map_metadata, -bitexact), so each output is rendered exactly as a
// standalone Run would render it; the lockfile keeps recording the
// per-entry args. A failure anywhere fails the whole process — callers
// retry per item to attribute the error.
func RunBatch(ctx context.Context, items []Item) error {
	if len(items) == 1 {
		return Run(ctx, items[0].In, items[0].Args, items[0].Out)
	}
	full := []string{"-hide_banner", "-loglevel", "error", "-y"}
	for _, it := range items {
		full = append(full, "-i", it.In)
	}
	for i, it := range items {
		full = append(full, "-map", fmt.Sprintf("%d:a:0", i))
		full = append(full, it.Args...)
		full = append(full, it.Out)
	}
	cmd := proc.Quiet(exec.CommandContext(ctx, "ffmpeg", full...))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg batch of %d: %w: %s", len(items), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
