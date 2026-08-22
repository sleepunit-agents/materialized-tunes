// Package ableton rewrites the sample references inside Ableton Live
// companion files (.adg drum racks, .adv presets, .als sets) so they point
// at where mtunes put the samples. The files are gzipped XML; every sample
// lives in a <FileRef> block that carries the path the pack author had.
// We edit those blocks textually — never re-serialize the document — so
// everything Live wrote stays byte-identical except the paths we own.
//
// Two FileRef dialects exist:
//
//	Live 11+:  <RelativePathType Value="3"/> <RelativePath Value="a/b.wav"/> <Path Value="C:/abs/a/b.wav"/>
//	Live ≤10:  <RelativePathType Value="3"/> <RelativePath><RelativePathElement Dir="a"/></RelativePath> <Name Value="b.wav"/>
//
// RelativePathType is how Live anchors the relative path: 3 = relative to
// the document (set/preset), 5 = relative to the User Library, 6 = a Live
// Pack. For a library materialized into <User Library>/<prefix> we write
// type 5 so the rack resolves on any machine (or Push) that holds the
// User Library, and the absolute Path as a courtesy for the machine that
// ran materialize.
package ableton

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Extensions that are companions — gzipped Live XML that references
// samples. .alp is a pack installer archive, not a document; excluded.
var Extensions = map[string]bool{"adg": true, "adv": true, "als": true}

// IsCompanion reports whether a path's extension is a supported companion.
func IsCompanion(p string) bool {
	return Extensions[strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))]
}

// Ref is one sample reference as the file carries it.
type Ref struct {
	Rel  string // slash-joined relative path as written (may be empty)
	Abs  string // absolute path as written (Live 11+; may be empty)
	Name string // basename, from Abs, Rel or <Name>
	Type string // RelativePathType value as written
}

// Key is the identity of a reference for lock records: the most specific
// path the file gave us.
func (r Ref) Key() string {
	if r.Abs != "" {
		return r.Abs
	}
	if r.Rel != "" {
		return r.Rel
	}
	return r.Name
}

// Stats summarizes a rewrite.
type Stats struct {
	Refs       int      // FileRef blocks that reference a file
	Rewritten  int      // references pointed at a materialized output
	Unresolved []string // references left untouched (original key)
}

// Target is where a reference should now point.
type Target struct {
	Rel string // relative path for the chosen anchor (already prefixed, slash-separated)
	Abs string // absolute path on the machine running materialize, slash-separated ("" leaves Path alone)
}

var (
	fileRefRe  = regexp.MustCompile(`(?s)<FileRef>.*?</FileRef>`)
	valueRe    = func(tag string) *regexp.Regexp { return regexp.MustCompile(`<` + tag + ` Value="([^"]*)"\s*/>`) }
	relTypeRe  = valueRe("RelativePathType")
	relPathRe  = valueRe("RelativePath")
	absPathRe  = valueRe("Path")
	nameRe     = valueRe("Name")
	relBlockRe = regexp.MustCompile(`(?s)<RelativePath>(.*?)</RelativePath>`)
	relElemRe  = regexp.MustCompile(`<RelativePathElement(?: Id="\d+")? Dir="([^"]*)"\s*/>`)
	hintRe     = regexp.MustCompile(`(?s)<SearchHint>.*?</SearchHint>`)
	pathHintRe = regexp.MustCompile(`(?s)<PathHint>(.*?)</PathHint>`)
	hasRelRe   = valueRe("HasRelativePath")
)

// Decode gunzips a companion file.
func Decode(gz []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("not a gzipped Live document: %w", err)
	}
	defer r.Close()
	xmlBytes, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(xmlBytes[:min(len(xmlBytes), 512)], []byte("<Ableton")) {
		return nil, fmt.Errorf("gzipped, but not an Ableton document")
	}
	return xmlBytes, nil
}

// Encode gzips XML deterministically (no mtime, no name) so the same
// input always hashes the same — the lock pins the output bytes.
func Encode(xmlBytes []byte) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	w.Write(xmlBytes)
	w.Close()
	return buf.Bytes()
}

// Refs lists the sample references in a decoded document, in order.
func Refs(xmlBytes []byte) []Ref {
	var refs []Ref
	for _, block := range fileRefRe.FindAll(xmlBytes, -1) {
		if r, ok := parseRef(block); ok {
			refs = append(refs, r)
		}
	}
	return refs
}

func parseRef(block []byte) (Ref, bool) {
	var r Ref
	if m := relTypeRe.FindSubmatch(block); m != nil {
		r.Type = string(m[1])
	}
	if m := absPathRe.FindSubmatch(block); m != nil {
		r.Abs = unescape(string(m[1]))
	}
	if m := relPathRe.FindSubmatch(block); m != nil {
		r.Rel = unescape(string(m[1]))
	} else if m := relBlockRe.FindSubmatch(block); m != nil {
		var dirs []string
		for _, e := range relElemRe.FindAllSubmatch(m[1], -1) {
			dirs = append(dirs, unescape(string(e[1])))
		}
		if m := nameRe.FindSubmatch(block); m != nil {
			r.Name = unescape(string(m[1]))
			r.Rel = strings.Join(append(dirs, r.Name), "/")
		}
	}
	if r.Name == "" {
		switch {
		case r.Abs != "":
			r.Name = path.Base(strings.ReplaceAll(r.Abs, `\`, "/"))
		case r.Rel != "":
			r.Name = path.Base(r.Rel)
		}
	}
	if r.Name == "" || r.Name == "." || r.Name == "/" {
		return r, false // folder refs, empty slots
	}
	return r, true
}

// Rewrite points every resolvable reference at its target. resolve
// returns the new location for a reference, or ok=false to leave it as
// written. pathType is the RelativePathType to write for rewritten refs.
func Rewrite(xmlBytes []byte, pathType string, resolve func(Ref) (Target, bool)) ([]byte, Stats) {
	var st Stats
	out := fileRefRe.ReplaceAllFunc(xmlBytes, func(block []byte) []byte {
		r, ok := parseRef(block)
		if !ok {
			return block
		}
		st.Refs++
		t, ok := resolve(r)
		if !ok {
			st.Unresolved = append(st.Unresolved, r.Key())
			return block
		}
		st.Rewritten++
		return rewriteBlock(block, r, t, pathType)
	})
	return out, st
}

func rewriteBlock(block []byte, r Ref, t Target, pathType string) []byte {
	rel := strings.Trim(t.Rel, "/")
	dir, name := path.Split(rel)
	b := block
	b = setValue(b, relTypeRe, "RelativePathType", pathType)
	if relPathRe.Match(b) {
		// Live 11+
		b = setValue(b, relPathRe, "RelativePath", rel)
		if t.Abs != "" {
			b = setValue(b, absPathRe, "Path", t.Abs)
		}
		return b
	}
	// Live ≤10: element list + Name, plus the search hint's path
	var sb strings.Builder
	sb.WriteString("<RelativePath>")
	for i, d := range strings.Split(strings.Trim(dir, "/"), "/") {
		if d == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n\t\t\t\t\t<RelativePathElement Id=\"%d\" Dir=\"%s\" />", i, escape(d))
	}
	sb.WriteString("\n\t\t\t\t</RelativePath>")
	b = relBlockRe.ReplaceAll(b, []byte(strings.ReplaceAll(sb.String(), "$", "$$")))
	b = setValue(b, nameRe, "Name", name)
	b = setValue(b, hasRelRe, "HasRelativePath", "true")
	b = hintRe.ReplaceAllFunc(b, func(h []byte) []byte {
		return pathHintRe.ReplaceAll(h, []byte("<PathHint />"))
	})
	return b
}

func setValue(b []byte, re *regexp.Regexp, tag, v string) []byte {
	repl := `<` + tag + ` Value="` + escape(v) + `" />`
	if loc := re.FindIndex(b); loc != nil {
		return append(append(append([]byte{}, b[:loc[0]]...), repl...), b[loc[1]:]...)
	}
	return b
}

var (
	escaper   = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	unescaper = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'")
)

func escape(s string) string   { return escaper.Replace(s) }
func unescape(s string) string { return unescaper.Replace(s) }
