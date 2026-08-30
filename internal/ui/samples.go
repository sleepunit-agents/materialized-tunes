package ui

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
)

// samples is the cross-pack view: packs stay the browsing unit, but a
// jungle pack still holds vocals and a piano, and this is how you find
// them. Filters run over the facts harvested from vendor labelling — a
// file the vendor never labelled simply doesn't match.
func (s *Server) samples(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	instrument := strings.ToLower(q.Get("instrument"))
	family := strings.ToLower(q.Get("family"))
	category := strings.ToLower(q.Get("category"))
	key := strings.ToLower(q.Get("key"))
	packQ := strings.ToLower(q.Get("pack"))
	text := strings.ToLower(q.Get("q"))
	locName := q.Get("location")

	var bpmLo, bpmHi int
	if b := q.Get("bpm"); b != "" {
		lo, hi, _ := strings.Cut(strings.ReplaceAll(b, "..", "-"), "-")
		bpmLo, _ = strconv.Atoi(strings.TrimSpace(lo))
		if hi == "" {
			bpmHi = bpmLo
		} else {
			bpmHi, _ = strconv.Atoi(strings.TrimSpace(hi))
		}
	}
	var dev *profile.Device
	if d := q.Get("device"); d != "" {
		var err error
		if dev, err = profile.LoadDevice(s.ws.Root, d); err != nil {
			jsonErr(w, 400, err)
			return
		}
	}
	limit := 300
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		limit = n
	}

	type row struct {
		Location   string  `json:"location"`
		Path       string  `json:"path"`
		Name       string  `json:"name"`
		Pack       string  `json:"pack"`
		Instrument string  `json:"instrument,omitempty"`
		Family     string  `json:"family,omitempty"`
		Category   string  `json:"category,omitempty"`
		Key        string  `json:"key,omitempty"`
		BPM        int     `json:"bpm,omitempty"`
		Duration   float64 `json:"duration,omitempty"`
		Channels   int     `json:"channels,omitempty"`
		Ineligible string  `json:"ineligible,omitempty"`
	}
	var out []row
	total := 0
	facets := map[string]int{}

	for _, lc := range s.ws.Config.Locations {
		if locName != "" && lc.Name != locName {
			continue
		}
		entries, err := catalog.Load(s.ws.CatalogPath(lc.Name))
		if err != nil {
			continue
		}
		meta := harvest.LoadMeta(s.ws, lc.Name)
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
			m := meta[ce.SHA256]
			if instrument != "" && !strings.EqualFold(m.Instrument, instrument) {
				continue
			}
			if family != "" && !strings.EqualFold(m.Family, family) {
				continue
			}
			if category != "" && !strings.EqualFold(m.Category, category) {
				continue
			}
			if key != "" && !strings.EqualFold(m.Key, key) {
				continue
			}
			if bpmLo > 0 && (m.BPM < bpmLo || m.BPM > bpmHi) {
				continue
			}
			segs := strings.Split(p, "/")
			pack := segs[0]
			if lc.Layout == "vendor-dirs" && len(segs) > 2 {
				pack = segs[0] + "/" + segs[1]
			}
			if packQ != "" && !strings.Contains(strings.ToLower(pack), packQ) {
				continue
			}
			name := segs[len(segs)-1]
			if text != "" && !strings.Contains(strings.ToLower(name), text) {
				continue
			}
			reason := ""
			if dev != nil {
				if reason = plan.Eligibility(dev, ce); reason != "" {
					continue // the lens hides what can't ride
				}
			}
			total++
			if m.Instrument != "" {
				facets[m.Instrument]++
			}
			if len(out) < limit {
				out = append(out, row{
					Location: lc.Name, Path: p, Name: name, Pack: pack,
					Instrument: m.Instrument, Family: m.Family, Category: m.Category,
					Key: m.Key, BPM: m.BPM, Duration: ce.Audio.DurationS,
					Channels: ce.Audio.Channels, Ineligible: reason,
				})
			}
		}
	}
	type facet struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	var fs []facet
	for id, n := range facets {
		fs = append(fs, facet{id, n})
	}
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Count != fs[j].Count {
			return fs[i].Count > fs[j].Count
		}
		return fs[i].ID < fs[j].ID
	})
	jsonOut(w, map[string]any{
		"total": total, "shown": len(out), "samples": out, "instruments": fs,
	})
}
