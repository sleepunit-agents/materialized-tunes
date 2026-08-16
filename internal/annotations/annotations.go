// Package annotations reads vendor annotation data — the community-shaped
// facts about how sample vendors structure their libraries (pack grammar,
// format trees, category folder variants, content identity). The data
// lives in a checkout of sample-vendor-annotations at
// <workspace>/annotations; mtunes only ever reads it.
package annotations

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Vendor struct {
	Slug     string   `toml:"slug" json:"slug"`
	Name     string   `toml:"name" json:"name"`
	Aliases  []string `toml:"aliases" json:"aliases,omitempty"`
	Homepage string   `toml:"homepage" json:"homepage,omitempty"`

	Grammar string `json:"grammar,omitempty"` // packs.grammar, e.g. "top-level-dirs"

	// Install: where this vendor's library lives by default, per OS.
	// A fact about the vendor, same as its pack grammar — used to offer
	// "you have Splice installed, add it?" without scanning the disk.
	InstallMac   []string `json:"install_macos,omitempty"`
	InstallLinux []string `json:"install_linux,omitempty"`
	InstallWin   []string `json:"install_windows,omitempty"`
	InstallNote  string   `json:"install_note,omitempty"`

	Categories []Category `json:"categories,omitempty"`
	Packs      []Pack     `json:"packs,omitempty"`

	dir string // vendor directory on disk, for manifest resolution
}

type Category struct {
	ID             string   `toml:"id" json:"id"`
	Match          []string `toml:"match" json:"match,omitempty"`
	DedicatedPacks []string `toml:"dedicated_packs" json:"dedicated_packs,omitempty"`
}

type Pack struct {
	Name          string   `toml:"name" json:"name"`
	Slug          string   `toml:"slug" json:"slug"`
	Dir           string   `toml:"dir" json:"dir"`
	URL           string   `toml:"url" json:"url,omitempty"`
	Provider      string   `toml:"provider" json:"provider,omitempty"`             // distributor vendors: the label the pack is BY
	SamplesListed int      `toml:"samples_listed" json:"samples_listed,omitempty"` // vendor's own count; honest denominator for partial copies
	Tags          []string `toml:"tags" json:"tags,omitempty"`                     // canonical tags (see tags.toml in the annotations repo)
	Archives      []string `toml:"archives" json:"archives,omitempty"`

	Meta     Meta     `json:"meta,omitempty"`
	Identity Identity `json:"identity,omitempty"`
	Dirs     []Dir    `json:"dirs,omitempty"`
}

type Meta struct {
	Title       string `toml:"title" json:"title,omitempty"`
	Type        string `toml:"type" json:"type,omitempty"`
	Image       string `toml:"image" json:"image,omitempty"`
	Description string `toml:"description" json:"description,omitempty"`
}

type Identity struct {
	Algo       string   `toml:"algo" json:"algo,omitempty"`
	AudioFiles int      `toml:"audio_files" json:"audio_files,omitempty"`
	AudioBytes int64    `toml:"audio_bytes" json:"audio_bytes,omitempty"`
	Digest     string   `toml:"digest" json:"digest,omitempty"`
	Anchors    []string `toml:"anchors" json:"anchors,omitempty"`
	Manifest   string   `toml:"manifest" json:"manifest,omitempty"`
}

type Dir struct {
	Path     string   `toml:"path" json:"path"`
	Role     string   `toml:"role" json:"role,omitempty"`
	Category string   `toml:"category" json:"category,omitempty"`
	Tags     []string `toml:"tags" json:"tags,omitempty"`
	Desc     string   `toml:"desc" json:"desc,omitempty"`
}

// Load reads every vendor under root. Root may be a checkout of the
// annotations repo (vendors/<slug>/...) or a bare directory of vendor
// dirs. A missing root is not an error — annotations are optional.
func Load(root string) ([]Vendor, error) {
	base := root
	if _, err := os.Stat(filepath.Join(root, "vendors")); err == nil {
		base = filepath.Join(root, "vendors")
	}
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var vendors []Vendor
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		vdir := filepath.Join(base, e.Name())
		v, err := loadVendor(vdir)
		if err != nil {
			return nil, err
		}
		if v != nil {
			vendors = append(vendors, *v)
		}
	}
	return vendors, nil
}

// BySlug indexes a Load result.
func BySlug(vendors []Vendor) map[string]*Vendor {
	m := make(map[string]*Vendor, len(vendors))
	for i := range vendors {
		m[vendors[i].Slug] = &vendors[i]
	}
	return m
}

// ByName finds a vendor by the name a human would put on a folder: slug,
// display name, or any alias, case-insensitively. Used by the vendor-dirs
// location layout, where the top-level directory names the vendor.
func ByName(vendors []Vendor, name string) *Vendor {
	for i := range vendors {
		v := &vendors[i]
		if strings.EqualFold(v.Slug, name) || strings.EqualFold(v.Name, name) {
			return v
		}
		for _, a := range v.Aliases {
			if strings.EqualFold(a, name) {
				return v
			}
		}
	}
	return nil
}

func loadVendor(dir string) (*Vendor, error) {
	var vf struct {
		Vendor struct {
			Name     string   `toml:"name"`
			Slug     string   `toml:"slug"`
			Aliases  []string `toml:"aliases"`
			Homepage string   `toml:"homepage"`
		} `toml:"vendor"`
		Packs struct {
			Grammar string `toml:"grammar"`
		} `toml:"packs"`
		Install struct {
			Macos   []string `toml:"macos"`
			Linux   []string `toml:"linux"`
			Windows []string `toml:"windows"`
			Note    string   `toml:"note"`
		} `toml:"install"`
		Category []Category `toml:"category"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "vendor.toml"))
	if os.IsNotExist(err) {
		return nil, nil // not a vendor dir; skip
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &vf); err != nil {
		return nil, err
	}
	v := &Vendor{
		Slug: vf.Vendor.Slug, Name: vf.Vendor.Name,
		Aliases: vf.Vendor.Aliases, Homepage: vf.Vendor.Homepage,
		Grammar: vf.Packs.Grammar, Categories: vf.Category,
		InstallMac: vf.Install.Macos, InstallLinux: vf.Install.Linux,
		InstallWin: vf.Install.Windows, InstallNote: vf.Install.Note,
		dir: dir,
	}
	if v.Slug == "" {
		v.Slug = filepath.Base(dir)
	}

	packFiles, _ := filepath.Glob(filepath.Join(dir, "packs", "*.toml"))
	for _, pf := range packFiles {
		var f struct {
			Pack struct {
				Name          string   `toml:"name"`
				Slug          string   `toml:"slug"`
				Dir           string   `toml:"dir"`
				URL           string   `toml:"url"`
				Provider      string   `toml:"provider"`
				SamplesListed int      `toml:"samples_listed"`
				Tags          []string `toml:"tags"`
				Archives      []string `toml:"archives"`
			} `toml:"pack"`
			Meta     Meta     `toml:"meta"`
			Identity Identity `toml:"identity"`
			Dir      []Dir    `toml:"dir"`
		}
		data, err := os.ReadFile(pf)
		if err != nil {
			return nil, err
		}
		if err := toml.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		v.Packs = append(v.Packs, Pack{
			Name: f.Pack.Name, Slug: f.Pack.Slug, Dir: f.Pack.Dir,
			URL: f.Pack.URL, Provider: f.Pack.Provider, SamplesListed: f.Pack.SamplesListed,
			Tags: f.Pack.Tags, Archives: f.Pack.Archives,
			Meta: f.Meta, Identity: f.Identity, Dirs: f.Dir,
		})
	}
	return v, nil
}

// PackByDir finds the pack annotated for an on-disk directory name.
func (v *Vendor) PackByDir(dir string) *Pack {
	for i := range v.Packs {
		if v.Packs[i].Dir == dir {
			return &v.Packs[i]
		}
	}
	return nil
}

// PackByArchive finds the pack whose vendor download matches a zip name —
// the unzip-and-leave-it identity signal.
func (v *Vendor) PackByArchive(zip string) *Pack {
	for i := range v.Packs {
		for _, a := range v.Packs[i].Archives {
			if strings.EqualFold(a, zip) {
				return &v.Packs[i]
			}
		}
	}
	return nil
}

// MatchIdentity compares the content SHAs observed under one directory
// against a pack's manifest. kind is "exact" (same content set), "partial"
// (fraction of the manifest present), or "" (no identity data, unreadable
// manifest, or nothing matches).
func (v *Vendor) MatchIdentity(p *Pack, shas map[string]bool) (kind string, fraction float64) {
	if p.Identity.Manifest == "" {
		return "", 0
	}
	f, err := os.Open(filepath.Join(v.dir, filepath.FromSlash(p.Identity.Manifest)))
	if err != nil {
		return "", 0
	}
	defer f.Close()

	total, present := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		total++
		if shas[line] {
			present++
		}
	}
	if sc.Err() != nil || total == 0 || present == 0 {
		return "", 0
	}
	fraction = float64(present) / float64(total)
	if present == total && len(shas) == total {
		return "exact", 1
	}
	return "partial", fraction
}
