package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// The plan is a run and an artifact: the first request starts a build and
// reports progress, polls answer from the cache once it lands, every
// entry is attributed to the rule that picked it, and a toggled-off rule
// still says what it would bring back.
func TestPlanEndpoint(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1000, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 4410, DurationS: 0.1}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("A/P1/kick.wav", "1"), mk("A/P1/snare.wav", "2"), mk("A/P2/hat.wav", "3"), mk("B/P3/clap.wav", "4"),
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "views", "v.toml"), []byte(`name="v"
device="octatrack"
storage="octatrack-cf"
[[include]]
location="src"
glob="A/**"
[[include]]
location="src"
glob="B/**"
[[exclude]]
glob="**/hat.wav"
`), 0o644)
	ws, _ = workspace.Load(dir)
	s := &Server{ws: ws, plans: map[string]*planArtifact{}}

	post := func(body string) map[string]any {
		w := httptest.NewRecorder()
		s.planEndpoint(w, httptest.NewRequest(http.MethodPost, "/api/plan", strings.NewReader(body)))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %s", w.Body.String())
		}
		return out
	}
	settle := func(body string) map[string]any {
		for i := 0; i < 200; i++ {
			out := post(body)
			if out["status"] != "running" {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("plan never settled")
		return nil
	}
	first := settle(`{"view":"v","disabled":[]}`)
	if first["status"] != "done" || first["files"].(float64) != 3 {
		t.Fatalf("first plan: %v", first)
	}
	rules := first["rules"].([]any)
	if r0, r1 := rules[0].(map[string]any), rules[1].(map[string]any); r0["files"].(float64) != 2 || r1["files"].(float64) != 1 || r1["converted_bytes"].(float64) == 0 {
		t.Errorf("attribution: %v", rules)
	}
	again := post(`{"view":"v","disabled":[]}`)
	if again["status"] != "done" || again["built"] != first["built"] {
		t.Errorf("second ask must answer from the artifact: %v vs %v", again["built"], first["built"])
	}
	if s.cachedPlan("v") == nil {
		t.Error("materialize should find the artifact")
	}
	off := settle(`{"view":"v","disabled":[0]}`)
	rules = off["rules"].([]any)
	if r0 := rules[0].(map[string]any); r0["enabled"] != false || r0["files"].(float64) != 2 || off["files"].(float64) != 1 {
		t.Errorf("toggled-off rule reports its matches less excludes: %v files=%v", rules, off["files"])
	}
	if s.cachedPlan("v") != nil {
		t.Error("a plan with rules off is not the one materialize may use")
	}
	// the library changed underneath: the artifact is stale, a rebuild lands
	cat["B/P3/rim.wav"] = mk("B/P3/rim.wav", "5")
	time.Sleep(20 * time.Millisecond)
	catalog.Write(ws.CatalogPath("src"), cat)
	after := settle(`{"view":"v","disabled":[]}`)
	if after["files"].(float64) != 4 || after["built"] == first["built"] {
		t.Errorf("stale inputs must rebuild: %v", after)
	}
}
