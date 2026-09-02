package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepunit-agents/materialized-tunes/internal/view"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// The Recipe screen edits a recipe through /api/view only. These drive the
// same calls the vendor picker makes and read the TOML back, because the
// promise the screen makes — "collapsing changes nothing about what gets
// picked" — is a promise about this file.
func editServer(t *testing.T, recipe string) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "views"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "views", "push.toml")
	if err := os.WriteFile(path, []byte(recipe), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Server{ws: &workspace.Workspace{Root: root}}, path
}

func edit(t *testing.T, s *Server, body map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	s.viewWrite(w, httptest.NewRequest(http.MethodPost, "/api/view", strings.NewReader(string(b))))
	if w.Code != http.StatusOK {
		t.Fatalf("%v → %d %s", body["action"], w.Code, w.Body.String())
	}
}

const threePackRecipe = `name    = "push"
device  = "push3"
storage = "cargo"

# added from the library: Grit
[[include]]
location = "splice"
glob     = "Grit/**"
as       = "SPLICE/Grit"

[[include]]
location = "splice"
glob     = "Junkie Kid/**"
as       = "SPLICE/Junkie Kid"

# hand-written, another location entirely
[[include]]
location = "archive"
glob     = "Goldbaby/**"
`

// "collapse to 1": three rules become one, and the neighbouring location's
// hand-written rule and its comment are untouched.
func TestCollapseGroupToOneRule(t *testing.T) {
	s, path := editServer(t, threePackRecipe)
	edit(t, s, map[string]any{"action": "add-rule", "name": "push", "location": "splice",
		"glob": "**", "as": "SPLICE", "replace_location": true, "replace_prefix": "", "note": "all of splice"})

	v, err := view.Load(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Include) != 2 {
		t.Fatalf("want 2 rules, got %d", len(v.Include))
	}
	if v.Include[0].Location != "archive" || v.Include[1].Glob != "**" || v.Include[1].As != "SPLICE" {
		t.Fatalf("wrong rules left: %+v", v.Include)
	}
	src, _ := os.ReadFile(path)
	if !strings.Contains(string(src), "# hand-written, another location entirely") {
		t.Errorf("hand-written comment lost:\n%s", src)
	}
}

// Unchecking a vendor whose rules are its own removes them in one write,
// however the indexes are ordered.
func TestUncheckGroupRemovesItsRules(t *testing.T) {
	s, _ := editServer(t, threePackRecipe)
	edit(t, s, map[string]any{"action": "remove-rules", "name": "push", "indices": []int{1, 0}})

	v, err := view.Load(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Include) != 1 || v.Include[0].Location != "archive" {
		t.Fatalf("want only the archive rule, got %+v", v.Include)
	}
}

// A pack carved out of a whole-vendor rule and put back: the rule stays
// whole through both, and the exclude leaves no residue.
func TestExcludeRoundTrip(t *testing.T) {
	s, path := editServer(t, `name    = "push"
device  = "push3"
storage = "cargo"

[[include]]
location = "splice"
glob     = "**"
as       = "SPLICE"
`)
	edit(t, s, map[string]any{"action": "add-exclude", "name": "push",
		"exclude_glob": "Grit/**", "note": "carved out of a wider rule: Grit"})
	edit(t, s, map[string]any{"action": "add-exclude", "name": "push", "exclude_glob": "Grit/**"}) // idempotent

	v, err := view.Load(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Include) != 1 || len(v.Exclude) != 1 || v.Exclude[0].Glob != "Grit/**" {
		t.Fatalf("after carve: %+v / %+v", v.Include, v.Exclude)
	}

	edit(t, s, map[string]any{"action": "remove-exclude", "name": "push", "exclude_glob": "Grit/**"})
	if v, err = view.Load(s.ws.Root, "push"); err != nil {
		t.Fatal(err)
	}
	if len(v.Include) != 1 || len(v.Exclude) != 0 {
		t.Fatalf("after uncarve: %+v / %+v", v.Include, v.Exclude)
	}
	if src, _ := os.ReadFile(path); strings.Contains(string(src), "exclude") {
		t.Errorf("exclude residue left:\n%s", src)
	}
}

// Unchecking the last vendor empties the recipe. That is a legitimate state
// the picker shows you — it must not come back as "no longer parses".
func TestEmptyingARecipeIsAllowed(t *testing.T) {
	s, _ := editServer(t, threePackRecipe)
	edit(t, s, map[string]any{"action": "remove-rules", "name": "push", "indices": []int{0, 1, 2}})

	if _, err := view.LoadRaw(s.ws.Root, "push"); err != nil {
		t.Fatalf("LoadRaw on an emptied recipe: %v", err)
	}
	if _, err := view.Load(s.ws.Root, "push"); err == nil {
		t.Error("Load must still refuse a recipe with no rules — materializing one selects nothing")
	}
}

// The recipe head's device and storage selects rewrite one key each and
// refuse a profile that doesn't exist — the rest of the file, comments
// and rules included, comes through untouched.
func TestRecipeSetDeviceAndStorage(t *testing.T) {
	s, path := editServer(t, threePackRecipe)
	for _, d := range []string{"devices", "storage"} {
		if err := os.MkdirAll(filepath.Join(s.ws.Root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(s.ws.Root, "devices", "op1.toml"), []byte("name = \"op1\"\n"), 0o644)
	os.WriteFile(filepath.Join(s.ws.Root, "storage", "sd.toml"), []byte("name = \"sd\"\n"), 0o644)

	edit(t, s, map[string]any{"action": "set-device", "name": "push", "device": "op1"})
	edit(t, s, map[string]any{"action": "set-storage", "name": "push", "storage": "sd"})
	v, err := view.LoadRaw(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if v.Device != "op1" || v.Storage != "sd" {
		t.Errorf("device/storage = %q/%q, want op1/sd", v.Device, v.Storage)
	}
	if len(v.Include) != 3 {
		t.Errorf("rules = %d, want 3 — a scalar edit must not touch the blocks", len(v.Include))
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "# hand-written, another location entirely") {
		t.Error("comments must survive a scalar edit")
	}

	// A profile that isn't on disk is refused, and the file stays put.
	b, _ := json.Marshal(map[string]any{"action": "set-device", "name": "push", "device": "ghost"})
	w := httptest.NewRecorder()
	s.viewWrite(w, httptest.NewRequest(http.MethodPost, "/api/view", strings.NewReader(string(b))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown device → %d, want 400: %s", w.Code, w.Body.String())
	}
	if v, _ := view.LoadRaw(s.ws.Root, "push"); v.Device != "op1" {
		t.Errorf("a refused edit must leave device = op1, got %q", v.Device)
	}
}

// The Edit form saves the recipe head in one write — every scalar plus
// the [companions] override — and the file it leaves is one the loader
// reads back with exactly those values, with the rule blocks and the
// hand-written comments untouched.
func TestRecipeSetOptionsRoundTrip(t *testing.T) {
	recipe := `# push — hand-tuned, keep the comments
name    = "push"
device  = "push3"
storage = "cargo"
# the target is a staging folder
target  = "~/stage"
limit   = 40

# racks ride along on this one
[companions]
types = ["adg"]

# added from the library: Grit
[[include]]
location = "splice"
glob     = "Grit/**"
as       = "SPLICE/Grit"
`
	s, path := editServer(t, recipe)
	for _, d := range []string{"devices", "storage"} {
		os.MkdirAll(filepath.Join(s.ws.Root, d), 0o755)
	}
	os.WriteFile(filepath.Join(s.ws.Root, "devices", "push3.toml"), []byte("name = \"push3\"\n"), 0o644)
	os.WriteFile(filepath.Join(s.ws.Root, "devices", "op1.toml"), []byte("name = \"op1\"\n"), 0o644)
	os.WriteFile(filepath.Join(s.ws.Root, "storage", "cargo.toml"), []byte("name = \"cargo\"\n"), 0o644)

	// Override every knob, replace the companions block.
	edit(t, s, map[string]any{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo",
		"target": "", "layout": "{family}/{pack}/{file}", "limit": 0, "format_tree": "keep", "dedup": "content",
		"cuts": "all", "vendor_prep": "keep",
		"companions": map[string]any{"types": []string{"ADG", ".als"}, "anchor": "user-library", "user_library_prefix": "/Samples/Push/"}})
	v, err := view.LoadRaw(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if v.Device != "op1" || v.Storage != "cargo" || v.Target != "" || v.Layout != "{family}/{pack}/{file}" || v.Limit != 0 ||
		v.FormatTree != "keep" || v.Dedup != "content" || v.Cuts != "all" || v.VendorPrep != "keep" {
		t.Errorf("head after save: %+v", *v)
	}
	if v.Companions == nil || strings.Join(v.Companions.Types, ",") != "adg,als" || v.Companions.Anchor != "user-library" || v.Companions.UserLibraryPrefix != "Samples/Push" {
		t.Errorf("companions after save: %+v", v.Companions)
	}
	b, _ := os.ReadFile(path)
	src := string(b)
	for _, want := range []string{"# push — hand-tuned, keep the comments", "# added from the library: Grit", "as       = \"SPLICE/Grit\""} {
		if !strings.Contains(src, want) {
			t.Errorf("lost %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "limit") || strings.Contains(src, "target") && !strings.Contains(src, "# the target") {
		t.Errorf("a knob set back to its default must lose its line:\n%s", src)
	}
	if strings.Count(src, "[companions]") != 1 || strings.Index(src, "[companions]") > strings.Index(src, "[[include]]") {
		t.Errorf("one [companions] block, ahead of the rules:\n%s", src)
	}

	// Back to "the device decides": the block goes, nothing else moves.
	edit(t, s, map[string]any{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo",
		"layout": "{family}/{pack}/{file}", "format_tree": "keep", "dedup": "content", "cuts": "all", "vendor_prep": "keep", "companions": nil})
	v, err = view.LoadRaw(s.ws.Root, "push")
	if err != nil {
		t.Fatal(err)
	}
	if v.Companions != nil {
		t.Errorf("companions should be nil after inherit, got %+v", v.Companions)
	}
	if len(v.Include) != 1 || v.Dedup != "content" {
		t.Errorf("inherit must only remove the block: %+v", *v)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "[companions]") || !strings.Contains(string(b), "\n\n# added from the library: Grit") {
		t.Errorf("block removal must leave the rules, their comment, and one blank line above:\n%s", b)
	}

	// Refusals leave the file alone.
	for _, bad := range []map[string]any{
		{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo", "format_tree": "sideways"},
		{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo", "companions": map[string]any{"types": []string{"alp"}}},
		{"action": "set-options", "name": "push", "device": "ghost", "storage": "cargo"},
		{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo", "layout": "{nonsense}/{file}"},
		{"action": "set-options", "name": "push", "device": "op1", "storage": "cargo", "limit": -3},
	} {
		bb, _ := json.Marshal(bad)
		w := httptest.NewRecorder()
		s.viewWrite(w, httptest.NewRequest(http.MethodPost, "/api/view", strings.NewReader(string(bb))))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%v → %d, want 400: %s", bad, w.Code, w.Body.String())
		}
	}
	if v2, _ := view.LoadRaw(s.ws.Root, "push"); v2.FormatTree != "keep" || v2.Companions != nil {
		t.Errorf("a refused save must not write: %+v", *v2)
	}
}
