package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Rate limiting has to be honest about two very different jobs. Somebody
// who bought one pack this week has four dirs to resolve: that should
// finish instantly, not crawl. Somebody pointing us at a 220-pack library
// is asking a vendor's free API for hundreds of answers, which is a
// favour, not a right — that job takes as long as it takes, spread over
// runs, and the answers are cached forever so it only happens once.
//
// So: a burst allowance for small jobs, a pace after it, a per-run cap so
// a huge library resolves incrementally, and a cooldown that SURVIVES the
// process — otherwise every scan cheerfully re-hammers an endpoint that
// just said no. (Learned the hard way: Splice 429s the assetsSearch
// operation for hours after a few hundred rapid calls.)
type policy struct {
	Burst     int           // requests at full speed before pacing starts
	Pace      time.Duration // floor between requests after the burst
	MaxPerRun int           // stop after this many; the rest waits for the next run
	Cooldown  time.Duration // first rest after a hard throttle
	MaxCool   time.Duration // ceiling for the doubling cooldown
}

var policies = map[string]policy{
	// Splice's public GraphQL. Unauthenticated and free; treat it gently.
	"splice-graphql": {
		Burst:     8,
		Pace:      1200 * time.Millisecond,
		MaxPerRun: 120,
		Cooldown:  30 * time.Minute,
		MaxCool:   12 * time.Hour,
	},
}

var defaultPolicy = policy{
	Burst:     8,
	Pace:      time.Second,
	MaxPerRun: 100,
	Cooldown:  30 * time.Minute,
	MaxCool:   12 * time.Hour,
}

func policyFor(strategy string) policy {
	if p, ok := policies[strategy]; ok {
		return p
	}
	return defaultPolicy
}

// state is the resolver's memory between runs, per vendor. It is the only
// thing standing between an eager rescan cadence and a vendor's patience.
type state struct {
	ThrottledUntil time.Time `json:"throttled_until,omitempty"`
	Cooldown       string    `json:"cooldown,omitempty"` // duration string; doubles per consecutive throttle
	LastRun        time.Time `json:"last_run,omitempty"`
	Resolved       int       `json:"resolved_total,omitempty"`
}

const stateFile = "_state.json"

func loadState(root, vendorSlug string) state {
	var st state
	data, err := os.ReadFile(filepath.Join(root, "annotations-cache", "resolve", vendorSlug, stateFile))
	if err != nil {
		return st
	}
	json.Unmarshal(data, &st)
	return st
}

func saveState(root, vendorSlug string, st state) {
	dir := filepath.Join(root, "annotations-cache", "resolve", vendorSlug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	os.WriteFile(filepath.Join(dir, stateFile), append(data, '\n'), 0o644)
}

// nextCooldown doubles the previous rest, starting at the policy's first
// value and stopping at its ceiling.
func (p policy) nextCooldown(prev string) time.Duration {
	d, err := time.ParseDuration(prev)
	if err != nil || d <= 0 {
		return p.Cooldown
	}
	d *= 2
	if d > p.MaxCool {
		d = p.MaxCool
	}
	return d
}
