package materialize

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/sleepunit-agents/materialized-tunes/internal/ableton"
	"github.com/sleepunit-agents/materialized-tunes/internal/plan"
	"github.com/sleepunit-agents/materialized-tunes/internal/profile"
)

// companionCtx is what a companion job needs to rewrite its sample refs:
// where every selected source landed (for Materialize, resolved against
// the plan) or the recorded ref map (for Restore), plus the device's
// anchoring rules and the absolute target.
type companionCtx struct {
	dev    profile.Companions
	target string // absolute target root, slash-separated, for the Path attribute

	// Materialize: resolve refs against the plan.
	outOf     map[string]string            // location\x00srcPath → OutPath (incl. dedup aliases)
	resolvers map[string]*ableton.Resolver // location → selected sources
}

func newCompanionCtx(p *plan.Plan, target string) *companionCtx {
	c := &companionCtx{
		dev:       p.Device.Companions,
		target:    strings.TrimSuffix(strings.ReplaceAll(target, `\`, "/"), "/"),
		outOf:     map[string]string{},
		resolvers: map[string]*ableton.Resolver{},
	}
	add := func(loc, src, out string) {
		c.outOf[loc+"\x00"+src] = out
		if c.resolvers[loc] == nil {
			c.resolvers[loc] = ableton.NewResolver(nil)
		}
		c.resolvers[loc].Add(src)
	}
	for _, e := range p.Entries {
		if !e.Companion {
			add(e.Location, e.SourcePath, e.OutPath)
		}
	}
	for k, out := range p.Aliases {
		loc, src, _ := strings.Cut(k, "\x00")
		add(loc, src, out)
	}
	return c
}

// resolve finds the selected source a reference means, from the point of
// view of a companion at srcPath in loc, and returns its output path.
// The resolution order is ableton.Resolver's — the same one plan used
// to learn what the document is made of.
func (c *companionCtx) resolve(loc, srcPath string, r ableton.Ref) (string, bool) {
	rs := c.resolvers[loc]
	if rs == nil {
		return "", false
	}
	src, ok := rs.Resolve(srcPath, r)
	if !ok {
		return "", false
	}
	out, ok := c.outOf[loc+"\x00"+src]
	return out, ok
}

// target builds the paths to write for a resolved output.
func (c *companionCtx) paths(companionOut, sampleOut string) ableton.Target {
	t := ableton.Target{}
	if c.target != "" {
		t.Abs = c.target + "/" + sampleOut
	}
	if c.dev.Anchor == "document" {
		t.Rel = slashRel(path.Dir(companionOut), sampleOut)
	} else {
		t.Rel = sampleOut
		if c.dev.UserLibraryPrefix != "" {
			t.Rel = c.dev.UserLibraryPrefix + "/" + sampleOut
		}
	}
	return t
}

// slashRel is filepath.Rel for slash paths: the path from dir to target.
func slashRel(dir, target string) string {
	if dir == "." {
		dir = ""
	}
	d := strings.Split(strings.Trim(dir, "/"), "/")
	t := strings.Split(target, "/")
	if dir == "" {
		d = nil
	}
	i := 0
	for i < len(d) && i < len(t)-1 && d[i] == t[i] {
		i++
	}
	var out []string
	for range d[i:] {
		out = append(out, "..")
	}
	return strings.Join(append(out, t[i:]...), "/")
}

// rewriteCompanion renders one companion: decode, point every resolvable
// ref at its materialized output, re-encode. refs is the ref map to
// record (Materialize) or replay (Restore): key → sample OutPath.
func rewriteCompanion(c *companionCtx, j job, src, outPath string) (map[string]string, ableton.Stats, error) {
	gz, err := os.ReadFile(src)
	if err != nil {
		return nil, ableton.Stats{}, err
	}
	xmlBytes, err := ableton.Decode(gz)
	if err != nil {
		return nil, ableton.Stats{}, err
	}
	refs := j.refs
	if refs == nil { // Materialize: resolve against the plan
		refs = map[string]string{}
		for _, r := range ableton.Refs(xmlBytes) {
			if out, ok := c.resolve(j.loc, j.srcPath, r); ok {
				refs[r.Key()] = out
			}
		}
	}
	out, st := ableton.Rewrite(xmlBytes, c.dev.PathType(), func(r ableton.Ref) (ableton.Target, bool) {
		sampleOut, ok := refs[r.Key()]
		if !ok {
			return ableton.Target{}, false
		}
		return c.paths(j.outRel, sampleOut), true
	})
	tmp := outPath + ".mtunes-part"
	if err := os.WriteFile(tmp, ableton.Encode(out), 0o644); err != nil {
		return nil, st, err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		os.Remove(tmp)
		return nil, st, err
	}
	return refs, st, nil
}

func unresolvedWarning(outRel string, st ableton.Stats) string {
	if len(st.Unresolved) == 0 {
		return ""
	}
	names := make([]string, 0, len(st.Unresolved))
	for _, k := range st.Unresolved {
		names = append(names, path.Base(strings.ReplaceAll(k, `\`, "/")))
	}
	sort.Strings(names)
	if len(names) > 4 {
		names = append(names[:4], "…")
	}
	return fmt.Sprintf("%s: %d of %d sample refs are not in this recipe, left as the pack wrote them (%s)",
		outRel, len(st.Unresolved), st.Refs, strings.Join(names, ", "))
}
