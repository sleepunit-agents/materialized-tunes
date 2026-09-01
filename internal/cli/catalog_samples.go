package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
)

var (
	smpInstrument string
	smpFamily     string
	smpCategory   string
	smpKey        string
	smpBPM        string
	smpPack       string
	smpLocation   string
	smpDevice     string
	smpLimit      int
	smpJSON       bool
	smpCount      bool
)

// Sample is one catalog file with its harvested facts — the cross-pack
// browsing unit.
type Sample struct {
	Location   string  `json:"location"`
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Pack       string  `json:"pack"`
	Instrument string  `json:"instrument,omitempty"`
	Family     string  `json:"family,omitempty"`
	Category   string  `json:"category,omitempty"`
	Key        string  `json:"key,omitempty"`
	BPM        int     `json:"bpm,omitempty"`
	Duration   float64 `json:"duration_s,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	Bytes      int64   `json:"bytes"`
}

var catalogSamplesCmd = &cobra.Command{
	Use:   "samples",
	Short: "Query individual samples across every pack by instrument, key, bpm, category",
	Long: `The cross-pack view: packs are the mental unit for browsing, but a vocal
pack still has a top loop in it. This queries the per-file facts harvested
from vendor naming and annotations (see 'catalog harvest'), across all
locations at once.

  mtunes catalog samples --instrument kick --key Am
  mtunes catalog samples --family drums --bpm 120-130 --category loops
  mtunes catalog samples --instrument vocal --device syntakt   # only what fits

Facts come from what the vendor labelled, never from guessing at audio;
samples the vendor left unlabelled simply do not match.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		var lo, hi int
		if smpBPM != "" {
			lo, hi, err = parseBPMRange(smpBPM)
			if err != nil {
				return err
			}
		}
		var dev *profile.Device
		if smpDevice != "" {
			if dev, err = profile.LoadDevice(ws.Root, smpDevice); err != nil {
				return err
			}
		}

		var out []Sample
		for _, lc := range ws.Config.Locations {
			if smpLocation != "" && lc.Name != smpLocation {
				continue
			}
			entries, err := catalog.Load(ws.CatalogPath(lc.Name))
			if err != nil {
				continue
			}
			meta := harvest.LoadMeta(ws, lc.Name)
			paths := make([]string, 0, len(entries))
			for p := range entries {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				ce := entries[p]
				if ce.Audio == nil {
					continue
				}
				m := meta[p]
				if !matches(m, smpInstrument, smpFamily, smpCategory, smpKey, lo, hi) {
					continue
				}
				pack := packOf(p, lc.Layout == "vendor-dirs")
				if smpPack != "" && !strings.Contains(strings.ToLower(pack), strings.ToLower(smpPack)) {
					continue
				}
				if dev != nil && plan.Eligibility(dev, ce) != "" {
					continue
				}
				out = append(out, Sample{
					Location: lc.Name, Path: p, Name: filepath.Base(p), Pack: pack,
					Instrument: m.Instrument, Family: m.Family, Category: m.Category,
					Key: m.Key, BPM: m.BPM, Duration: ce.Audio.DurationS,
					Channels: ce.Audio.Channels, Bytes: ce.Size,
				})
			}
		}

		if smpCount {
			fmt.Printf("%d samples\n", len(out))
			return nil
		}
		total := len(out)
		if smpLimit > 0 && len(out) > smpLimit {
			out = out[:smpLimit]
		}
		if smpJSON {
			return emitJSON(map[string]any{"total": total, "shown": len(out), "samples": out})
		}
		if total == 0 {
			fmt.Println("no samples match")
			return nil
		}
		for _, s := range out {
			bits := []string{}
			if s.Instrument != "" {
				bits = append(bits, s.Instrument)
			}
			if s.Key != "" {
				bits = append(bits, s.Key)
			}
			if s.BPM > 0 {
				bits = append(bits, fmt.Sprintf("%d bpm", s.BPM))
			}
			if s.Category != "" {
				bits = append(bits, s.Category)
			}
			fmt.Printf("%-44s %-28s %s\n", trunc(s.Name, 44), trunc(s.Pack, 28), strings.Join(bits, " · "))
		}
		if total > len(out) {
			fmt.Printf("\n… %d more (--limit 0 for all, --json for everything)\n", total-len(out))
		} else {
			fmt.Printf("\n%d samples\n", total)
		}
		return nil
	},
}

func matches(m harvest.Meta, instrument, family, category, key string, lo, hi int) bool {
	if instrument != "" && !strings.EqualFold(m.Instrument, instrument) {
		return false
	}
	if family != "" && !strings.EqualFold(m.Family, family) {
		return false
	}
	if category != "" && !strings.EqualFold(m.Category, category) {
		return false
	}
	if key != "" && !strings.EqualFold(m.Key, key) {
		return false
	}
	if lo > 0 || hi > 0 {
		if m.BPM == 0 || m.BPM < lo || m.BPM > hi {
			return false
		}
	}
	return true
}

// packOf returns the pack a catalog path belongs to, honoring the
// location's layout (vendor-dirs packs are two segments deep).
func packOf(p string, vendorDirs bool) string {
	segs := strings.Split(p, "/")
	if vendorDirs && len(segs) > 2 {
		return segs[0] + "/" + segs[1]
	}
	if len(segs) > 1 {
		return segs[0]
	}
	return ""
}

// parseBPMRange accepts "120", "120-130" or "120..130".
func parseBPMRange(s string) (lo, hi int, err error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "..", "-")
	a, b, split := strings.Cut(s, "-")
	lo, err = strconv.Atoi(strings.TrimSpace(a))
	if err != nil {
		return 0, 0, fmt.Errorf("bad --bpm %q: want 120 or 120-130", s)
	}
	if !split {
		return lo, lo, nil
	}
	hi, err = strconv.Atoi(strings.TrimSpace(b))
	if err != nil {
		return 0, 0, fmt.Errorf("bad --bpm %q: want 120 or 120-130", s)
	}
	return lo, hi, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func init() {
	f := catalogSamplesCmd.Flags()
	f.StringVar(&smpInstrument, "instrument", "", "canonical instrument id (kick, vocal, pad, …)")
	f.StringVar(&smpFamily, "family", "", "instrument family (drums, bass, keys, synth, vocal, fx, …)")
	f.StringVar(&smpCategory, "category", "", "one-shots | loops | multisamples | fx")
	f.StringVar(&smpKey, "key", "", "musical key as harvested (Am, C#1, F)")
	f.StringVar(&smpBPM, "bpm", "", "tempo or range: 128 or 120-130")
	f.StringVar(&smpPack, "pack", "", "substring of the pack dir")
	f.StringVar(&smpLocation, "location", "", "limit to one location")
	f.StringVar(&smpDevice, "device", "", "only samples that can materialize for this device")
	f.IntVar(&smpLimit, "limit", 40, "rows to print (0 = all)")
	f.BoolVar(&smpJSON, "json", false, "machine-readable output")
	f.BoolVar(&smpCount, "count", false, "print only the match count")
	catalogCmd.AddCommand(catalogSamplesCmd)
}
