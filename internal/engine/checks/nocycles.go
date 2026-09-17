package checks

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(NoCycles{}) }

// NoCycles forbids circular dependencies between directories at a given depth.
// File-level cycles are common and often harmless; directory-level cycles mean
// two modules cannot be understood, tested or extracted independently.
//
//   - no_cycles:
//     within: "internal/**"
//     depth: 2
type NoCycles struct{}

func (NoCycles) Kind() string { return "no_cycles" }

func (NoCycles) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "within", "depth", "message"); err != nil {
		return nil, err
	}
	within := optString(cfg, "within", "**")
	if err := checkGlobs(within); err != nil {
		return nil, err
	}
	depth := 2
	if v, ok := cfg["depth"]; ok {
		n, ok := v.(int)
		if !ok {
			return nil, fmt.Errorf("field \"depth\" must be an integer")
		}
		if n < 1 {
			return nil, fmt.Errorf("field \"depth\" must be >= 1")
		}
		depth = n
	}
	return &cycleRunner{
		within: glob.MustCompile(within),
		depth:  depth,
		msg:    optString(cfg, "message", "circular dependency between modules"),
	}, nil
}

type cycleRunner struct {
	within *glob.Pattern
	depth  int
	msg    string
}

func (r *cycleRunner) Needs() model.Capability { return model.CapImports }

func (r *cycleRunner) Run(snap *model.Snapshot) []model.Finding {
	// Collapse the file graph to a directory graph, remembering one concrete
	// edge per pair so the finding can point at a real line.
	type edge struct {
		file model.FileID
		line int
		spec string
	}
	adj := map[string]map[string]edge{}

	for _, im := range snap.Imports {
		if im.External() || im.Type {
			continue
		}
		a, b := groupOf(string(im.From), r.depth), groupOf(string(im.To), r.depth)
		if a == b || a == "" || b == "" {
			continue
		}
		if !r.within.Match(string(im.From)) || !r.within.Match(string(im.To)) {
			continue
		}
		if adj[a] == nil {
			adj[a] = map[string]edge{}
		}
		if _, seen := adj[a][b]; !seen {
			adj[a][b] = edge{im.From, im.Line, im.Spec}
		}
	}

	var out []model.Finding
	for _, comp := range stronglyConnected(adj) {
		sort.Strings(comp)
		members := strings.Join(comp, " -> ") + " -> " + comp[0]
		inComp := map[string]bool{}
		for _, m := range comp {
			inComp[m] = true
		}
		// Report one finding per participating edge so every file in the cycle
		// is actionable, rather than one opaque finding for the whole loop.
		for _, a := range comp {
			for b, e := range adj[a] {
				if !inComp[b] {
					continue
				}
				out = append(out, model.Finding{
					File:     e.file,
					Line:     e.line,
					Message:  r.msg,
					Evidence: fmt.Sprintf("cycle: %s", members),
					Hint:     "Extract the shared piece into a package both sides may depend on, or invert one dependency.",
				})
			}
		}
	}
	return out
}

// groupOf truncates a path to its first `depth` segments.
func groupOf(p string, depth int) string {
	parts := strings.Split(path.Dir(p), "/")
	if parts[0] == "." {
		return ""
	}
	if len(parts) > depth {
		parts = parts[:depth]
	}
	return strings.Join(parts, "/")
}

// stronglyConnected is Tarjan's algorithm. Components of size > 1 are cycles.
func stronglyConnected[E any](adj map[string]map[string]E) [][]string {
	var (
		index   = map[string]int{}
		low     = map[string]int{}
		onStack = map[string]bool{}
		stack   []string
		counter int
		out     [][]string
	)
	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var strongConnect func(v string)
	strongConnect = func(v string) {
		index[v] = counter
		low[v] = counter
		counter++
		stack = append(stack, v)
		onStack[v] = true

		succ := make([]string, 0, len(adj[v]))
		for w := range adj[v] {
			succ = append(succ, w)
		}
		sort.Strings(succ)
		for _, w := range succ {
			if _, seen := index[w]; !seen {
				if _, isNode := adj[w]; !isNode {
					continue // sink: cannot participate in a cycle
				}
				strongConnect(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			if len(comp) > 1 {
				out = append(out, comp)
			}
		}
	}
	for _, n := range nodes {
		if _, seen := index[n]; !seen {
			strongConnect(n)
		}
	}
	return out
}
