package browse

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jbarket/materialized-tunes/internal/workspace"
)

// A workspace where the catalog holds one pack ("Full Pack") of an annotated
// vendor with four registry identities:
//   - full     (owned; asserts sampler-of → bigger)
//   - freebie  (vendor-free, subset-of full, manifest fully contained)
//   - bigger   (vendor-paid; the owned pack is its sampler — the upgrade row)
//   - gone     (discontinued orphan — recognized, not sourced)
func discoverFixture(t *testing.T) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	vdir := filepath.Join(root, "annotations", "vendors", "acme")
	for _, d := range []string{filepath.Join(vdir, "packs"), filepath.Join(vdir, "manifests"), filepath.Join(root, "catalog")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(vdir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vendor.toml", `
[vendor]
name = "Acme Audio"
slug = "acme"
role = "house"
domains = ["acme.example"]
[packs]
grammar = "top-level-dirs"
`)
	shaA, shaB, shaC := sha(t, "a"), sha(t, "b"), sha(t, "c")
	write("packs/full.toml", `
[pack]
name = "Full Pack"
slug = "full"
dir  = "Full Pack"
url  = "https://acme.example/full"
[acquisition]
class = "vendor-paid"
url = "https://acme.example/full"
gate = "purchase"
license = "royalty-free"
[identity]
manifest = "manifests/full.sha256"
[[relation]]
type = "sampler-of"
pack = "acme/bigger"
basis = "vendor-states"
`)
	write("manifests/full.sha256", shaA+"\n"+shaB+"\n"+shaC+"\n")
	write("packs/freebie.toml", `
[pack]
name = "Freebie Cuts"
slug = "freebie"
dir  = "Freebie Cuts"
[acquisition]
class = "vendor-free"
url = "https://acme.example/freebie"
gate = "email"
license = "royalty-free"
[identity]
manifest = "manifests/freebie.sha256"
[[relation]]
type = "subset-of"
pack = "acme/full"
basis = "sha"
`)
	write("manifests/freebie.sha256", shaA+"\n"+shaB+"\n")
	write("packs/bigger.toml", `
[pack]
name = "Bigger Pack"
slug = "bigger"
dir  = "Bigger Pack"
[acquisition]
class = "vendor-paid"
url = "https://acme.example/bigger"
gate = "purchase"
license = "royalty-free"
`)
	write("packs/gone.toml", `
[pack]
name = "Gone Pack"
slug = "gone"
dir  = "Gone Pack"
discontinued = true
[acquisition]
class = "orphan"
`)

	catalog := ""
	for i, s := range []string{shaA, shaB, shaC} {
		catalog += fmt.Sprintf(`{"path":"Full Pack/%d.wav","size":10,"mtime":0,"sha256":"%s","scanned_at":"2026-08-20T00:00:00Z"}`+"\n", i, s)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog", "arch.jsonl"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	return &workspace.Workspace{Root: root, Config: workspace.Config{Locations: []workspace.LocationConfig{
		{Name: "arch", Type: "local", Root: filepath.Join(root, "src"), Vendor: "acme"},
	}}}
}

func sha(t *testing.T, seed string) string {
	t.Helper()
	// any stable 64-hex string works; derive one from the seed byte
	out := ""
	for len(out) < 64 {
		out += fmt.Sprintf("%02x", seed[0])
	}
	return out[:64]
}

func TestDiscover(t *testing.T) {
	ws := discoverFixture(t)
	rows, err := Discover(ws)
	if err != nil {
		t.Fatal(err)
	}
	byslug := map[string]DiscoverRow{}
	for _, r := range rows {
		byslug[r.Slug] = r
	}
	if _, ok := byslug["full"]; ok {
		t.Error("owned pack leaked into discover")
	}
	fr, ok := byslug["freebie"]
	if !ok {
		t.Fatal("freebie missing from discover")
	}
	if !fr.Obtainable() || fr.URL != "https://acme.example/freebie" {
		t.Errorf("freebie should be obtainable with its vendor page, got class=%q url=%q", fr.Class, fr.URL)
	}
	if fr.HaveFraction < 0.999 {
		t.Errorf("freebie manifest is fully contained in the library; have_fraction = %v", fr.HaveFraction)
	}
	if len(fr.Relations) != 1 || fr.Relations[0].Type != "subset-of" || !fr.Relations[0].Owned || fr.Relations[0].Inverse {
		t.Errorf("freebie relations = %+v", fr.Relations)
	}
	if fr.Relations[0].Pack != "Full Pack" {
		t.Errorf("relation target should carry the display name, got %q", fr.Relations[0].Pack)
	}

	bg := byslug["bigger"]
	if !bg.Obtainable() {
		t.Error("bigger should be obtainable")
	}
	// the inverse hint: the owned pack asserts sampler-of toward bigger
	if len(bg.Relations) != 1 || !bg.Relations[0].Inverse || !bg.Relations[0].Owned || bg.Relations[0].Type != "sampler-of" || bg.Relations[0].Pack != "Full Pack" {
		t.Errorf("bigger should carry the owned-sampler upgrade hint, got %+v", bg.Relations)
	}

	gn := byslug["gone"]
	if gn.Obtainable() || gn.URL != "" {
		t.Errorf("orphan must carry no pointer, got class=%q url=%q", gn.Class, gn.URL)
	}
	if !gn.Discontinued {
		t.Error("discontinued flag lost")
	}
}
