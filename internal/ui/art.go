package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/browse"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
)

// ---- cover art: a thumbnail, served from disk, that never re-reads its ----
// ---- source and never re-walks the annotations to be allowed to exist. ----
//
// The library grid draws a 46 px cover per pack and pack detail an 88 px
// one, yet /api/art used to hand the browser the vendor's full-size
// product image — and, before it would, re-load every annotation TOML and
// every resolver JSON in the workspace to check the URL was one it knew,
// per image, per render. Art shipped inside a pack was worse: served in
// place, which meant SHA-256 of the file on the archive drive every time
// the grid redrew. "It shouldn't take a threadripper pro any time to
// display a bunch of thumbnails" (2026-09-02). Now:
//
//   - one thumbnail per image, at most thumbSide on the long edge, built
//     once into annotations-cache/img/thumb/ and served straight from
//     there. A hit touches nothing else: not the source file, not the
//     catalog beyond one map lookup, not the annotations.
//   - the allow-list is memoized. A URL already in it answers at once; an
//     unknown one rebuilds the list at most every allowRebuild, so a
//     freshly resolved pack shows its cover within a beat and a grid of
//     unknowns cannot turn into a grid of full annotation loads.

const (
	thumbSide    = 192             // 88 px box at 2× DPR, with room
	thumbQuality = 85              // JPEG quality for photographic sources
	allowRebuild = 2 * time.Second // floor between allow-list rebuilds on a miss
	maxPixels    = 50_000_000      // refuse to decode anything larger
)

// artAllow is the memoized allow-list of image and product-page URLs the
// annotation layer knows (see Server.allowedURLs).
type artAllow struct {
	mu     sync.Mutex
	images map[string]bool
	pages  map[string]bool
	built  time.Time
}

// allowedURL reports whether u is a known image (kind "img") or product
// page (kind "page"). A hit in the memo is instant; a miss rebuilds the
// memo from disk if the last build is older than allowRebuild.
func (s *Server) allowedURL(kind, u string) bool {
	a := &s.allow
	a.mu.Lock()
	defer a.mu.Unlock()
	pick := func() map[string]bool {
		if kind == "page" {
			return a.pages
		}
		return a.images
	}
	if pick()[u] {
		return true
	}
	if time.Since(a.built) < allowRebuild {
		return false
	}
	a.images, a.pages = s.allowedURLs()
	a.built = time.Now()
	return pick()[u]
}

// thumbPath is where the thumbnail for a source key lives. Extensionless:
// the file is JPEG or PNG by source and http.ServeFile sniffs which.
func (s *Server) thumbPath(key string) string {
	return filepath.Join(s.ws.Root, "annotations-cache", "img", "thumb", key)
}

func urlKey(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:12])
}

// art serves a pack's cover as a thumbnail. Two kinds of source: a
// catalog:<location>/<path> ref to art shipped inside the pack (keyed by
// the cataloged file's own hash, so a changed file is a new thumbnail),
// or a vendor URL from the annotations (fetched once into the workspace,
// only if the annotations name it — no open proxy).
func (s *Server) art(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	var key string
	var source func() ([]byte, int)
	if strings.HasPrefix(u, browse.CatalogScheme) {
		locName, path, ok := browse.SplitCatalogRef(u)
		if !ok {
			w.WriteHeader(404)
			return
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		default:
			w.WriteHeader(415)
			return
		}
		entries, err := catalog.Load(s.ws.CatalogPath(locName))
		if err != nil {
			w.WriteHeader(500)
			return
		}
		ce, ok := entries[path]
		if !ok || ce.SHA256 == "" {
			w.WriteHeader(404)
			return
		}
		key = "c-" + ce.SHA256
		source = func() ([]byte, int) {
			local, herr := s.localCopy(r, locName, path)
			if herr != nil {
				return nil, herr.code
			}
			data, err := os.ReadFile(local)
			if err != nil {
				return nil, 502
			}
			return data, 0
		}
	} else {
		if u == "" {
			w.WriteHeader(404)
			return
		}
		key = "u-" + urlKey(u)
		source = func() ([]byte, int) {
			if !s.allowedURL("img", u) {
				return nil, 403
			}
			orig := s.cachePath("img", u)
			data, err := os.ReadFile(orig)
			if err != nil {
				if data, err = fetchURL(u); err != nil {
					return nil, 502
				}
				os.MkdirAll(filepath.Dir(orig), 0o755)
				os.WriteFile(orig, data, 0o644)
			}
			return data, 0
		}
	}

	thumb := s.thumbPath(key)
	if _, err := os.Stat(thumb); err != nil {
		data, code := source()
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		out, err := thumbnail(data, thumbSide)
		if err != nil {
			w.WriteHeader(415)
			return
		}
		if err := writeAtomic(thumb, out); err != nil {
			// disk trouble: still answer this request
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", http.DetectContentType(out))
			w.Write(out)
			return
		}
	}
	// Content-addressed (catalog) or pinned to the URL the annotations
	// give (vendor): neither changes under the same key.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, thumb)
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// thumbnail scales an encoded image so its long edge is at most side
// pixels. JPEG sources come back as JPEG; anything with an alpha channel
// (PNG, GIF) as PNG so logos on transparency stay transparent. A source
// already within side, or one Go cannot decode (WebP), is returned as-is
// — the browser handles it and object-fit does the rest.
func thumbnail(src []byte, side int) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		if http.DetectContentType(src) == "image/webp" {
			return src, nil
		}
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("image %dx%d out of range", cfg.Width, cfg.Height)
	}
	if cfg.Width <= side && cfg.Height <= side {
		return src, nil
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	small := downsample(img, side)
	var buf bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&buf, small, &jpeg.Options{Quality: thumbQuality})
	} else {
		err = png.Encode(&buf, small)
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// downsample is a box filter: every destination pixel is the mean of the
// source block it covers. For shrinking by 3–10× (a 600–2000 px product
// shot to 192) that is the right filter — it averages every source pixel
// instead of sampling a few, so fine texture becomes tone rather than
// moiré — and it needs nothing outside the standard library.
func downsample(img image.Image, side int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if sw >= sh {
		dw, dh = side, max(1, sh*side/sw)
	} else {
		dw, dh = max(1, sw*side/sh), side
	}
	// premultiplied RGBA in, so averaging is linear in coverage
	src, ok := img.(*image.RGBA)
	if !ok || src.Bounds().Min != (image.Point{}) {
		src = image.NewRGBA(image.Rect(0, 0, sw, sh))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a uint64
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[sy*src.Stride+x0*4 : sy*src.Stride+x1*4]
				for i := 0; i < len(row); i += 4 {
					r += uint64(row[i])
					g += uint64(row[i+1])
					bl += uint64(row[i+2])
					a += uint64(row[i+3])
				}
			}
			n := uint64((y1 - y0) * (x1 - x0))
			dst.SetRGBA(x, y, color.RGBA{uint8((r + n/2) / n), uint8((g + n/2) / n), uint8((bl + n/2) / n), uint8((a + n/2) / n)})
		}
	}
	return dst
}
