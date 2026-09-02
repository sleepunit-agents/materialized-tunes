package plan

import (
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/catalog"
)

// A recipe's [companions] block overrides the device's for that recipe,
// in both directions: a device that drops racks can carry them for one
// recipe, and a device that carries them can drop them for one — and the
// effective block is what plan (and so materialize, the lock and migrate)
// sees, since Build applies it where the device is loaded.
func TestRecipeCompanionsOverrideDevice(t *testing.T) {
	d := &ableton.Doc{Refs: []ableton.Ref{{Rel: "../Kick.wav", Name: "Kick.wav", Type: "3"}}}
	ws := testWorkspace(t, []catalog.Entry{
		wavEntry("Kit/Kick.wav", 1, 48000, 16, 4800),
		{Path: "Kit/Racks/Clean Kit.adg", Size: 1000, SHA256: "ddkit", Doc: d},
	}, nil)
	writeProfile(t, ws, "devices/drop.toml", syntaktDevice)
	writeProfile(t, ws, "devices/carry.toml", syntaktDevice+"[companions]\ntypes = [\"adg\"]\n")
	writeProfile(t, ws, "storage/sq.toml", "name = \"sq\"\nkind = \"quota\"\ncapacity_bytes = 33554432\n")

	cases := []struct {
		name, device, block string
		wantCompanions      int
		wantAnchor          string
	}{
		{"device drops, recipe silent", "drop", "", 0, ""},
		{"device drops, recipe carries", "drop", "[companions]\ntypes = [\"adg\", \"als\"]\nanchor = \"document\"\n", 1, "document"},
		{"device carries, recipe silent", "carry", "", 1, "user-library"},
		{"device carries, recipe drops", "carry", "[companions]\ntypes = []\n", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeView(t, ws, "v", "name=\"v\"\ndevice=\""+c.device+"\"\nstorage=\"sq\"\n"+c.block+"[[include]]\nlocation=\"src\"\nglob=\"**\"\n")
			p, err := Build(ws, "v")
			if err != nil {
				t.Fatal(err)
			}
			if p.Companions != c.wantCompanions {
				t.Errorf("companions = %d, want %d", p.Companions, c.wantCompanions)
			}
			if p.Device.Companions.Anchor != c.wantAnchor {
				t.Errorf("effective anchor = %q, want %q", p.Device.Companions.Anchor, c.wantAnchor)
			}
		})
	}
}
