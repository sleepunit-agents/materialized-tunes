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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/workspace"
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
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
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
	// one probe file per pack dir: the first audio path in sort order
	probe := map[string]string{}
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
		if _, seen := probe[top]; !seen {
			probe[top] = rest
		}
	}
	res.Packs = len(probe)
	have := Load(ws, v.Slug)
	tags := annotations.LoadTagMap(filepath.Join(ws.Root, "annotations"))
	dirs := make([]string, 0, len(probe))
	for d := range probe {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	done := 0
	for _, d := range dirs {
		done++
		if progress != nil {
			progress(done, len(dirs))
		}
		if c, ok := have[d]; ok {
			fresh := c.Error == "" && (!c.Missing || time.Since(c.ResolvedAt) < MissingTTL)
			if fresh {
				res.Cached++
				continue
			}
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		p, err := withBackoff(ctx, func() (Pack, error) { return r(ctx, d, probe[d]) })
		p.Dir = d
		p.ResolvedAt = time.Now().UTC()
		p.Tags = tags.Canonical(p.Tags)
		switch {
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
	return res, nil
}

// rateLimited is returned by strategies when the vendor says slow down;
// RetryAfter is the vendor's hint (0 = none).
type rateLimited struct{ RetryAfter time.Duration }

func (e rateLimited) Error() string { return "rate limited" }

// pace is the floor between two API calls — polite by default; the vendor
// serves musicians, not crawlers.
const pace = 1 * time.Second

// withBackoff runs one resolution, sleeping through 429s with the vendor's
// Retry-After (or a doubling wait, capped) and giving up after a few
// tries so a run never hangs on a throttled endpoint.
func withBackoff(ctx context.Context, fn func() (Pack, error)) (Pack, error) {
	wait := 2 * time.Second
	for attempt := 0; ; attempt++ {
		p, err := fn()
		var rl rateLimited
		if !errors.As(err, &rl) {
			sleep(ctx, pace)
			return p, err
		}
		if attempt >= 5 {
			return p, errors.New("splice: rate limited; try again later")
		}
		d := wait
		if rl.RetryAfter > 0 {
			d = rl.RetryAfter
		}
		if d > 90*time.Second {
			d = 90 * time.Second
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
	const q = `query($fp:String!){ assetsSearch(filter:{asset_type_slug:sample, filepath:$fp}, pagination:{limit:5}) { items { ... on SampleAsset { name parents(filter:{asset_type_slug:pack}) { items { ... on PackAsset { name uuid permalink_slug permalink_base_url provider { name permalink_slug } tags { label } files { url asset_file_type_slug } } } } } } } }`
	body, _ := json.Marshal(map[string]any{"query": q, "variables": map[string]string{"fp": probePath}})
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
	// Prefer the sample whose name IS our probe path; the search is fuzzy.
	for pass := 0; pass < 2; pass++ {
		for _, it := range out.Data.AssetsSearch.Items {
			if pass == 0 && !strings.EqualFold(it.Name, probePath) {
				continue
			}
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
