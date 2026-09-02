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

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/audio"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// Queues group the plan's failures by source folder, the tree walks the
// destination, a correction previews its radius, applies into the local
// layer, and the next plan reads it; an ack leaves the queue.
func TestReviewSurface(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Locations = []workspace.LocationConfig{{Name: "src", Type: "local", Root: dir, Layout: "vendor-dirs"}}
	ws.SaveConfig()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	write("annotations/instruments.toml", "[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drums\"]\n")
	write("annotations/categories.toml", "[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n[[category]]\nid=\"one-shots\"\naliases=[\"hit\"]\n")
	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1000, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 4410, DurationS: 0.1}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("A/P1/Noise/take 1.wav", "1"), mk("A/P1/Noise/take 2.wav", "2"), // nothing → unsorted
		mk("A/P1/Kicks/Kick 01.wav", "3"),   // instrument, no kind → uncategorized
		mk("A/P1/Loops/Kick Loop.wav", "4"), // placed
		// a rack over the uncategorized kicks inherits their gap, but the
		// question is the Kicks folder's, never the Racks folder's
		{Path: "A/P1/Racks/Kit.adg", SHA256: "5", Size: 1000, ScannedAt: time.Now(),
			Doc: &ableton.Doc{Refs: []ableton.Ref{{Rel: "../Kicks/Kick 01.wav", Name: "Kick 01.wav", Type: "3"}}}},
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	write("devices/live.toml", "name = \"live\"\n[audio]\nformat = \"wav\"\nbit_depth = 16\nsample_rate = 48000\nchannels = \"mono\"\nmax_duration_seconds = 5.0\n[delivery]\nmode = \"staged\"\n[companions]\ntypes = [\"adg\"]\n")
	write("views/v.toml", "name=\"v\"\ndevice=\"live\"\nstorage=\"octatrack-cf\"\nlayout=\"{family}/{instrument}/{category}/{pack}/{file}\"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	ws, _ = workspace.Load(dir)
	s := &Server{ws: ws, plans: map[string]*planArtifact{}}

	call := func(method, url, body string) map[string]any {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		switch {
		case strings.HasPrefix(url, "/api/plan/queues"):
			s.queues(w, req)
		case strings.HasPrefix(url, "/api/plan/dump"):
			s.dump(w, req)
		case strings.HasPrefix(url, "/api/plan/tree"):
			s.tree(w, req)
		case strings.HasPrefix(url, "/api/plan/folder"):
			s.folder(w, req)
		case strings.HasPrefix(url, "/api/plan"):
			s.planEndpoint(w, req)
		case strings.HasPrefix(url, "/api/correct"):
			s.correctEndpoint(w, req)
		case strings.HasPrefix(url, "/api/ack"):
			s.ackEndpoint(w, req)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: bad json: %s", method, url, w.Body.String())
		}
		if e, ok := out["error"]; ok {
			t.Fatalf("%s %s: %v", method, url, e)
		}
		return out
	}
	settle := func() map[string]any {
		for i := 0; i < 300; i++ {
			out := call(http.MethodPost, "/api/plan", `{"view":"v","disabled":[]}`)
			if out["status"] != "running" {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("plan never settled")
		return nil
	}
	settle()

	q := call(http.MethodGet, "/api/plan/queues?view=v", "")
	rows := q["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("queue rows (the Racks folder must not be one): %v", rows)
	}
	if pl := q["kinds"].(map[string]any); pl["uncategorized"].(float64) != 1 {
		t.Errorf("kind totals count samples only: %v", pl)
	}
	r0, r1 := rows[0].(map[string]any), rows[1].(map[string]any)
	if r0["folder"] != "A/P1/Noise" || r0["kind"] != "unsorted" || r0["count"].(float64) != 2 || r0["pack_path"] != "A/P1" {
		t.Errorf("row 0: %v", r0)
	}
	if r1["folder"] != "A/P1/Kicks" || r1["kind"] != "uncategorized" || r1["instrument"] != "kick" || r1["why"] == nil {
		t.Errorf("row 1: %v", r1)
	}
	// the dump is the queues with nothing left out: every folder, every file, the why
	dj := call(http.MethodGet, "/api/plan/dump?view=v&format=json", "")
	folders := dj["folders"].([]any)
	if len(folders) != 2 || dj["files"].(float64) != 3 {
		t.Fatalf("dump folders (the Racks folder must not be one): %v", dj)
	}
	f0 := folders[0].(map[string]any)
	if fs := f0["files"].([]any); f0["folder"] != "A/P1/Noise" || len(fs) != 2 || fs[0].(map[string]any)["name"] != "take 1.wav" {
		t.Errorf("dump folder 0 carries every file: %v", f0)
	}
	{
		w := httptest.NewRecorder()
		s.dump(w, httptest.NewRequest(http.MethodGet, "/api/plan/dump?view=v", nil))
		txt := w.Body.String()
		for _, want := range []string{"## unsorted · src: A/P1/Noise · 2 files", "  take 2.wav  — · —", "nothing spoke",
			"## uncategorized · src: A/P1/Kicks · 1 file", `instruments.toml "kick"`, "2 folders · 3 files need a decision: uncategorized 1 · unsorted 2"} {
			if !strings.Contains(txt, want) {
				t.Errorf("dump text lacks %q:\n%s", want, txt)
			}
		}
		if strings.Contains(txt, "Racks") {
			t.Errorf("dump text must not list the rack folder:\n%s", txt)
		}
		if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="plan-dump-v-`) {
			t.Errorf("dump is a download: %q", cd)
		}
	}
	tr := call(http.MethodGet, "/api/plan/tree?view=v", "")
	names := map[string]bool{}
	for _, d := range tr["dirs"].([]any) {
		names[d.(map[string]any)["name"].(string)] = true
	}
	if !names["Drums"] || !names["_Unsorted"] || tr["total"].(float64) != 5 {
		t.Errorf("tree root: %v", tr)
	}
	tr = call(http.MethodGet, "/api/plan/tree?view=v&prefix=Drums/Kick/Loops/P1", "")
	if fs := tr["files"].([]any); len(fs) != 1 || fs[0].(map[string]any)["source_path"] != "A/P1/Loops/Kick Loop.wav" || fs[0].(map[string]any)["why"] == nil {
		t.Errorf("tree leaf: %v", tr)
	}
	fo := call(http.MethodGet, "/api/plan/folder?view=v&location=src&folder=A/P1/Noise", "")
	if fs := fo["files"].([]any); len(fs) != 2 {
		t.Errorf("folder: %v", fo)
	}

	// the correction: kind A on Kicks, seen before written
	pv := call(http.MethodPost, "/api/correct", `{"location":"src","path":"A/P1/Kicks","facet":"category","value":"one-shots","preview":true}`)
	rad := pv["radius"].(map[string]any)
	if rad["covered"].(float64) != 1 || rad["changed"].(float64) != 1 || rad["filled"].(float64) != 1 {
		t.Errorf("preview radius: %v", rad)
	}
	if _, err := os.Stat(ws.LocalAnnotations()); !os.IsNotExist(err) {
		t.Error("preview wrote something")
	}
	call(http.MethodPost, "/api/correct", `{"location":"src","path":"A/P1/Kicks","facet":"category","value":"one-shots","note":"all hits"}`)
	if _, err := os.Stat(filepath.Join(ws.LocalAnnotations(), "vendors", "a", "packs", "p1.toml")); err != nil {
		t.Fatal("apply must write the local pack file")
	}
	after := settle()
	if after["built"] == q["built"] {
		t.Error("the plan must rebuild after a correction (inputs changed)")
	}
	q = call(http.MethodGet, "/api/plan/queues?view=v", "")
	if rows := q["rows"].([]any); len(rows) != 1 || rows[0].(map[string]any)["folder"] != "A/P1/Noise" {
		t.Errorf("Kicks must have left the queue: %v", rows)
	}
	tr = call(http.MethodGet, "/api/plan/tree?view=v&prefix=Drums/Kick/One-Shots/P1", "")
	if tr["total"].(float64) != 2 { // the kick, and the rack that followed it
		t.Errorf("the corrected file lands in One-Shots and its rack follows: %v", tr)
	}

	// kind C: leave it — the ack takes the row out without inventing a label
	call(http.MethodPost, "/api/ack", `{"location":"src","folder":"A/P1/Noise","note":"numbered takes"}`)
	q = call(http.MethodGet, "/api/plan/queues?view=v", "")
	if rows := q["rows"].([]any); len(rows) != 0 {
		t.Errorf("acked folder must leave the queue: %v", rows)
	}
	q = call(http.MethodGet, "/api/plan/queues?view=v&acked=1", "")
	if rows := q["rows"].([]any); len(rows) != 1 || rows[0].(map[string]any)["acked"] != true {
		t.Errorf("acked=1 shows it flagged: %v", rows)
	}
	dj = call(http.MethodGet, "/api/plan/dump?view=v&format=json", "")
	if folders := dj["folders"].([]any); len(folders) != 1 || folders[0].(map[string]any)["acked"] != true {
		t.Errorf("the dump keeps an acked folder, marked: %v", folders)
	}
}

// The lexicon repeats an id to rank a second set of words lower (break
// carries "drum loop" and "beat" below "breakbeat"); the picker offers each
// id once, under its first entry, or the dropdown listed break three times.
func TestLexiconOffersEachIdOnce(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "annotations"), 0o755)
	os.WriteFile(filepath.Join(dir, "annotations", "instruments.toml"), []byte(
		"[[instrument]]\nid=\"break\"\nfamily=\"drums\"\naliases=[\"break\"]\ncategory=\"loops\"\n"+
			"[[instrument]]\nid=\"kick\"\nfamily=\"drums\"\naliases=[\"kick\"]\n"+
			"[[instrument]]\nid=\"break\"\nfamily=\"drums\"\naliases=[\"drum loop\"]\ncategory=\"loops\"\n"+
			"[[instrument]]\nid=\"drums\"\nfamily=\"drums\"\naliases=[\"drum\"]\n"+
			"[[instrument]]\nid=\"break\"\nfamily=\"drums\"\naliases=[\"beat\"]\ncategory=\"loops\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "annotations", "categories.toml"), []byte("[[category]]\nid=\"loops\"\naliases=[\"loop\"]\n"), 0o644)
	s := &Server{ws: ws}
	w := httptest.NewRecorder()
	s.lexicon(w, httptest.NewRequest(http.MethodGet, "/api/lexicon", nil))
	var got struct {
		Instruments []struct{ ID string } `json:"instruments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range got.Instruments {
		ids = append(ids, i.ID)
	}
	if want := "break kick drums"; strings.Join(ids, " ") != want {
		t.Errorf("lexicon instruments = %v, want %q", ids, want)
	}
}
