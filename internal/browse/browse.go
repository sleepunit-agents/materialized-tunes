// Package browse computes pack summaries — the browsing unit shared by
// `catalog packs` and the UI server. Pure aggregation over catalogs plus
// the annotation layer; no filesystem writes.
package browse

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jbarket/materialized-tunes/internal/annotations"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/plan"
	"github.com/jbarket/materialized-tunes/internal/profile"
	"github.com/jbarket/materialized-tunes/internal/resolve"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

// Row is one pack summary — annotated when the location names a vendor.
type Row struct {
	Location   string `json:"location"`
	Dir        string `json:"dir"`  // catalog path prefix of the pack ("808 From Mars", or "Samples From Mars/808 From Mars" under vendor-dirs)
	Name       string `json:"name"` // display name: annotated pack name, else the pack's own directory name
	Vendor     string `json:"vendor,omitempty"`
	VendorSlug string `json:"vendor_slug,omitempty"` // set when the row resolved against an annotated vendor
	Tier       string `json:"tier"` // "vendor" (annotations) | "docs" (art/about shipped in the pack) | "top-level-dirs" (honest fallback)

	Slug          string   `json:"slug,omitempty"`
	URL           string   `json:"url,omitempty"`
	Image         string   `json:"image,omitempty"`       // vendor CDN URL, or "catalog:<location>/<path>" for art living in the archive
	Blurb         string   `json:"blurb,omitempty"`       // "catalog:<location>/<path>" of an About file in the archive; the UI falls back to URL
	Description   string   `json:"description,omitempty"` // inline prose from annotations — only discontinued packs carry it (SCHEMA exception)
	Provider      string   `json:"provider,omitempty"`
	SamplesListed int      `json:"samples_listed,omitempty"`
	Tags          []string `json:"tags,omitempty"`

	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`

	Eligible       int   `json:"eligible,omitempty"`        // device lens only
	ConvertedBytes int64 `json:"converted_bytes,omitempty"` // device lens only

	Match         string  `json:"match,omitempty"` // "exact" | "partial"
	MatchFraction float64 `json:"match_fraction,omitempty"`
}

// CatalogScheme prefixes Image/Blurb values that point into the archive
// itself rather than at a vendor URL: "catalog:<location>/<catalog path>".
const CatalogScheme = "catalog:"

// SplitCatalogRef parses a CatalogScheme value into location and path.
func SplitCatalogRef(ref string) (location, path string, ok bool) {
	rest, found := strings.CutPrefix(ref, CatalogScheme)
	if !found {
		return "", "", false
	}
	location, path, ok = strings.Cut(rest, "/")
	return location, path, ok && location != "" && path != ""
}

// Rows aggregates pack summaries across locations. dev applies the device
// lens (nil = off); location filters to one location ("" = all).
func Rows(ws *workspace.Workspace, dev *profile.Device, location string) ([]Row, error) {
	vendors, err := annotations.Load(filepath.Join(ws.Root, "annotations"))
	if err != nil {
		return nil, err
	}
	bySlug := annotations.BySlug(vendors)

	var rows []Row
	for _, lc := range ws.Config.Locations {
		if location != "" && lc.Name != location {
			continue
		}
		entries, err := catalog.Load(ws.CatalogPath(lc.Name))
		if err != nil {
			return nil, err
		}
		vendorDirs := lc.Layout == "vendor-dirs"

		// Marketplace vendors: per-pack facts come from the local resolver
		// cache (annotations-cache/resolve/<vendor>/), not the repo.
		var resolved map[string]resolve.Pack
		if !vendorDirs {
			if v := bySlug[lc.Vendor]; v != nil && v.Resolver != "" {
				resolved = resolve.Load(ws, v.Slug)
			}
		}

		// Group catalog entries by pack dir. Files that sit above pack level
		// (sibling archives, vendor-root previews) are not pack content.
		groups := map[string][]catalog.Entry{}
		for _, ce := range entries {
			top, rest, found := strings.Cut(ce.Path, "/")
			if !found || rest == "" {
				continue
			}
			key := top
			if vendorDirs {
				pack, rest2, found2 := strings.Cut(rest, "/")
				if !found2 || rest2 == "" {
					continue
				}
				key = top + "/" + pack
			}
			groups[key] = append(groups[key], ce)
		}

		for dir, ces := range groups {
			row := Row{Location: lc.Name, Dir: dir, Name: dir, Tier: "top-level-dirs"}
			var vendor *annotations.Vendor
			packDir := dir
			if vendorDirs {
				vname, pname, _ := strings.Cut(dir, "/")
				row.Vendor, row.Name, packDir = vname, pname, pname
				vendor = annotations.ByName(vendors, vname)
			} else {
				vendor = bySlug[lc.Vendor]
			}
			if vendor != nil {
				row.Tier = "vendor"
				row.VendorSlug = vendor.Slug
				if row.Vendor == "" {
					row.Vendor = vendor.Name
				}
				if p := vendor.PackByDir(packDir); p != nil {
					row.Name, row.Slug, row.URL = p.Name, p.Slug, p.URL
					row.Image = p.Meta.Image
					row.Description = p.Meta.Description
					row.Provider = p.Provider
					row.SamplesListed = p.SamplesListed
					row.Tags = p.Tags
					shas := make(map[string]bool, len(ces))
					for _, ce := range ces {
						if isAudioPath(ce.Path) {
							shas[ce.SHA256] = true
						}
					}
					row.Match, row.MatchFraction = vendor.MatchIdentity(p, shas)
				} else if rp, ok := resolved[packDir]; ok && rp.Name != "" {
					// resolved from the vendor's API and cached locally
					row.Name, row.Slug, row.URL = rp.Name, rp.Slug, rp.URL
					row.Image, row.Provider, row.Tags = rp.Image, rp.Provider, rp.Tags
				}
			}
			// Docs tier: art and an About file living in the pack itself
			// (SFM ships them; the house archive adds them to everyone else,
			// SPEC §3.1). Fills whatever the vendor layer left empty.
			applyDocs(&row, lc, ces)

			for _, ce := range ces {
				row.Files++
				row.Bytes += ce.Size
				if dev != nil && plan.Eligibility(dev, ce) == "" {
					row.Eligible++
					row.ConvertedBytes += plan.ConvertedBytes(dev, ce)
				}
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Location != rows[j].Location {
			return rows[i].Location < rows[j].Location
		}
		return rows[i].Dir < rows[j].Dir
	})
	return rows, nil
}

var (
	docsDirRe     = regexp.MustCompile(`(?i)^(\d+\.\s*)?docs?$`)
	docsRootArtRe = regexp.MustCompile(`(?i)cover|artwork|\bart\b`)
	docsArtSkip   = regexp.MustCompile(`(?i)install|saving|favorites|screenshot`)
	docsAboutRe   = regexp.MustCompile(`(?i)about|read ?me`)
	docsImageExt  = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
)

// applyDocs looks for <pack>/Docs/ art and an About file among the pack's
// entries. Art becomes a catalog: image ref; a markdown About becomes a
// catalog: blurb ref and lends its product URL. Only local locations are
// read for the URL (remote About files still work as blurbs, via the cache).
func applyDocs(row *Row, lc workspace.LocationConfig, ces []catalog.Entry) {
	root := row.Dir + "/"
	packName := row.Dir[strings.LastIndex(row.Dir, "/")+1:]
	var art, about string
	for _, ce := range ces {
		rest := strings.TrimPrefix(ce.Path, root)
		if rest == ce.Path {
			continue
		}
		// Accept <pack>/Docs/<file> (SFM also numbers it: "5. Docs"), and
		// <pack>/<cover image> at the pack root (Blu Mar Ten's convention).
		dir, name, nested := strings.Cut(rest, "/")
		if nested {
			if !docsDirRe.MatchString(dir) {
				continue
			}
			name = name[strings.LastIndex(name, "/")+1:] // Docs/Artwork/<file> is fine
		} else {
			name = rest
			if !docsRootArtRe.MatchString(name) {
				continue
			}
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch {
		case docsImageExt[ext] && !docsArtSkip.MatchString(name):
			if art == "" || docsScore(name, packName) > docsScore(art[strings.LastIndex(art, "/")+1:], packName) {
				art = ce.Path
			}
		case (ext == ".md" || ext == ".rtf" || ext == ".txt") && docsAboutRe.MatchString(name):
			if about == "" || docsScore(name, packName) > docsScore(about[strings.LastIndex(about, "/")+1:], packName) {
				about = ce.Path
			}
		}
	}
	if art == "" && about == "" {
		return
	}
	if row.Tier == "top-level-dirs" {
		row.Tier = "docs"
	}
	if art != "" && row.Image == "" {
		row.Image = CatalogScheme + lc.Name + "/" + art
	}
	if about != "" {
		if row.Blurb == "" {
			row.Blurb = CatalogScheme + lc.Name + "/" + about
		}
		if row.URL == "" && lc.Type == "local" {
			if _, _, url := ReadAbout(filepath.Join(lc.Root, filepath.FromSlash(about))); url != "" {
				row.URL = url
			}
		}
	}
}

// docsScore ranks candidate docs files: one that names the pack itself
// beats a sibling kit's ("Artwork - Sample Journal From Mars.jpg" over
// "Artwork - 909 Tube Kit.jpg"), and an explicit "Artwork"/"About" prefix
// beats an incidental image.
func docsScore(name, packName string) int {
	l := strings.ToLower(name)
	score := 0
	if packName != "" && strings.Contains(l, strings.ToLower(packName)) {
		score += 2
	}
	if strings.HasPrefix(l, "artwork") || strings.HasPrefix(l, "art -") || strings.Contains(l, "about") {
		score++
	}
	return score
}

var (
	aboutURLRe   = regexp.MustCompile(`(?i)\*\*product page:\*\*\s*(https?://\S+)`)
	aboutTitleRe = regexp.MustCompile(`^#\s+(.+)$`)
	mdBoldRe     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// ReadAbout parses an About file shipped with a pack. Markdown (the house
// convention): the H1 becomes the title, the body (minus the H1 and the
// trailing "---" provenance note) the description, and a
// "**Product page:** <url>" line the URL. RTF (what SFM ships): stripped
// to plain text, first line as title. Plain text: as is.
func ReadAbout(path string) (title, description, url string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", ""
	}
	if len(data) > 1<<20 {
		data = data[:1<<20]
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rtf":
		text := rtfToText(string(data))
		title, description, _ = strings.Cut(text, "\n")
		title = strings.TrimSpace(title)
		description = strings.TrimSpace(description)
		if description == "" {
			description, title = title, ""
		}
		return title, description, ""
	case ".md":
	default:
		return "", strings.TrimSpace(string(data)), ""
	}
	var body []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if title == "" {
			if m := aboutTitleRe.FindStringSubmatch(line); m != nil {
				title = strings.TrimSpace(m[1])
				continue
			}
		}
		if url == "" {
			if m := aboutURLRe.FindStringSubmatch(line); m != nil {
				url = strings.TrimRight(m[1], ").,")
			}
		}
		if strings.TrimSpace(line) == "---" {
			break // provenance trailer; not prose
		}
		body = append(body, mdBoldRe.ReplaceAllString(line, "$1"))
	}
	description = strings.TrimSpace(strings.Join(body, "\n"))
	return title, description, url
}

// rtfToText is the crude-but-sufficient RTF stripper for vendor About
// files (TextEdit output): destination groups ({\*...}, fonttbl,
// colortbl, stylesheet…) are dropped, \par and \line become newlines,
// \'hh escapes decode as Latin-1, every other control word vanishes.
func rtfToText(s string) string {
	var out strings.Builder
	depth, skipDepth := 0, -1
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '{':
			depth++
			// destination group we never want text from
			if skipDepth < 0 && strings.HasPrefix(s[i:], `{\*`) {
				skipDepth = depth
			}
			for _, d := range []string{`{\fonttbl`, `{\colortbl`, `{\stylesheet`, `{\info`, `{\pict`, `{\listtable`, `{\listoverridetable`, `{\expandedcolortbl`} {
				if skipDepth < 0 && strings.HasPrefix(s[i:], d) {
					skipDepth = depth
				}
			}
			i++
		case c == '}':
			if depth == skipDepth {
				skipDepth = -1
			}
			depth--
			i++
		case c == '\\':
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '\'':
				if i+2 < len(s) {
					var b byte
					fmt.Sscanf(s[i+1:i+3], "%02x", &b)
					if skipDepth < 0 {
						out.WriteRune(cp1252(b))
					}
				}
				i += 3
			case '\\', '{', '}':
				if skipDepth < 0 {
					out.WriteByte(s[i])
				}
				i++
			case '\n', '\r':
				// "\<newline>" is a paragraph break in Cocoa-written RTF.
				if skipDepth < 0 {
					out.WriteByte('\n')
				}
				i++
			default:
				j := i
				for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
					j++
				}
				word := s[i:j]
				k := j
				if k < len(s) && s[k] == '-' {
					k++
				}
				for k < len(s) && s[k] >= '0' && s[k] <= '9' {
					k++
				}
				if k < len(s) && s[k] == ' ' {
					k++ // the delimiter space belongs to the control word
				}
				if skipDepth < 0 {
					switch word {
					case "par", "line":
						out.WriteByte('\n')
					case "tab":
						out.WriteByte('\t')
					case "u":
						// \uN — unicode code point, followed by a fallback char we skip
						if n, err := strconv.Atoi(strings.TrimSuffix(s[j:k], " ")); err == nil {
							if n < 0 {
								n += 65536
							}
							out.WriteRune(rune(n))
							if k < len(s) && s[k] != '\\' && s[k] != '{' && s[k] != '}' {
								k++
							}
						}
					}
				}
				i = k
			}
		case c == '\r' || c == '\n':
			i++
		default:
			if skipDepth < 0 {
				out.WriteByte(c)
			}
			i++
		}
	}
	text := regexp.MustCompile(`\n{3,}`).ReplaceAllString(out.String(), "\n\n")
	return strings.TrimSpace(text)
}

// cp1252 decodes one Windows-1252 byte (RTF's \ansicpg1252 default): the
// 0x80–0x9F range holds the curly quotes and dashes vendors actually use.
func cp1252(b byte) rune {
	if b < 0x80 || b >= 0xA0 {
		return rune(b) // identical to Latin-1 / Unicode
	}
	table := [32]rune{
		'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
		0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
	}
	return table[b-0x80]
}

func isAudioPath(p string) bool {
	l := strings.ToLower(p)
	return strings.HasSuffix(l, ".wav") || strings.HasSuffix(l, ".aif") || strings.HasSuffix(l, ".aiff")
}
