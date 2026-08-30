// Package resolve identifies packs of marketplace vendors — the ones whose
// catalog is unbounded and growing daily, so the annotations repo ships
// their grammar but no per-pack files — by asking the vendor's own public
// API and caching the answer in the workspace. Each user's Splice library
// is their own cache; the shared repo stays about pack houses.
//
// Cache: annotations-cache/resolve/<vendor>/<pack-dir>.json, one per pack
// dir, including negative answers (delisted packs) so they aren't re-asked
// every scan. Facts only — name, slug, URL, provider, cover pointer —
// never prose or pixels; the art endpoint fetches covers into its own cache
// like it does for repo-annotated packs.
package resolve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Pack is a resolved (or unresolvable) marketplace pack.
type Pack struct {
	Dir        string    `json:"dir"`
	Name       string    `json:"name,omitempty"`
	Slug       string    `json:"slug,omitempty"`
	UUID       string    `json:"uuid,omitempty"`
	URL        string    `json:"url,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Image      string    `json:"image,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	ResolvedAt time.Time `json:"resolved_at"`
	Missing    bool      `json:"missing,omitempty"` // asked, not found (delisted); retried after MissingTTL
	Error      string    `json:"error,omitempty"`   // transient failure; retried next run
}

// MissingTTL is how long a "not on Splice" answer is trusted before it is
// asked again — packs do get relisted, and our probe file may have been a
// bad pick.
const MissingTTL = 30 * 24 * time.Hour

var safeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func cacheDir(ws *workspace.Workspace, vendorSlug string) string {
	return filepath.Join(ws.Root, "annotations-cache", "resolve", vendorSlug)
}

func cachePath(ws *workspace.Workspace, vendorSlug, packDir string) string {
	return filepath.Join(cacheDir(ws, vendorSlug), safeName.ReplaceAllString(packDir, "_")+".json")
}

// Load returns the cache for one vendor: pack dir → resolved pack. Missing
// cache is empty, not an error.
func Load(ws *workspace.Workspace, vendorSlug string) map[string]Pack {
	out := map[string]Pack{}
	entries, err := os.ReadDir(cacheDir(ws, vendorSlug))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == stateFile {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cacheDir(ws, vendorSlug), e.Name()))
		if err != nil {
			continue
		}
		var p Pack
		if json.Unmarshal(data, &p) == nil && p.Dir != "" {
			out[p.Dir] = p
		}
	}
	return out
}

func save(ws *workspace.Workspace, vendorSlug string, p Pack) error {
	if err := os.MkdirAll(cacheDir(ws, vendorSlug), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(cachePath(ws, vendorSlug, p.Dir), append(data, '\n'), 0o644)
}

// Result summarizes one run.
type Result struct {
	Packs, Resolved, Missing, Cached, Failed int
	Remaining                                int       // left for the next run (per-run cap, or a throttle)
	Throttled                                bool      // the vendor closed its window mid-run
	CoolingUntil                             time.Time // non-zero: resting until then, nothing was asked
}

// Progress reports resolver activity (done, total).
type Progress func(done, total int)

// Location resolves every pack dir of a location whose vendor declares a
// resolver. Cached answers are reused; only new dirs (and expired
// negatives / earlier failures) hit the network. Best-effort: a network
// failure records an Error entry and moves on.
func Location(ctx context.Context, ws *workspace.Workspace, lc workspace.LocationConfig, vendors []annotations.Vendor, progress Progress) (*Result, error) {
	res := &Result{}
	if lc.Layout == "vendor-dirs" || lc.Vendor == "" {
		return res, nil // resolvers apply to single-vendor locations
	}
	v := annotations.BySlug(vendors)[lc.Vendor]
	if v == nil || v.Resolver == "" {
		return res, nil
	}
	r, ok := strategies[v.Resolver]
	if !ok {
		return nil, fmt.Errorf("location %s: unknown resolver %q", lc.Name, v.Resolver)
	}
	entries, err := catalog.Load(ws.CatalogPath(lc.Name))
	if err != nil {
		return nil, err
	}
	// Probe files per pack dir: first, middle and last audio path in sort
	// order — a renamed or delisted single sample must not sink the pack.
	byPack := map[string][]string{}
	paths := make([]string, 0, len(entries))
	for p, e := range entries {
		if e.Audio != nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		top, rest, ok := strings.Cut(p, "/")
		if !ok || rest == "" {
			continue
		}
		byPack[top] = append(byPack[top], rest)
	}
	probe := map[string][]string{}
	for top, all := range byPack {
		picks := []string{all[0]}
		if n := len(all); n > 2 {
			picks = append(picks, all[n/2], all[n-1])
		} else if n == 2 {
			picks = append(picks, all[1])
		}
		probe[top] = picks
	}
	res.Packs = len(probe)
	have := Load(ws, v.Slug)
	tags := annotations.LoadTagMap(filepath.Join(ws.Root, "annotations"))
	dirs := make([]string, 0, len(probe))
	for d := range probe {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	// Split the work before doing any of it, so the counts are honest even
	// when the run stops early, and a fully-cached library touches no
	// network at all.
	var todo []string
	for _, d := range dirs {
		if c, ok := have[d]; ok && c.Error == "" && (!c.Missing || time.Since(c.ResolvedAt) < MissingTTL) {
			res.Cached++
			continue
		}
		todo = append(todo, d)
	}
	if len(todo) == 0 {
		return res, nil
	}

	// Respect a cooldown the last run earned, even across processes.
	pol := policyFor(v.Resolver)
	st := loadState(ws.Root, v.Slug)
	if time.Now().Before(st.ThrottledUntil) {
		res.Remaining = len(todo)
		res.CoolingUntil = st.ThrottledUntil
		return res, nil
	}

	asked := 0
	done := 0
	for _, d := range todo {
		done++
		if progress != nil {
			progress(done, len(todo))
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		// A big library is resolved over several runs; the cache makes that
		// free, and the vendor never sees a flood.
		if pol.MaxPerRun > 0 && asked >= pol.MaxPerRun {
			res.Remaining = len(todo) - done + 1
			break
		}
		if asked >= pol.Burst {
			if !sleep(ctx, pol.Pace) {
				return res, ctx.Err()
			}
		}
		asked++
		var p Pack
		var err error
		for _, fp := range probe[d] {
			p, err = withBackoff(ctx, func() (Pack, error) { return r(ctx, d, fp) })
			if err != nil || p.Name != "" {
				break // a hit, or a real failure; only "not found" tries the next probe
			}
		}
		p.Dir = d
		p.ResolvedAt = time.Now().UTC()
		p.Tags = tags.Canonical(p.Tags)
		switch {
		case errors.Is(err, errThrottled):
			// The vendor's window is closed. Stop immediately — a post-scan
			// resolve must never stall a scan — and remember the cooldown so
			// the next run doesn't walk straight back into it.
			cool := pol.nextCooldown(st.Cooldown)
			st.ThrottledUntil = time.Now().Add(cool)
			st.Cooldown = cool.String()
			st.LastRun = time.Now().UTC()
			saveState(ws.Root, v.Slug, st)
			res.Remaining = len(todo) - done + 1
			res.Throttled = true
			res.CoolingUntil = st.ThrottledUntil
			return res, nil
		case err != nil:
			p.Error = err.Error()
			res.Failed++
		case p.Name == "":
			p.Missing = true
			res.Missing++
		default:
			res.Resolved++
		}
		if err := save(ws, v.Slug, p); err != nil {
			return res, err
		}
	}
	// A run that got through without a hard throttle earns its cooldown back.
	if res.Resolved > 0 {
		st.Cooldown = ""
		st.ThrottledUntil = time.Time{}
	}
	st.LastRun = time.Now().UTC()
	st.Resolved += res.Resolved
	saveState(ws.Root, v.Slug, st)
	return res, nil
}

// rateLimited is returned by strategies when the vendor says slow down;
// RetryAfter is the vendor's hint (0 = none).
type rateLimited struct{ RetryAfter time.Duration }

func (e rateLimited) Error() string { return "rate limited" }

// errThrottled: backoff gave up — the vendor wants a long pause.
var errThrottled = errors.New("rate limited by the vendor; try again later")

// withBackoff runs one resolution, sleeping through 429s with the vendor's
// Retry-After (or a doubling wait, capped) and giving up after a few
// tries so a run never hangs on a throttled endpoint.
func withBackoff(ctx context.Context, fn func() (Pack, error)) (Pack, error) {
	wait := 2 * time.Second
	for attempt := 0; ; attempt++ {
		p, err := fn()
		var rl rateLimited
		if !errors.As(err, &rl) {
			return p, err
		}
		if attempt >= 3 {
			return p, errThrottled
		}
		d := wait
		if rl.RetryAfter > 0 {
			d = rl.RetryAfter
		}
		if d > 30*time.Second {
			d = 30 * time.Second
		}
		if !sleep(ctx, d) {
			return p, ctx.Err()
		}
		wait *= 2
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// strategy resolves one pack from its dir name and a probe file path
// within the pack. Empty Name with nil error = not found.
type strategy func(ctx context.Context, packDir, probePath string) (Pack, error)

var strategies = map[string]strategy{
	"splice-graphql": spliceGraphQL,
}

// ---- Splice -------------------------------------------------------------

const spliceEndpoint = "https://surfaces-graphql.splice.com/graphql"

// spliceGraphQL asks Splice's public GraphQL for the sample at a filepath
// (SampleAsset.name is exactly the path within the pack) and reads its
// parent pack: name, slug, provider, permalink base, cover file.
func spliceGraphQL(ctx context.Context, packDir, probePath string) (Pack, error) {
	// Search by the sample's basename — the full-path filter chokes on some
	// characters ("#" in "ff_at_synth_korg_C#min.wav") — and match the full
	// name strictly below.
	const q = `query($fp:String!){ assetsSearch(filter:{asset_type_slug:sample, filepath:$fp}, pagination:{limit:25}) { items { ... on SampleAsset { name parents(filter:{asset_type_slug:pack}) { items { ... on PackAsset { name uuid permalink_slug permalink_base_url provider { name permalink_slug } tags { label } files { url asset_file_type_slug } } } } } } } }`
	body, _ := json.Marshal(map[string]any{"query": q, "variables": map[string]string{"fp": path.Base(probePath)}})
	req, err := http.NewRequestWithContext(ctx, "POST", spliceEndpoint, bytes.NewReader(body))
	if err != nil {
		return Pack{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "mtunes/0.5 (+pack resolver; local cache)")
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return Pack{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Pack{}, err
	}
	if resp.StatusCode == 429 {
		var ra time.Duration
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, err := time.ParseDuration(s + "s"); err == nil {
				ra = secs
			}
		}
		return Pack{}, rateLimited{RetryAfter: ra}
	}
	if resp.StatusCode != 200 {
		return Pack{}, fmt.Errorf("splice: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			AssetsSearch struct {
				Items []struct {
					Name    string `json:"name"`
					Parents struct {
						Items []struct {
							Name     string `json:"name"`
							UUID     string `json:"uuid"`
							Slug     string `json:"permalink_slug"`
							Base     string `json:"permalink_base_url"`
							Provider struct {
								Name string `json:"name"`
								Slug string `json:"permalink_slug"`
							} `json:"provider"`
							Tags []struct {
								Label string `json:"label"`
							} `json:"tags"`
							Files []struct {
								URL  string `json:"url"`
								Type string `json:"asset_file_type_slug"`
							} `json:"files"`
						} `json:"items"`
					} `json:"parents"`
				} `json:"items"`
			} `json:"assetsSearch"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Pack{}, fmt.Errorf("splice: %w", err)
	}
	if len(out.Errors) > 0 {
		return Pack{}, errors.New("splice: " + out.Errors[0].Message)
	}
	// The search is fuzzy and happily returns unrelated packs for a path it
	// doesn't know, so only a real hit counts: the sample's name IS our
	// probe path, or shares its basename and immediate parent dir (a
	// renamed top-level folder). Anything looser attributes wrong packs —
	// verified the hard way (21 of 196 wrong with a loose fallback).
	probeBase := path.Base(probePath)
	probeDir := path.Base(path.Dir(probePath))
	for _, it := range out.Data.AssetsSearch.Items {
		if !strings.EqualFold(it.Name, probePath) &&
			!(strings.EqualFold(path.Base(it.Name), probeBase) && strings.EqualFold(path.Base(path.Dir(it.Name)), probeDir)) {
			continue
		}
		{
			for _, pk := range it.Parents.Items {
				if pk.Name == "" {
					continue
				}
				p := Pack{Name: pk.Name, Slug: pk.Slug, UUID: pk.UUID, Provider: pk.Provider.Name}
				if pk.Base != "" && pk.Slug != "" {
					p.URL = strings.TrimSuffix(pk.Base, "/") + "/" + pk.Slug
				}
				for _, f := range pk.Files {
					if f.Type == "cover_image" {
						p.Image = f.URL
						break
					}
				}
				for _, t := range pk.Tags {
					if t.Label != "" {
						p.Tags = append(p.Tags, t.Label)
					}
				}
				return p, nil
			}
		}
	}
	return Pack{}, nil // not found
}
