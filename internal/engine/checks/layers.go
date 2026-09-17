package checks

import (
	"fmt"
	"strings"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(Layers{}) }

// Layers declares a dependency direction for the whole repository in one rule.
//
// It exists because layering expressed as no_import pairs grows as n², and the
// pairs someone forgets to write are exactly the ones that break. Jasper's own
// layering demonstrated it: seven no_import rules named seven pairs, and every
// pair nobody thought of was unguarded — internal/scan could import
// internal/engine, two layers up, with check still green.
//
//   - layers:
//     order:
//   - "internal/model/**"
//   - ["internal/scan/**", "internal/store/**"]
//   - ["internal/engine/**", "internal/facts/**"]
//   - "internal/service/**"
//   - "internal/ports/**"
//
// Lowest tier first. Importing upward is always a violation. Within one tier,
// each glob is a peer group: files in the same group may import each other
// freely, but two different groups in the same tier may not — that is how you
// say "scan and engine both sit below service, and neither may see the other".
// Set allow_peers: true for a plain stack with no peer constraint.
//
// Files matching no tier are ignored, so a partial declaration is useful on
// day one.
//
// Note what this check cannot express: a constraint that is not about
// direction. Jasper's own engine must not import scan because the engine must
// stay pure, not because scan sits above it — and "ports must not skip past
// service" is a hop rule, not an ordering. Both are DOWNWARD imports that a
// stack permits, so both stay explicit no_import rules alongside the `layers`
// rule in .jasper/decisions/002-layering.yaml. The two are complements, not
// alternatives: `layers` guards the direction, no_import guards the rest.
type Layers struct{}

func (Layers) Kind() string { return "layers" }

func (Layers) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "order", "message", "allow_peers", "include_type_only"); err != nil {
		return nil, err
	}
	raw, ok := cfg["order"]
	if !ok {
		return nil, fmt.Errorf("missing required field %q", "order")
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be a list of layers, lowest first", "order")
	}
	if len(items) < 2 {
		return nil, fmt.Errorf("field %q needs at least two layers to express a direction", "order")
	}

	var (
		tiers  [][]*glob.Pattern
		names  []string
		groups [][]string
	)
	for i, item := range items {
		var pats []string
		switch t := item.(type) {
		case string:
			pats = []string{t}
		case []any:
			for _, e := range t {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("order[%d]: layer members must be globs", i)
				}
				pats = append(pats, s)
			}
		case []string: // a Go-literal proposal from facts
			pats = t
		default:
			return nil, fmt.Errorf("order[%d]: must be a glob or a list of globs", i)
		}
		if len(pats) == 0 {
			return nil, fmt.Errorf("order[%d]: layer has no globs", i)
		}
		if err := checkGlobs(pats...); err != nil {
			return nil, err
		}
		compiled := make([]*glob.Pattern, len(pats))
		for j, p := range pats {
			compiled[j] = glob.MustCompile(p)
		}
		tiers = append(tiers, compiled)
		groups = append(groups, pats)
		names = append(names, strings.Join(pats, " + "))
	}

	return &layersRunner{
		tiers:      tiers,
		names:      names,
		groups:     groups,
		allowPeers: optionalBool(cfg, "allow_peers", false),
		types:      optionalBool(cfg, "include_type_only", false),
		msg:        optString(cfg, "message", "layers depend downward only"),
	}, nil
}

type layersRunner struct {
	tiers      [][]*glob.Pattern
	names      []string   // one label per tier, for messages
	groups     [][]string // the raw globs, per tier, per group
	allowPeers bool
	types      bool
	msg        string
}

func (r *layersRunner) Needs() model.Capability { return model.CapImports }

// tierOf returns the lowest tier matching path and which group within that
// tier matched, or (-1, -1). Lowest wins so overlapping globs resolve
// predictably rather than by list order.
func (r *layersRunner) tierOf(path string) (tier, group int) {
	for i, pats := range r.tiers {
		for j, p := range pats {
			if p.Match(path) {
				return i, j
			}
		}
	}
	return -1, -1
}

func (r *layersRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding
	for _, im := range snap.Imports {
		if im.External() {
			continue
		}
		if im.Type && !r.types {
			continue
		}
		from, fromGroup := r.tierOf(string(im.From))
		to, toGroup := r.tierOf(string(im.To))
		if from < 0 || to < 0 {
			continue // outside the declared stack
		}
		switch {
		case to > from:
			out = append(out, model.Finding{
				File:    im.From,
				Line:    im.Line,
				Message: r.msg,
				Evidence: fmt.Sprintf("%s (layer %d: %s) imports %s (layer %d: %s)",
					im.From, from, r.names[from], im.To, to, r.names[to]),
				Hint: "Invert the dependency: move the shared type down a layer, or pass it in from above.",
			})
		case to == from && toGroup != fromGroup && !r.allowPeers:
			out = append(out, model.Finding{
				File:    im.From,
				Line:    im.Line,
				Message: r.msg,
				Evidence: fmt.Sprintf("%s and %s are peers in layer %d and must not import each other",
					r.groups[from][fromGroup], r.groups[from][toGroup], from),
				Hint: "Peers cannot see each other: move the shared piece down a layer, " +
					"or have the layer above wire them together.",
			})
		}
	}
	return out
}
