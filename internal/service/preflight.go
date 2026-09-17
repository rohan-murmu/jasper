package service

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

// Verdict answers "may I do this?" for a change that has not happened yet.
//
// Only findings the hypothetical change *introduces* are returned. A repo with
// pre-existing violations must not answer "denied" to every question, or the
// agent learns the tool is noise and stops calling it.
type Verdict struct {
	Allowed  bool            `json:"allowed"`
	Findings []model.Finding `json:"findings,omitempty"`
	Existing int             `json:"existing_violations"`
	Checks   int             `json:"checks"`
	Note     string          `json:"note,omitempty"`

	// Present reports that the change is already in the tree. Combined with
	// Allowed == false it means this is a violation happening right now, not a
	// hypothetical one — the honest answer to "may I do this?" when the answer
	// is "you already did".
	Present bool `json:"present"`
}

// preflight runs every compiled check twice — once against a baseline with the
// change *absent*, once with it *present* — and returns the difference.
//
// The engine is a pure function of a Snapshot, so both worlds are just two
// Snapshots. No file is written and nothing is staged.
//
// Removing the change first is what makes the answer meaningful when the code
// already does the thing being asked about: without the baseline step, asking
// about an import that already exists compares the tree to itself and reports
// no new violation, which reads as approval for something that is failing CI
// right now.
func (s *Service) preflight(baseline, candidate func(*model.Snapshot)) (*Verdict, error) {
	snap, err := scan.New(s.Root).Scan()
	if err != nil {
		return nil, err
	}
	decisions, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	eng, err := engine.Compile(decisions)
	if err != nil {
		return nil, err
	}

	if baseline != nil {
		baseline(snap)
	}
	before := eng.Run(snap)
	candidate(snap)
	after := eng.Run(snap)

	introduced := diffFindings(before, after)
	return &Verdict{
		Allowed:  len(introduced) == 0,
		Findings: introduced,
		Existing: len(before),
		Checks:   eng.Len(),
	}, nil
}

// diffFindings returns findings present in after but not in before, counting
// multiplicity so a second identical violation still registers.
func diffFindings(before, after []model.Finding) []model.Finding {
	seen := make(map[string]int, len(before))
	for _, f := range before {
		seen[findingKey(f)]++
	}
	var out []model.Finding
	for _, f := range after {
		k := findingKey(f)
		if seen[k] > 0 {
			seen[k]--
			continue
		}
		out = append(out, f)
	}
	return out
}

// findingKey identifies a finding as the reader sees it, Evidence included.
//
// Evidence is what carries the *degree* of a violation, and leaving it out made
// preflight answer the wrong question for any check that reports one. A repo
// already over its max_dependencies budget produced the same Message before and
// after adding another package — only Evidence changed, "5 declared, budget 3"
// to "6 declared, budget 3" — so the diff saw nothing new and preflight
// answered ALLOWED to a change that made the violation strictly worse.
func findingKey(f model.Finding) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s", f.Decision, f.Rule, f.File, f.Line, f.Message, f.Evidence)
}

// CanImport reports whether `from` may import `spec`. The file named by from
// need not exist yet.
func (s *Service) CanImport(from, spec string) (*Verdict, error) {
	id := model.FileID(from)
	im, err := scan.ResolveSpec(s.Root, id, spec)
	if err != nil {
		return nil, err
	}

	present := false
	v, err := s.preflight(
		// Baseline: the tree without this import, whether or not it is there.
		func(snap *model.Snapshot) {
			kept := make([]model.Import, 0, len(snap.Imports))
			for _, e := range snap.Imports {
				if e.From == id && e.Spec == spec {
					present = true
					continue
				}
				kept = append(kept, e)
			}
			snap.Imports = kept
		},
		func(snap *model.Snapshot) { snap.Imports = append(snap.Imports, im) },
	)
	if err != nil {
		return nil, err
	}
	v.Present = present

	where := fmt.Sprintf("%q is an external package (%s)", spec, im.Pkg)
	if !im.External() {
		where = fmt.Sprintf("%q resolves to %s", spec, im.To)
	}
	v.Note = where
	return v, nil
}

// CanAddDependency reports whether pkg may be a direct dependency.
func (s *Service) CanAddDependency(pkg, version string) (*Verdict, error) {
	if version == "" {
		version = "*"
	}
	present := false
	v, err := s.preflight(
		// Baseline: the tree without this package, whether or not it is there.
		func(snap *model.Snapshot) {
			if have, ok := snap.Manifest.Direct[pkg]; ok {
				present = true
				if version == "*" {
					version = have
				}
				delete(snap.Manifest.Direct, pkg)
			}
		},
		func(snap *model.Snapshot) { snap.Manifest.Direct[pkg] = version },
	)
	if err != nil {
		return nil, err
	}
	v.Present = present
	return v, nil
}

// ValidateEnforce compiles an enforce block without writing it, so a bad rule
// from an agent is rejected at the door.
//
// This matters more than it looks: store.Load treats a malformed decision file
// as a hard error, so one unvalidated write would break `jasper check` for the
// whole repo — including the checks the bad file has nothing to do with.
func ValidateEnforce(enforce []map[string]any) error {
	d := &model.Decision{
		ID:     "DEC-PROPOSED",
		Status: model.StatusAccepted, // so Compile does not skip it
		Path:   "<proposed>",
	}
	for _, item := range enforce {
		for kind, cfg := range item {
			body, ok := normalizeJSON(cfg).(map[string]any)
			if !ok {
				return fmt.Errorf("enforce %q: body must be an object", kind)
			}
			d.Enforce = append(d.Enforce, model.EnforceRule{Kind: kind, Cfg: model.RawConfig(body)})
		}
	}
	if len(d.Enforce) == 0 {
		return nil // a decision with no rules is a note, which is allowed
	}
	_, err := engine.Compile([]*model.Decision{d})
	return err
}

// normalizeJSON reshapes a config body into the form the checks expect.
//
// Enforce bodies reach the engine from three places with three different Go
// types for the same YAML, and the checks are written against what the YAML
// loader produces:
//
//	YAML (store)   list -> []any     number -> int
//	JSON (MCP)     list -> []any     number -> float64
//	Go literal     list -> []string  number -> int
//	 (facts)
//
// So no_cycles asserts cfg["depth"].(int) and would reject a JSON float64,
// while stringList matches []any and silently ignores a []string. Both
// mismatches are fixed here, once, rather than loosening every check.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int(t)) {
			return int(t)
		}
		return t
	case []string:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = e
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeJSON(e)
		}
		return out
	}
	return v
}

// NormalizeEnforce applies normalizeJSON across a whole enforce block.
func NormalizeEnforce(enforce []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(enforce))
	for _, item := range enforce {
		m, _ := normalizeJSON(item).(map[string]any)
		out = append(out, m)
	}
	return out
}
