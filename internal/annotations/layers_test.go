package annotations

import (
	"os"
	"path/filepath"
	"testing"
)

// The local layer is a partial tree in the repo's own layout; loading it
// after the checkout puts its entries first wherever both speak, unions
// its packs in, and adds vendors the checkout never heard of.
func TestLoadLayers(t *testing.T) {
	repo, local := t.TempDir(), t.TempDir()
	write := func(root, rel, body string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write(repo, "vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n[[instrument]]\nid=\"hat\"\naliases=[\"ch\"]\n[[category]]\nid=\"one-shots\"\nmatch=[\"Hits\"]\n")
	write(repo, "vendors/sfm/packs/101.toml", "[pack]\nname=\"101 From Mars\"\nslug=\"101-from-mars\"\ndir=\"101 From Mars\"\n[[dir]]\npath=\"WAV/Bass\"\ncategory=\"multisamples\"\n[[instrument]]\nid=\"kick\"\naliases=[\"boom\"]\n")
	// local: same vendor, same pack, a same-path [[dir]] with the opposite
	// answer; a new pack; a vendor-level block; and a vendor of its own
	write(local, "vendors/sfm/vendor.toml", "[vendor]\nslug=\"samples-from-mars\"\n[[instrument]]\nid=\"snare\"\naliases=[\"ch\"]\nlocal=true\n")
	write(local, "vendors/sfm/packs/101.toml", "[pack]\nslug=\"101-from-mars\"\n[[dir]]\npath=\"WAV/Bass\"\ncategory=\"one-shots\"\nobserved=\"2026-09-01\"\nnote=\"my copy\"\n[[dir]]\npath=\"WAV/Leads\"\ndefault_category=\"multisamples\"\ndefault_instrument=\"synth\"\n")
	write(local, "vendors/sfm/packs/new.toml", "[pack]\nname=\"New\"\nslug=\"new\"\ndir=\"New\"\n")
	write(local, "vendors/mine/vendor.toml", "[vendor]\nname=\"My Drums\"\nslug=\"mine\"\n")

	vendors, err := Load(repo, local, filepath.Join(local, "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	by := BySlug(vendors)
	sfm := by["samples-from-mars"]
	if sfm == nil || by["mine"] == nil || len(vendors) != 2 {
		t.Fatalf("vendors = %+v", vendors)
	}
	if sfm.Name != "Samples From Mars" {
		t.Errorf("scalar facts stay the checkout's: name = %q", sfm.Name)
	}
	if len(sfm.Instruments) != 2 || sfm.Instruments[0].ID != "snare" || !sfm.Instruments[0].Local || sfm.Instruments[0].Scope != "vendor" {
		t.Errorf("local vendor block must come first and keep its marker: %+v", sfm.Instruments)
	}
	if len(sfm.Packs) != 2 || sfm.PackByDir("New") == nil {
		t.Errorf("packs union: %+v", sfm.Packs)
	}
	p := sfm.PackByDir("101 From Mars")
	if p == nil || p.Name != "101 From Mars" {
		t.Fatalf("pack merge lost identity: %+v", p)
	}
	if len(p.Dirs) != 3 || p.Dirs[0].Category != "one-shots" || p.Dirs[0].Observed != "2026-09-01" || p.Dirs[0].Note != "my copy" {
		t.Errorf("local [[dir]] must come first with its provenance: %+v", p.Dirs)
	}
	if p.Dirs[1].DefaultCategory != "multisamples" || p.Dirs[1].DefaultInstrument != "synth" {
		t.Errorf("defaults not read: %+v", p.Dirs[1])
	}
	if len(p.Instruments) != 1 || p.Instruments[0].Scope != "pack" {
		t.Errorf("pack blocks: %+v", p.Instruments)
	}
}
