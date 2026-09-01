package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sleepunit-agents/materialized-tunes/internal/annotations"
	"github.com/sleepunit-agents/materialized-tunes/internal/harvest"
	"github.com/sleepunit-agents/materialized-tunes/internal/workspace"
)

var (
	whyLocation string
	whyJSON     bool
)

var catalogWhyCmd = &cobra.Command{
	Use:   "why <path>...",
	Short: "Explain how a file's category and instrument were resolved",
	Long: `Harvests the given catalog paths afresh from the annotations on disk and
prints, per facet, which tier answered and the exact word and path segment
it fired on — pack [[dir]] pin, vendor [[category]] glob, categories.toml
alias, pack or vendor [[instrument]] block, instruments.toml alias or code,
or nothing. The meta cache is not consulted: edit an annotation and ask
again to see the effect before re-harvesting.

Paths are relative to the location root, as "mtunes catalog samples" prints
them; backslashes are accepted. Without --location every location's catalog
is searched.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := openWorkspace()
		if err != nil {
			return err
		}
		var out []harvest.Meta
		explainers := map[string]*harvest.Explainer{} // one catalog load per location
		for _, arg := range args {
			p := strings.ReplaceAll(arg, "\\", "/")
			name, x, err := locationOf(ws, explainers, p)
			if err != nil {
				return err
			}
			m, err := x.Explain(p)
			if err != nil {
				return err
			}
			if whyJSON {
				out = append(out, m)
				continue
			}
			printWhy(name, m)
		}
		if whyJSON {
			return emitJSON(out)
		}
		return nil
	},
}

// locationOf finds the location whose catalog holds p — the --location
// flag when given, else the first catalog that lists it — loading each
// catalog at most once.
func locationOf(ws *workspace.Workspace, explainers map[string]*harvest.Explainer, p string) (string, *harvest.Explainer, error) {
	get := func(lc workspace.LocationConfig) (*harvest.Explainer, error) {
		if x, ok := explainers[lc.Name]; ok {
			return x, nil
		}
		x, err := harvest.NewExplainer(ws, lc)
		if err != nil {
			return nil, err
		}
		explainers[lc.Name] = x
		return x, nil
	}
	if whyLocation != "" {
		lc, ok := ws.Location(whyLocation)
		if !ok {
			return "", nil, fmt.Errorf("no location named %q", whyLocation)
		}
		x, err := get(lc)
		return lc.Name, x, err
	}
	for _, lc := range ws.Config.Locations {
		x, err := get(lc)
		if err != nil {
			continue
		}
		if x.Has(p) {
			return lc.Name, x, nil
		}
	}
	return "", nil, fmt.Errorf("%s: not in any location's catalog (paths are relative to the location root)", p)
}

func printWhy(location string, m harvest.Meta) {
	fmt.Printf("%s  (%s)\n", m.Path, location)
	facet := func(name, value, family string, src *annotations.Source) {
		v := value
		if v == "" {
			v = "—"
		}
		if family != "" && family != value {
			v += " (" + family + ")"
		}
		why := "nothing spoke"
		if src != nil {
			why = src.Describe()
		}
		fmt.Printf("  %-11s %-22s %s\n", name, v, why)
	}
	var cs, is *annotations.Source
	if m.Why != nil {
		cs, is = m.Why.Category, m.Why.Instrument
	}
	facet("category", m.Category, "", cs)
	facet("instrument", m.Instrument, m.Family, is)
	if m.BPM > 0 || m.Key != "" || len(m.Tags) > 0 {
		var extra []string
		if m.BPM > 0 {
			extra = append(extra, fmt.Sprintf("bpm %d", m.BPM))
		}
		if m.Key != "" {
			extra = append(extra, "key "+m.Key)
		}
		if len(m.Tags) > 0 {
			extra = append(extra, "tags "+strings.Join(m.Tags, ","))
		}
		fmt.Printf("  %-11s %s\n", "also", strings.Join(extra, "; "))
	}
}

func init() {
	f := catalogWhyCmd.Flags()
	f.StringVar(&whyLocation, "location", "", "the location the path belongs to (default: search all)")
	f.BoolVar(&whyJSON, "json", false, "machine-readable output (the harvest record with its why)")
	catalogCmd.AddCommand(catalogWhyCmd)
}
