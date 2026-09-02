package ableton

import (
	"path"
	"strings"
)

// Doc is what the catalog records about a companion document: the sample
// references it carries, as written. Parsed once at scan time, so plan
// can ask "what does this rack point at" without opening the file — the
// catalog stays the only input a plan reads.
type Doc struct {
	Refs []Ref `json:"refs"`
}

// ParseDoc decodes a gzipped Live document and lists its references —
// each distinct one once, in first-seen order. A set that plays one kick
// in forty clips references it forty times; the catalog wants to know
// the kick is in it, not how many times, and the entry is one JSONL line
// that has to be read back on every launch.
func ParseDoc(gz []byte) (*Doc, error) {
	xmlBytes, err := Decode(gz)
	if err != nil {
		return nil, err
	}
	d := &Doc{Refs: []Ref{}} // present-but-empty survives JSON, nil would not
	seen := map[string]bool{}
	for _, r := range Refs(xmlBytes) {
		if k := r.Key(); !seen[k] {
			seen[k] = true
			d.Refs = append(d.Refs, r)
		}
	}
	return d, nil
}

// Resolver maps a document's references onto a set of known source paths
// (slash-separated, relative to one location root). It is the one
// definition of "which file does this ref mean" — plan uses it against
// the whole catalog to learn what a rack is made of, materialize against
// the selected sources to rewire it.
//
// Order: the relative path as written, anchored at the document's dir
// and each parent; the absolute path's longest tail that is a known
// path; a basename unique within the nearest enclosing dir. Ambiguous at
// the nearest level = unresolved — guessing would wire a wrong pad.
type Resolver struct {
	known  map[string]bool
	byName map[string][]string // lower(basename) → paths
}

// NewResolver indexes the paths a reference may resolve to.
func NewResolver(paths []string) *Resolver {
	r := &Resolver{known: make(map[string]bool, len(paths)), byName: map[string][]string{}}
	for _, p := range paths {
		r.Add(p)
	}
	return r
}

// Add indexes one more path.
func (rs *Resolver) Add(p string) {
	if rs.known[p] {
		return
	}
	rs.known[p] = true
	k := strings.ToLower(path.Base(p))
	rs.byName[k] = append(rs.byName[k], p)
}

// Resolve finds the known path a reference means from the point of view
// of a document at docPath.
func (rs *Resolver) Resolve(docPath string, r Ref) (string, bool) {
	dir := path.Dir(docPath)
	if dir == "." {
		dir = ""
	}
	parents := []string{dir}
	for d := dir; d != "" && d != "."; {
		d = path.Dir(d)
		if d == "." {
			d = ""
		}
		parents = append(parents, d)
	}

	if rel := strings.ReplaceAll(r.Rel, `\`, "/"); rel != "" {
		for _, base := range parents {
			cand := path.Clean(path.Join(base, rel))
			if strings.HasPrefix(cand, "../") {
				continue
			}
			if rs.known[cand] {
				return cand, true
			}
		}
	}

	names := rs.byName[strings.ToLower(r.Name)]
	if len(names) == 0 {
		return "", false
	}
	if abs := strings.ReplaceAll(r.Abs, `\`, "/"); abs != "" {
		best, bestLen := "", 0
		for _, src := range names {
			if len(src) > bestLen && strings.HasSuffix(strings.ToLower("/"+abs), strings.ToLower("/"+src)) {
				best, bestLen = src, len(src)
			}
		}
		if best != "" {
			return best, true
		}
	}
	for _, base := range parents {
		var hits []string
		for _, src := range names {
			if base == "" || strings.HasPrefix(src, base+"/") {
				hits = append(hits, src)
			}
		}
		if len(hits) == 1 {
			return hits[0], true
		}
		if len(hits) > 1 {
			return "", false // ambiguous at the nearest level
		}
	}
	return "", false
}
