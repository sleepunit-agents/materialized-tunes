package plan

import (
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
)

// sfmPrepPlan builds a plan over one SFM pack, with extra recipe lines.
func sfmPrepPlan(t *testing.T, entries []catalog.Entry, extra ...string) *Plan {
	t.Helper()
	ws := testWorkspace(t, entries, sfmAnnotations)
	ws.Config.Locations[0].Layout = "vendor-dirs"
	if err := ws.SaveConfig(); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, ws, "devices/dev.toml", device(24, 44100, "stereo"))
	writeProfile(t, ws, "storage/sq.toml", bigStorage)
	writeView(t, ws, "v", "name=\"v\"\ndevice=\"dev\"\nstorage=\"sq\"\n"+strings.Join(extra, "\n")+
		"\n[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
	p, err := Build(ws, "v")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// agogo727 is the shape of a real SFM pack: the canonical WAV tree holding
// the hits twice (clean and tape), and two sampler trees holding the same
// hits again under the host's own folder names.
func agogo727() []catalog.Entry {
	const dir = "Samples From Mars/727 From Mars/"
	return []catalog.Entry{
		wavEntry(dir+"WAV/01. Clean/Agogo Hi 727 25.wav", 2, 44100, 24, 44100),
		wavEntry(dir+"WAV/02. Tape/Agogo Hi 727 25.wav", 2, 44100, 24, 44100),
		wavEntry(dir+"Battery/727 From Mars/Assorted 1 Samples/Agogo Hi 727 25.wav", 2, 44100, 16, 22050),
		wavEntry(dir+"Maschine/727 From Mars/Assorted 1 Samples/Agogo Hi 727 25.wav", 2, 44100, 16, 21000),
	}
}

// TestVendorPrepSkipsSamplerTrees: Jonathan's call — SFM's per-host
// re-exports are the vendor's device prep, not content, and mtunes does
// that job itself. Both sampler trees leave; the canonical tree stays
// whole, both of its folders intact.
func TestVendorPrepSkipsSamplerTrees(t *testing.T) {
	p := sfmPrepPlan(t, agogo727())
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if p.VendorPrepSkipped != 2 {
		t.Errorf("vendor_prep_skipped = %d, want 2", p.VendorPrepSkipped)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("%d entries, want the 2 WAV cuts: %v", len(p.Entries), outPaths(p))
	}
	for _, e := range p.Entries {
		if !strings.Contains(e.SourcePath, "/WAV/") {
			t.Errorf("%q survived — only the canonical tree should", e.SourcePath)
		}
	}
	// Nothing left for the cut resolver to adjudicate: the two WAV files
	// are different folders of one tree, which was never a cut set.
	if p.CutsDropped != 0 {
		t.Errorf("cuts_dropped = %d, want 0 — the sampler trees left before scoring", p.CutsDropped)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "sampler exports") || !strings.Contains(w, `"Battery" (1)`) {
		t.Errorf("warning should tally the skipped trees, got: %v", p.Warnings)
	}
	if !strings.Contains(w, "Every skipped name has a same-named file") {
		t.Errorf("a clean swap should say so, got: %v", p.Warnings)
	}
}

// TestVendorPrepNeedsCanonicalTree: skipping is a swap, never a
// subtraction. A pack whose canonical tree is not in the selection keeps
// its sampler trees, and the cut resolver handles them as before.
func TestVendorPrepNeedsCanonicalTree(t *testing.T) {
	p := sfmPrepPlan(t, agogo727()[2:]) // Battery and Maschine only
	if p.VendorPrepSkipped != 0 {
		t.Fatalf("skipped %d — with no canonical tree there is nothing to swap in", p.VendorPrepSkipped)
	}
	if len(p.Entries) != 1 || p.CutsDropped != 1 {
		t.Fatalf("%d entries, %d cuts dropped — want the cut resolver to keep the longest", len(p.Entries), p.CutsDropped)
	}
	if !strings.Contains(p.Entries[0].SourcePath, "Battery") {
		t.Errorf("kept %q, want the longer Battery render", p.Entries[0].SourcePath)
	}
}

// TestVendorPrepScopedToPack: one pack having a canonical tree says
// nothing about its neighbour. The pack that has one gets swapped; the
// pack that does not keeps everything.
func TestVendorPrepScopedToPack(t *testing.T) {
	p := sfmPrepPlan(t, []catalog.Entry{
		wavEntry("Samples From Mars/727 From Mars/WAV/01. Clean/Agogo.wav", 2, 44100, 24, 44100),
		wavEntry("Samples From Mars/727 From Mars/Battery/Kit/Agogo.wav", 2, 44100, 16, 22050),
		wavEntry("Samples From Mars/909 From Mars/Battery/Kit/Rim.wav", 2, 44100, 16, 22050),
	})
	if p.VendorPrepSkipped != 1 {
		t.Fatalf("skipped %d, want 1 — 909 has no canonical tree of its own", p.VendorPrepSkipped)
	}
	got := outPaths(p)
	if len(got) != 2 {
		t.Fatalf("entries %v, want the 727 WAV hit and the 909 Battery hit", got)
	}
	for _, e := range p.Entries {
		if strings.Contains(e.SourcePath, "727 From Mars/Battery") {
			t.Errorf("727's Battery copy survived: %q", e.SourcePath)
		}
	}
}

// TestVendorPrepNamesUnmatchedSkips: the swap cannot prove every sampler
// hit exists under the canonical tree — the trees are named differently
// and every byte differs. So it reports what it could not match instead
// of implying it checked.
func TestVendorPrepNamesUnmatchedSkips(t *testing.T) {
	p := sfmPrepPlan(t, []catalog.Entry{
		wavEntry("Samples From Mars/727 From Mars/WAV/01. Clean/Agogo.wav", 2, 44100, 24, 44100),
		wavEntry("Samples From Mars/727 From Mars/Battery/Kit/Agogo.wav", 2, 44100, 16, 22050),
		wavEntry("Samples From Mars/727 From Mars/Battery/Kit/Quijada.wav", 2, 44100, 16, 22050),
	})
	if p.VendorPrepSkipped != 2 {
		t.Fatalf("skipped %d, want 2", p.VendorPrepSkipped)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "1 skipped name has no same-named file") || !strings.Contains(w, "Quijada.wav") {
		t.Errorf("the unmatched skip should be named, got: %v", p.Warnings)
	}
}

// TestVendorPrepKeep: the opt-out leaves every tree in and hands the
// question back to the cut resolver, exactly as before this existed —
// and shows why the cut resolver was never going to be enough. It only
// ever sees a collision, and a sampler tree collides with its siblings,
// not with the canonical tree: SFM files "Agogo Hi 727 25" under
// "01. Clean" and under "Assorted 1 Samples", so the surviving Battery
// copy lands beside the WAV ones rather than on top of them. Three
// entries for one hit, which is the residue Jonathan asked to be rid of.
func TestVendorPrepKeep(t *testing.T) {
	p := sfmPrepPlan(t, agogo727(), `vendor_prep="keep"`)
	if p.VendorPrepSkipped != 0 {
		t.Fatalf("skipped %d with vendor_prep = keep", p.VendorPrepSkipped)
	}
	if len(p.Entries) != 3 || p.CutsDropped != 1 {
		t.Fatalf("%d entries, %d cuts dropped — want 3 and 1: only Battery and Maschine ever collide", len(p.Entries), p.CutsDropped)
	}
}

// TestVendorPrepLeavesCutVendorsAlone: Polyend's parallel trees are cuts
// of one render — content somebody's 16-bit tracker wants — and `cuts`
// decides those. Only a vendor that declares parallel_role = "reexport"
// is reached at all.
func TestVendorPrepLeavesCutVendorsAlone(t *testing.T) {
	p := cutPlan(t, device(24, 44100, "stereo"))
	if p.VendorPrepSkipped != 0 {
		t.Errorf("skipped %d — a cut vendor's trees are not device prep", p.VendorPrepSkipped)
	}
	if p.CutsDropped != 2 || len(p.Entries) != 1 {
		t.Errorf("%d entries, %d cuts dropped — the cut resolver still owns this", len(p.Entries), p.CutsDropped)
	}
}

func outPaths(p *Plan) []string {
	out := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.OutPath
	}
	return out
}
