package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbarket/materialized-tunes/internal/ableton"
	"github.com/jbarket/materialized-tunes/internal/audio"
	"github.com/jbarket/materialized-tunes/internal/catalog"
	"github.com/jbarket/materialized-tunes/internal/lock"
	"github.com/jbarket/materialized-tunes/internal/plan"
)

const rackXML = `<?xml version="1.0" encoding="UTF-8"?>
<Ableton MajorVersion="5" MinorVersion="11.0_433" Creator="Ableton Live 11.3.4">
	<GroupDevicePreset>
		<FileRef>
			<RelativePathType Value="3" />
			<RelativePath Value="Kicks/Kick 01.wav" />
			<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Kicks/Kick 01.wav" />
			<Type Value="2" />
		</FileRef>
		<FileRef>
			<RelativePathType Value="1" />
			<RelativePath Value="" />
			<Path Value="/Volumes/Old/Big Pack/Snares/Snare 01.aif" />
			<Type Value="2" />
		</FileRef>
		<FileRef>
			<RelativePathType Value="3" />
			<RelativePath Value="Hats/Not Selected.wav" />
			<Path Value="C:/Users/joe/Splice/Sounds/Big Pack/Hats/Not Selected.wav" />
			<Type Value="2" />
		</FileRef>
	</GroupDevicePreset>
</Ableton>
`

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// TestCompanionEndToEnd: plan picks the rack up, materialize rewrites its
// refs to the landed paths (passthrough copies, so no ffmpeg), the lock
// pins the ref map, diff is clean, restore reproduces the bytes.
func TestCompanionEndToEnd(t *testing.T) {
	wav := make([]byte, 68+480*2*3) // 24-bit stereo 48k canonical size
	aif := []byte("aiff bytes")
	rack := ableton.Encode([]byte(rackXML))
	ws := testWorkspace(t, map[string]string{
		"Big Pack/Kicks/Kick 01.wav":     string(wav),
		"Big Pack/Snares/Snare 01.aif":   string(aif),
		"Big Pack/Hats/Not Selected.wav": string(wav),
		"Big Pack/Racks/Big Kit.adg":     string(rack),
	})
	meta := &audio.Meta{Format: "wav", Channels: 2, SampleRate: 48000, BitDepth: 24, Frames: 480, DurationS: 0.01}
	cat := map[string]catalog.Entry{}
	for p, e := range map[string]catalog.Entry{
		"Big Pack/Kicks/Kick 01.wav":     {Size: int64(len(wav)), SHA256: sha(wav), Audio: meta},
		"Big Pack/Snares/Snare 01.aif":   {Size: int64(len(aif)), SHA256: sha(aif), Audio: meta},
		"Big Pack/Hats/Not Selected.wav": {Size: int64(len(wav)), SHA256: sha(wav), Audio: meta},
		"Big Pack/Racks/Big Kit.adg":     {Size: int64(len(rack)), SHA256: sha(rack)},
	} {
		e.Path, e.ScannedAt = p, time.Now()
		cat[p] = e
	}
	if err := catalog.Write(ws.CatalogPath("src"), cat); err != nil {
		t.Fatal(err)
	}
	write := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(ws.Root, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("devices/live.toml", `name = "live"
[audio]
format = "wav"
bit_depth = 24
sample_rate = 48000
channels = "stereo"
[delivery]
mode = "staged"
[companions]
types = ["adg"]
`)
	write("storage/big.toml", "name = \"big\"\nkind = \"quota\"\ncapacity_bytes = 1073741824\n")
	write("views/samples.toml", `name = "samples"
device = "live"
storage = "big"
format_tree = "keep"
dedup = "content"
[[include]]
location = "src"
glob = "Big Pack/**"
as = "SPLICE/Big Pack"
[[exclude]]
glob = "**/Hats/**"
`)

	p, err := plan.Build(ws, "samples")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Errors) > 0 {
		t.Fatalf("plan errors: %v", p.Errors)
	}
	if p.Companions != 1 || len(p.Entries) != 3 || p.Deduped != 0 {
		t.Fatalf("plan: companions=%d entries=%d deduped=%d", p.Companions, len(p.Entries), p.Deduped)
	}
	var rackEntry plan.Entry
	for _, e := range p.Entries {
		if e.Companion {
			rackEntry = e
		}
	}
	if rackEntry.OutPath != "SPLICE/Big Pack/Racks/Big Kit.adg" {
		t.Fatalf("rack out = %q", rackEntry.OutPath)
	}

	target := t.TempDir()
	out, err := Materialize(context.Background(), ws, p, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Skipped) != 0 || out.Written != 3 {
		t.Fatalf("outcome: %+v", out)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "1 of 3 sample refs") || !strings.Contains(out.Warnings[0], "Not Selected.wav") {
		t.Fatalf("warnings: %v", out.Warnings)
	}

	got, err := os.ReadFile(filepath.Join(target, "SPLICE", "Big Pack", "Racks", "Big Kit.adg"))
	if err != nil {
		t.Fatal(err)
	}
	xmlBytes, err := ableton.Decode(got)
	if err != nil {
		t.Fatal(err)
	}
	s := string(xmlBytes)
	absRoot := strings.ReplaceAll(target, `\`, "/")
	for _, want := range []string{
		`<RelativePathType Value="5" />`,
		`<RelativePath Value="Samples/SPLICE/Big Pack/Kicks/Kick 01.wav" />`,
		`<Path Value="` + absRoot + `/SPLICE/Big Pack/Kicks/Kick 01.wav" />`,
		`<RelativePath Value="Samples/SPLICE/Big Pack/Snares/Snare 01.wav" />`, // .aif → .wav, matched by absolute-path tail
		`<RelativePath Value="Hats/Not Selected.wav" />`,                       // excluded from the recipe: untouched
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}

	l, err := lock.Read(out.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	var le lock.Entry
	for _, e := range l.Entries {
		if e.Transform.Companion {
			le = e
		}
	}
	if le.Output.SHA256 != sha(got) || len(le.Transform.Refs) != 2 ||
		le.Transform.Refs["C:/Users/joe/Splice/Sounds/Big Pack/Kicks/Kick 01.wav"] != "SPLICE/Big Pack/Kicks/Kick 01.wav" ||
		le.Transform.Refs["/Volumes/Old/Big Pack/Snares/Snare 01.aif"] != "SPLICE/Big Pack/Snares/Snare 01.wav" {
		t.Fatalf("lock entry: %+v", le)
	}

	// diff: nothing moved
	d := lock.Compute(l, p, map[string]map[string]string{"src": {
		"Big Pack/Kicks/Kick 01.wav": sha(wav), "Big Pack/Snares/Snare 01.aif": sha(aif), "Big Pack/Racks/Big Kit.adg": sha(rack),
	}})
	if !d.Clean() {
		t.Fatalf("diff not clean: %+v", d)
	}

	// restore into a fresh dir at the same path: bytes match the lock
	os.RemoveAll(target)
	os.MkdirAll(target, 0o755)
	r, err := Restore(context.Background(), ws, l, out.LockPath, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 0 || r.Written != 3 {
		t.Fatalf("restore: %+v", r)
	}
	again, _ := os.ReadFile(filepath.Join(target, "SPLICE", "Big Pack", "Racks", "Big Kit.adg"))
	if sha(again) != le.Output.SHA256 {
		t.Error("restore did not reproduce the locked bytes")
	}
}

func TestSlashRel(t *testing.T) {
	for _, c := range []struct{ dir, target, want string }{
		{"SPLICE/Pack/Racks", "SPLICE/Pack/Kicks/k.wav", "../Kicks/k.wav"},
		{"SPLICE/Pack", "SPLICE/Pack/k.wav", "k.wav"},
		{"", "Pack/k.wav", "Pack/k.wav"},
		{"A/B", "C/k.wav", "../../C/k.wav"},
	} {
		if got := slashRel(c.dir, c.target); got != c.want {
			t.Errorf("slashRel(%q,%q) = %q, want %q", c.dir, c.target, got, c.want)
		}
	}
}
