package harvest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/workspace"
)

func TestHarvest(t *testing.T) {
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
	write("annotations/vendors/bmt/vendor.toml", "[vendor]\nname=\"Blu Mar Ten\"\nslug=\"blu-mar-ten\"\n[naming]\nkey_suffix=\" - <camelot>\"\n[[category]]\nid=\"loops\"\nmatch=[\"Breaks\"]\n")
	write("annotations/vendors/bmt/packs/jj.toml", "[pack]\nname=\"JJ\"\nslug=\"jj\"\ndir=\"JJ\"\n[[dir]]\npath=\"Bass\"\ncategory=\"one-shots\"\ntags=[\"bass\",\"sub\"]\n")
	write("annotations/vendors/zg/vendor.toml", "[vendor]\nname=\"Zero-G\"\nslug=\"zero-g\"\n[naming]\nbpm_dir_suffix=true\n[[category]]\nid=\"loops\"\nmatch=[\"Bass Lines*\"]\n")
	write("annotations/vendors/sfm/vendor.toml", "[vendor]\nname=\"Samples From Mars\"\nslug=\"samples-from-mars\"\n[naming]\nnote_suffix=\"_<note><octave>\"\n[[category]]\nid=\"one-shots\"\nmatch=[\"*Individual Hits*\"]\n[[category]]\nid=\"multisamples\"\nmatch=[\"*Synths*\"]\n")

	mk := func(path, sha string) catalog.Entry {
		return catalog.Entry{Path: path, SHA256: sha, Size: 1, ScannedAt: time.Now(),
			Audio: &audio.Meta{Format: "wav", Channels: 1, SampleRate: 44100, BitDepth: 16, Frames: 10}}
	}
	cat := map[string]catalog.Entry{}
	for _, e := range []catalog.Entry{
		mk("Blu Mar Ten/JJ/Bass/Champion Sub - 10A.wav", "s1"),
		mk("Blu Mar Ten/JJ/Breaks/Amen Cut.wav", "s2"),
		mk("Zero-G/Jungle Warfare Vol 2/Bass Lines 166.5/808 Standard 1 (JW2).wav", "s3"),
		mk("Samples From Mars/2600 From Mars/WAV/Synths/37_ItsASurprise_2600_C#1.wav", "s4"),
		mk("Samples From Mars/808 From Mars/WAV/01. Individual Hits/BD 01.wav", "s5"),
		mk("Elektron/OT/Loops/Hat Loop 03 124 Bpm.wav", "s6"), // unknown vendor: generic bpm still works
		mk("Nobody/Pack/x.wav", "s7"),                         // nothing to say
	} {
		cat[e.Path] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	ws, _ = workspace.Load(dir)
	res, err := Run(ws, ws.Config.Locations[0])
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 6 {
		t.Errorf("files with metadata = %d, want 6", res.Files)
	}
	got := map[string]Meta{}
	f, _ := os.Open(filepath.Join(dir, "annotations-cache", "meta", "src.jsonl"))
	defer f.Close()
	dec := jsonDecoder(f)
	for {
		var m Meta
		if dec.Decode(&m) != nil {
			break
		}
		got[m.SHA] = m
	}
	check := func(sha, key string, bpm int, cat string, tags ...string) {
		m := got[sha]
		if m.Key != key || m.BPM != bpm || m.Category != cat {
			t.Errorf("%s: got key=%q bpm=%d cat=%q, want %q %d %q", sha, m.Key, m.BPM, m.Category, key, bpm, cat)
		}
		if len(tags) > 0 && len(m.Tags) != len(tags) {
			t.Errorf("%s: tags %v want %v", sha, m.Tags, tags)
		}
	}
	check("s1", "Bm", 0, "one-shots", "bass", "sub") // camelot 10A → B minor; pack [[dir]] governs
	check("s2", "", 0, "loops")                      // vendor [[category]]
	check("s3", "", 167, "loops")                    // bpm from dir suffix (166.5 → 167)
	check("s4", "C#1", 0, "multisamples")            // SFM note suffix
	check("s5", "", 0, "one-shots")                  // "01. Individual Hits" via glob
	check("s6", "", 124, "")                         // literal "124 Bpm", no vendor
	if _, ok := got["s7"]; ok {
		t.Error("s7 has nothing to harvest and must be absent")
	}
}

func jsonDecoder(f *os.File) interface{ Decode(any) error } { return json.NewDecoder(f) }
