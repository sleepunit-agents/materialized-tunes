package correct

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

// LocalEntry is one assertion in the local layer, for listing and for
// reconciliation (SPEC §19.5).
type LocalEntry struct {
	File   string         `json:"file"`   // relative to annotations.local
	Vendor string         `json:"vendor"` // slug
	Pack   string         `json:"pack"`   // slug
	Kind   string         `json:"kind"`   // dir | instrument
	Entry  map[string]any `json:"entry"`
}

// List reads every [[dir]] and [[instrument]] entry in the local layer.
func List(ws *workspace.Workspace) ([]LocalEntry, error) {
	var out []LocalEntry
	root := ws.LocalAnnotations()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".toml") || d.Name() == "vendor.toml" {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		segs := strings.Split(rel, "/") // vendors/<slug>/packs/<pack>.toml
		if len(segs) != 4 {
			return nil
		}
		doc := map[string]any{}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := toml.Unmarshal(data, &doc); err != nil {
			return err
		}
		pack := strings.TrimSuffix(segs[3], ".toml")
		if pk, ok := doc["pack"].(map[string]any); ok {
			if s, ok := pk["slug"].(string); ok && s != "" {
				pack = s
			}
		}
		for _, kind := range []string{"dir", "instrument"} {
			for _, e := range tableList(doc[kind]) {
				out = append(out, LocalEntry{File: rel, Vendor: segs[1], Pack: pack, Kind: kind, Entry: e})
			}
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return out, err
}

func tableList(v any) []map[string]any {
	switch l := v.(type) {
	case []map[string]any:
		return l
	case []any:
		var out []map[string]any
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// Export zips the local layer as a submission: every vendor and pack
// file minus the entries marked local = true, plus corrections.jsonl.
// The ack list stays home — it is nothing to submit. Files left with
// no entries and no identity to contribute are dropped.
func Export(ws *workspace.Workspace) ([]byte, error) {
	root := ws.LocalAnnotations()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(files)
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "acks.jsonl" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(rel, ".toml") && filepath.Base(p) != "vendor.toml" {
			doc := map[string]any{}
			if err := toml.Unmarshal(data, &doc); err != nil {
				return nil, err
			}
			kept := 0
			for _, kind := range []string{"dir", "instrument"} {
				var list []map[string]any
				for _, e := range tableList(doc[kind]) {
					if l, _ := e["local"].(bool); l {
						continue
					}
					list = append(list, e)
				}
				if len(list) > 0 {
					doc[kind] = list
					kept += len(list)
				} else {
					delete(doc, kind)
				}
			}
			newPack := false
			if pk, ok := doc["pack"].(map[string]any); ok {
				_, newPack = pk["name"]
			}
			if kept == 0 && !newPack {
				continue
			}
			var out strings.Builder
			if err := toml.NewEncoder(&out).Encode(doc); err != nil {
				return nil, err
			}
			data = []byte(out.String())
		}
		w, err := zw.Create("annotations.local/" + rel)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
