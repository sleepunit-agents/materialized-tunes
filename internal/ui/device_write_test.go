package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

func deviceServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Server{ws: &workspace.Workspace{Root: root}}
}

func postDevice(t *testing.T, s *Server, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	s.deviceWrite(w, httptest.NewRequest(http.MethodPost, "/api/device", strings.NewReader(string(b))))
	return w
}

func listDevices(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.devices(w, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("devices: %v\n%s", err, w.Body.String())
	}
	return out
}

// A device created with companions carries the User Library subfolder the
// form was given, the listing hands it back in form shape, and re-saving
// with overwrite (the UI's edit) replaces the file rather than 409ing.
func TestDeviceCompanionsPrefixRoundTrip(t *testing.T) {
	s := deviceServer(t)
	base := map[string]any{"name": "push", "bit_depth": 24, "sample_rate": 48000, "channels": "stereo",
		"mode": "card", "layout": "mirror", "companions": true, "user_library_prefix": "Samples/mtunes"}
	if w := postDevice(t, s, base); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	src, _ := os.ReadFile(filepath.Join(s.ws.Root, "devices", "push.toml"))
	if !strings.Contains(string(src), `user_library_prefix = "Samples/mtunes"`) {
		t.Fatalf("prefix not written:\n%s", src)
	}
	devs := listDevices(t, s)
	if len(devs) != 1 {
		t.Fatalf("devices: %v", devs)
	}
	form := devs[0]["form"].(map[string]any)
	if form["companions"] != true || form["user_library_prefix"] != "Samples/mtunes" || form["bit_depth"] != float64(24) {
		t.Fatalf("form: %v", form)
	}
	if !strings.Contains(devs[0]["sub"].(string), "24-bit") {
		t.Fatalf("sub: %v", devs[0]["sub"])
	}

	// same name again without overwrite: refused, file untouched
	again := map[string]any{}
	for k, v := range base {
		again[k] = v
	}
	again["user_library_prefix"] = "Elsewhere"
	if w := postDevice(t, s, again); w.Code != 409 {
		t.Fatalf("expected 409, got %d %s", w.Code, w.Body.String())
	}
	src, _ = os.ReadFile(filepath.Join(s.ws.Root, "devices", "push.toml"))
	if !strings.Contains(string(src), `"Samples/mtunes"`) {
		t.Fatalf("409 must not write:\n%s", src)
	}

	// the edit: overwrite, companions off → no [companions] block at all
	again["overwrite"] = true
	again["companions"] = false
	if w := postDevice(t, s, again); w.Code != 200 {
		t.Fatalf("overwrite: %d %s", w.Code, w.Body.String())
	}
	src, _ = os.ReadFile(filepath.Join(s.ws.Root, "devices", "push.toml"))
	if strings.Contains(string(src), "[companions]") {
		t.Fatalf("companions should be gone:\n%s", src)
	}
	if f := listDevices(t, s)[0]["form"].(map[string]any); f["companions"] != false {
		t.Fatalf("form after edit: %v", f)
	}
}

// An empty subfolder means Samples (the historical default); backslashes
// and stray slashes are normalised; a path that escapes the library is
// refused before anything is written.
func TestDevicePrefixNormalisation(t *testing.T) {
	s := deviceServer(t)
	mk := func(name, prefix string) *httptest.ResponseRecorder {
		return postDevice(t, s, map[string]any{"name": name, "bit_depth": 16, "sample_rate": 44100, "channels": "stereo",
			"mode": "card", "layout": "mirror", "companions": true, "user_library_prefix": prefix})
	}
	if w := mk("a", ""); w.Code != 200 {
		t.Fatalf("empty: %d %s", w.Code, w.Body.String())
	}
	if w := mk("b", `\Samples\mtunes\`); w.Code != 200 {
		t.Fatalf("backslash: %d %s", w.Code, w.Body.String())
	}
	if w := mk("c", "../Outside"); w.Code != 400 {
		t.Fatalf("escape should be 400, got %d", w.Code)
	}
	for name, want := range map[string]string{"a": `user_library_prefix = "Samples"`, "b": `user_library_prefix = "Samples/mtunes"`} {
		src, _ := os.ReadFile(filepath.Join(s.ws.Root, "devices", name+".toml"))
		if !strings.Contains(string(src), want) {
			t.Errorf("%s: want %s in\n%s", name, want, src)
		}
	}
	if _, err := os.Stat(filepath.Join(s.ws.Root, "devices", "c.toml")); err == nil {
		t.Errorf("c.toml should not exist")
	}
}
