package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

func encJPEG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encPNG(t *testing.T, w, h int, alpha uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{200, 40, 40, alpha})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A thumbnail keeps the long edge at thumbSide and the aspect, comes back
// in the source's family (JPEG stays JPEG, PNG stays PNG with its alpha),
// and a source already small enough is passed through untouched.
func TestThumbnail(t *testing.T) {
	out, err := thumbnail(encJPEG(t, 800, 600), 192)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != 192 || cfg.Height != 144 {
		t.Errorf("jpeg 800x600 → %s %dx%d (%v)", format, cfg.Width, cfg.Height, err)
	}
	if len(out) > 20_000 {
		t.Errorf("thumbnail is %d bytes — not a thumbnail", len(out))
	}

	out, err = thumbnail(encPNG(t, 100, 400, 90), 192)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "png" || img.Bounds().Dx() != 48 || img.Bounds().Dy() != 192 {
		t.Errorf("png 100x400 → %s %v (%v)", format, img.Bounds(), err)
	}
	if _, _, _, a := img.At(10, 10).RGBA(); a>>8 < 85 || a>>8 > 95 {
		t.Errorf("alpha not preserved: %d", a>>8)
	}

	small := encPNG(t, 120, 120, 255)
	out, err = thumbnail(small, 192)
	if err != nil || !bytes.Equal(out, small) {
		t.Errorf("small source should pass through unchanged (%v)", err)
	}

	if _, err := thumbnail([]byte("<html>not an image</html>"), 192); err == nil {
		t.Error("garbage decoded")
	}
}

// The box filter averages every source pixel: a checkerboard shrunk 4×
// comes out as flat mid-grey, not as one of the two colours.
func TestDownsampleAverages(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			v := uint8(0)
			if (x+y)%2 == 0 {
				v = 255
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	small := downsample(img, 16)
	if small.Bounds().Dx() != 16 || small.Bounds().Dy() != 16 {
		t.Fatalf("bounds %v", small.Bounds())
	}
	c := small.RGBAAt(7, 7)
	if c.R < 120 || c.R > 135 || c.A != 255 {
		t.Errorf("checkerboard averaged to %v, want ~128 grey", c)
	}
}

func artServer(t *testing.T) (*Server, string) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src")
	os.MkdirAll(filepath.Join(src, "V", "P1", "Docs"), 0o755)
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: src, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	ws, _ = workspace.Load(dir)
	return &Server{ws: ws, plans: map[string]*planArtifact{}}, src
}

// Art shipped inside a pack: the first request builds the thumbnail from
// the cataloged file; every request after is answered from the thumbnail
// alone — the source can be gone and the cover still shows.
func TestArtCatalogRef(t *testing.T) {
	s, src := artServer(t)
	art := encJPEG(t, 1200, 1200)
	rel := "V/P1/Docs/Artwork - P1.jpg"
	full := filepath.Join(src, filepath.FromSlash(rel))
	os.WriteFile(full, art, 0o644)
	sum := sha256.Sum256(art)
	cat := map[string]catalog.Entry{rel: {Path: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(art)), ScannedAt: time.Now()}}
	if err := catalog.Write(s.ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}

	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.art(w, httptest.NewRequest(http.MethodGet, "/api/art?u="+url.QueryEscape("catalog:src/"+rel), nil))
		return w
	}
	w := get()
	if w.Code != 200 {
		t.Fatalf("first GET %d", w.Code)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || format != "jpeg" || cfg.Width != 192 || cfg.Height != 192 {
		t.Errorf("served %s %dx%d (%v)", format, cfg.Width, cfg.Height, err)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc == "" || cc == "max-age=86400" {
		t.Errorf("cache-control %q — a content-addressed thumbnail is immutable", cc)
	}
	if _, err := os.Stat(s.thumbPath("c-" + cat[rel].SHA256)); err != nil {
		t.Errorf("thumbnail not cached: %v", err)
	}

	os.Remove(full) // the archive drive went away
	w = get()
	if w.Code != 200 || len(w.Body.Bytes()) == 0 {
		t.Errorf("second GET %d with %d bytes — should come from the thumbnail, not the source", w.Code, w.Body.Len())
	}

	// not in the catalog: no file touched, 404
	w = httptest.NewRecorder()
	s.art(w, httptest.NewRequest(http.MethodGet, "/api/art?u=catalog:src/V/P1/Docs/other.jpg", nil))
	if w.Code != 404 {
		t.Errorf("uncataloged ref → %d", w.Code)
	}
}

// A vendor image is fetched once, only when the annotations name it, and
// the allow-list that says so is built once — not once per image.
func TestArtVendorURL(t *testing.T) {
	s, _ := artServer(t)
	var hits atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(encPNG(t, 600, 300, 255))
	}))
	defer cdn.Close()
	imgURL := cdn.URL + "/cover.png"

	vdir := filepath.Join(s.ws.Root, "annotations", "vendors", "acme")
	os.MkdirAll(filepath.Join(vdir, "packs"), 0o755)
	os.WriteFile(filepath.Join(vdir, "vendor.toml"), []byte("[vendor]\nname=\"Acme\"\nslug=\"acme\"\n"), 0o644)
	os.WriteFile(filepath.Join(vdir, "packs", "p1.toml"), []byte("[pack]\nname=\"P1\"\nslug=\"acme-p1\"\ndir=\"P1\"\n[meta]\nimage=\""+imgURL+"\"\n"), 0o644)

	get := func(u string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.art(w, httptest.NewRequest(http.MethodGet, "/api/art?u="+url.QueryEscape(u), nil))
		return w
	}
	w := get(imgURL)
	if w.Code != 200 {
		t.Fatalf("GET %d", w.Code)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || format != "png" || cfg.Width != 192 || cfg.Height != 96 {
		t.Errorf("served %s %dx%d (%v)", format, cfg.Width, cfg.Height, err)
	}
	for i := 0; i < 20; i++ {
		if w := get(imgURL); w.Code != 200 {
			t.Fatalf("repeat GET %d", w.Code)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("CDN fetched %d times, want once", n)
	}
	if w := get(cdn.URL + "/not-in-annotations.png"); w.Code != 403 {
		t.Errorf("unknown URL → %d, want 403", w.Code)
	}

	// the allow-list is memoized: a hit never rebuilds, a miss rebuilds at
	// most every allowRebuild
	built := s.allow.built
	s.allowedURL("img", imgURL)
	if s.allow.built != built {
		t.Error("a known URL rebuilt the allow-list")
	}
	s.allowedURL("img", "https://nope/x.png")
	if s.allow.built != built {
		t.Error("a miss inside the rebuild floor rebuilt the allow-list")
	}
	s.allow.built = time.Now().Add(-allowRebuild - time.Second)
	os.WriteFile(filepath.Join(vdir, "packs", "p2.toml"), []byte("[pack]\nname=\"P2\"\nslug=\"acme-p2\"\ndir=\"P2\"\n[meta]\nimage=\"https://nope/x.png\"\n"), 0o644)
	if !s.allowedURL("img", "https://nope/x.png") {
		t.Error("a URL added to the annotations was not picked up after the floor")
	}
}
