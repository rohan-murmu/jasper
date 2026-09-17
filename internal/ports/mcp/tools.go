package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/service"
)

// obj is shorthand for the JSON Schema literals below.
type obj = map[string]any

// toolDefs is the tool surface. Descriptions are written for the agent, not
// for a human reading docs: each one says *when to call*, because that is the
// only thing that decides whether the tool gets used at the right moment.
func toolDefs() []obj {
	return []obj{
		{
			"name": "architecture_brief",
			"description": "Get this project's binding architectural decisions. " +
				"Call this before writing code in an area you have not touched yet, " +
				"and before choosing where to put a new module. Cheap, and it is the " +
				"same text a human would be handed on their first day.",
			"inputSchema": obj{"type": "object", "properties": obj{}},
		},
		{
			"name": "can_import",
			"description": "Ask whether one file may import one specifier, BEFORE you write it. " +
				"The importing file does not need to exist yet. Use this whenever an import " +
				"crosses a module or layer boundary — reaching into another module's internals, " +
				"importing a driver or client library outside the layer that owns it, or wiring " +
				"two subsystems together. Returns ALLOWED or DENIED with the decision that " +
				"governs it. This is cheaper than writing the code and having CI reject it.",
			"inputSchema": obj{
				"type": "object",
				"properties": obj{
					"from": obj{
						"type": "string",
						"description": "Repo-relative path of the importing file, e.g. " +
							"\"src/billing/invoice.ts\". Need not exist yet.",
					},
					"spec": obj{
						"type": "string",
						"description": "The import specifier exactly as it would be written, e.g. " +
							"\"@/identity/internal/session\", \"./pool\", \"pg\", " +
							"\"github.com/rohan/jasper/internal/scan\".",
					},
				},
				"required": []string{"from", "spec"},
			},
		},
		{
			"name": "can_add_dependency",
			"description": "Ask whether a package may be added as a direct dependency, BEFORE you " +
				"add it to package.json or go.mod. Call this every time you are about to " +
				"introduce a library — projects commonly record a decision about which HTTP " +
				"client, datastore driver, validator or test runner they use, and adding a " +
				"second one is the most common kind of silent architectural drift.",
			"inputSchema": obj{
				"type": "object",
				"properties": obj{
					"package": obj{"type": "string", "description": "Package name, e.g. \"mongoose\", \"axios\", \"gopkg.in/yaml.v3\"."},
					"version": obj{"type": "string", "description": "Optional version constraint; only used in the explanation."},
				},
				"required": []string{"package"},
			},
		},
		{
			"name": "check_architecture",
			"description": "Run every decision against the code as it is on disk right now. " +
				"Call this after a batch of edits, and before you tell the user you are done. " +
				"Returns each violation with the file, line, and a suggested repair. " +
				"Equivalent to what CI will run, so a clean result here means CI passes.",
			"inputSchema": obj{"type": "object", "properties": obj{}},
		},
		{
			"name": "explain_decision",
			"description": "Read the full rationale behind one decision: why it was made, what it " +
				"forbids, and when it was recorded. Call this when a check denies something and " +
				"you need to understand the constraint before working around it — the rationale " +
				"usually names the failure the decision exists to prevent.",
			"inputSchema": obj{
				"type": "object",
				"properties": obj{
					"id": obj{"type": "string", "description": "Decision id, e.g. \"DEC-002\"."},
				},
				"required": []string{"id"},
			},
		},
		{
			"name":        "list_decisions",
			"description": "List every recorded decision with its id, title, and whether it is enforced or advisory.",
			"inputSchema": obj{"type": "object", "properties": obj{}},
		},
		{
			"name": "propose_decision",
			"description": "Propose a NEW architectural decision for a human to review. " +
				"Writes a file with status: proposed, which has NO effect on any check — it " +
				"cannot unblock you, and `check_architecture` will keep denying whatever it " +
				"denied before. Use it when you have concluded that a recorded decision is " +
				"stale or wrong, or when you have made a genuine architectural choice the " +
				"project has not recorded yet. Do not use it to route around a denial: " +
				"change your approach instead, and mention the proposal to the user.",
			"inputSchema": obj{
				"type": "object",
				"properties": obj{
					"title": obj{"type": "string", "description": "One line, e.g. \"Datastore is PostgreSQL\"."},
					"why": obj{
						"type": "string",
						"description": "Rationale for a human reading this in six months. Name the " +
							"failure it prevents and the alternatives considered.",
					},
					"brief": obj{
						"type":        "string",
						"description": "~60 tokens of constraint for a coding agent, in the imperative.",
					},
					"enforce": obj{
						"type": "array",
						"description": "Optional. Each element is a single-key object naming a check " +
							"kind: no_import, no_cycles, forbid_dependency, approved_dependencies, " +
							"confine. Example: [{\"confine\": {\"package\": \"pg\", \"to\": \"src/db/**\"}}]. " +
							"Omit for an advisory note. Invalid rules are rejected.",
						"items": obj{"type": "object"},
					},
				},
				"required": []string{"title", "why", "brief"},
			},
		},
	}
}

// ---- dispatch ----

func (s *Server) callTool(raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "params must be an object with name and arguments"}
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}

	switch p.Name {
	case "architecture_brief":
		return s.toolBrief()
	case "can_import":
		return s.toolCanImport(p.Arguments)
	case "can_add_dependency":
		return s.toolCanAddDependency(p.Arguments)
	case "check_architecture":
		return s.toolCheck()
	case "explain_decision":
		return s.toolExplain(p.Arguments)
	case "list_decisions":
		return s.toolList()
	case "propose_decision":
		return s.toolPropose(p.Arguments)
	default:
		return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
	}
}

// text is a successful tool result. A DENIED verdict is a success: the tool
// answered the question. isError is reserved for the tool failing to run.
func text(format string, args ...any) any {
	return obj{
		"content": []obj{{"type": "text", "text": fmt.Sprintf(format, args...)}},
		"isError": false,
	}
}

func toolErr(err error) any {
	return obj{
		"content": []obj{{"type": "text", "text": "jasper: " + err.Error()}},
		"isError": true,
	}
}

// ---- handlers: one service call each ----

func (s *Server) toolBrief() (any, *rpcError) {
	b, err := s.svc.Brief()
	if err != nil {
		return toolErr(err), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Architectural decisions for %s\n", b.Project)
	if len(b.Decisions) == 0 && len(b.Notes) == 0 {
		sb.WriteString("\nNo decisions recorded yet. Nothing is enforced.\n")
		return text("%s", sb.String()), nil
	}
	if len(b.Decisions) > 0 {
		sb.WriteString("\nBinding — check_architecture fails the build when code contradicts these:\n")
		for _, d := range b.Decisions {
			fmt.Fprintf(&sb, "\n%s %s\n  %s\n", d.ID, d.Title, strings.TrimSpace(d.Brief))
		}
	}
	if len(b.Notes) > 0 {
		sb.WriteString("\nAdvisory (not enforced):\n")
		for _, n := range b.Notes {
			fmt.Fprintf(&sb, "  %s\n", n)
		}
	}
	if len(b.Modules) > 0 {
		sb.WriteString("\nModules:\n")
		for _, m := range b.Modules {
			fmt.Fprintf(&sb, "  %s (%d files)\n", m.Glob, m.Files)
		}
	}
	return text("%s", sb.String()), nil
}

func (s *Server) toolCanImport(args map[string]any) (any, *rpcError) {
	from, ok := argString(args, "from")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"from\" is required"}
	}
	spec, ok := argString(args, "spec")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"spec\" is required"}
	}
	v, err := s.svc.CanImport(from, spec)
	if err != nil {
		return toolErr(err), nil
	}
	return text("%s", s.renderVerdict(v, fmt.Sprintf("import of %q by %s", spec, from))), nil
}

func (s *Server) toolCanAddDependency(args map[string]any) (any, *rpcError) {
	pkg, ok := argString(args, "package")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"package\" is required"}
	}
	version, _ := argString(args, "version")
	v, err := s.svc.CanAddDependency(pkg, version)
	if err != nil {
		return toolErr(err), nil
	}
	return text("%s", s.renderVerdict(v, fmt.Sprintf("dependency %s", pkg))), nil
}

func (s *Server) toolCheck() (any, *rpcError) {
	rep, err := s.svc.Check()
	if err != nil {
		return toolErr(err), nil
	}
	titles, paths := s.index()
	var sb strings.Builder
	if len(rep.Findings) == 0 {
		fmt.Fprintf(&sb, "PASS — %d checks, %d decisions, %d files.\nCI will pass on this tree.\n",
			rep.Checks, rep.Decisions, rep.Files)
		return text("%s", sb.String()), nil
	}
	fmt.Fprintf(&sb, "FAIL — %d violation(s) across %d checks. CI will reject this tree.\n",
		rep.Errors(), rep.Checks)
	for _, id := range orderOf(rep.Findings) {
		fmt.Fprintf(&sb, "\n%s %s\n", id, titles[id])
		for _, f := range rep.Findings {
			if f.Decision != id {
				continue
			}
			fmt.Fprintf(&sb, "  %s\n    %s\n", f.Location(), f.Message)
			if f.Evidence != "" {
				fmt.Fprintf(&sb, "    evidence: %s\n", f.Evidence)
			}
			if f.Hint != "" {
				fmt.Fprintf(&sb, "    fix: %s\n", f.Hint)
			}
		}
		if p := paths[id]; p != "" {
			fmt.Fprintf(&sb, "  rationale: %s (explain_decision id=%s)\n", p, id)
		}
	}
	return text("%s", sb.String()), nil
}

func (s *Server) toolExplain(args map[string]any) (any, *rpcError) {
	want, ok := argString(args, "id")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"id\" is required"}
	}
	ds, err := s.svc.Decisions()
	if err != nil {
		return toolErr(err), nil
	}
	up := strings.ToUpper(want)
	for _, d := range ds {
		if !strings.EqualFold(d.ID, want) && !strings.Contains(strings.ToUpper(d.ID), up) {
			continue
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s %s\nstatus: %s · origin: %s · recorded: %s\n",
			d.ID, d.Title, d.Status, orDash(string(d.Origin)), orDash(d.Date))
		if d.Why != "" {
			fmt.Fprintf(&sb, "\nwhy:\n%s\n", strings.TrimSpace(d.Why))
		}
		if d.Brief != "" {
			fmt.Fprintf(&sb, "\nconstraint:\n%s\n", strings.TrimSpace(d.Brief))
		}
		if len(d.Enforce) == 0 {
			sb.WriteString("\nNot enforced — this is advisory, not a rule.\n")
		} else {
			sb.WriteString("\nenforced by:\n")
			for _, e := range d.Enforce {
				b, _ := json.Marshal(e.Cfg)
				fmt.Fprintf(&sb, "  %s %s\n", e.Kind, b)
			}
		}
		fmt.Fprintf(&sb, "\nsource: %s\n", d.Path)
		return text("%s", sb.String()), nil
	}
	return text("No decision matching %q. Call list_decisions to see what exists.", want), nil
}

func (s *Server) toolList() (any, *rpcError) {
	ds, err := s.svc.Decisions()
	if err != nil {
		return toolErr(err), nil
	}
	if len(ds) == 0 {
		return text("No decisions recorded. Nothing is enforced in this project."), nil
	}
	var sb strings.Builder
	for _, d := range ds {
		kind := "advisory"
		switch {
		case !d.Active():
			kind = "inactive"
		case d.Binding():
			kind = fmt.Sprintf("enforced (%d rule%s)", len(d.Enforce), plural(len(d.Enforce)))
		}
		fmt.Fprintf(&sb, "%-9s %-12s %s\n", d.ID, kind, d.Title)
	}
	return text("%s", sb.String()), nil
}

func (s *Server) toolPropose(args map[string]any) (any, *rpcError) {
	title, ok := argString(args, "title")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"title\" is required"}
	}
	why, ok := argString(args, "why")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"why\" is required"}
	}
	brief, ok := argString(args, "brief")
	if !ok {
		return nil, &rpcError{codeInvalidParams, "\"brief\" is required"}
	}
	enforce := service.NormalizeEnforce(argEnforce(args["enforce"]))
	if err := service.ValidateEnforce(enforce); err != nil {
		return toolErr(fmt.Errorf("enforce block rejected: %w", err)), nil
	}
	d, err := s.svc.Record(
		&service.Proposal{Title: title, Why: why, Brief: brief, Enforce: enforce},
		model.OriginAuthored, model.StatusProposed,
	)
	if err != nil {
		return toolErr(err), nil
	}
	return text("Wrote %s %q with status: proposed.\n\n"+
		"This has NO effect yet — engine.Compile skips decisions that are not accepted, so "+
		"check_architecture still enforces exactly what it did before. A human must change "+
		"status to accepted for it to bind. Tell the user the proposal is waiting for review "+
		"in %s, and continue working within the existing decisions.",
		d.ID, d.Title, d.Path), nil
}

// ---- rendering ----

func (s *Server) renderVerdict(v *service.Verdict, action string) string {
	titles, paths := s.index()
	var sb strings.Builder

	if v.Allowed {
		if v.Present {
			fmt.Fprintf(&sb, "ALLOWED — %s is already in the tree and violates nothing.\n", action)
		} else {
			fmt.Fprintf(&sb, "ALLOWED — %s introduces no violation.\n", action)
		}
		if v.Note != "" {
			fmt.Fprintf(&sb, "%s\n", v.Note)
		}
		fmt.Fprintf(&sb, "(%d checks evaluated", v.Checks)
		if v.Existing > 0 {
			fmt.Fprintf(&sb, "; %d unrelated violation(s) already exist in this repo", v.Existing)
		}
		sb.WriteString(")\n")
		return sb.String()
	}

	if v.Present {
		fmt.Fprintf(&sb, "DENIED — %s is ALREADY in the tree and is violating a decision right now. "+
			"This is a live failure, not a hypothetical one: CI is red on it today.\n", action)
	} else {
		fmt.Fprintf(&sb, "DENIED — %s would violate a recorded decision.\n", action)
	}
	if v.Note != "" {
		fmt.Fprintf(&sb, "%s\n", v.Note)
	}
	for _, id := range orderOf(v.Findings) {
		fmt.Fprintf(&sb, "\n%s %s\n", id, titles[id])
		for _, f := range v.Findings {
			if f.Decision != id {
				continue
			}
			fmt.Fprintf(&sb, "  %s\n", f.Message)
			if f.Hint != "" {
				fmt.Fprintf(&sb, "  fix: %s\n", f.Hint)
			}
		}
		if p := paths[id]; p != "" {
			fmt.Fprintf(&sb, "  rationale: %s (explain_decision id=%s)\n", p, id)
		}
	}
	sb.WriteString("\nChange your approach rather than working around this. " +
		"If you believe the decision is stale, read it with explain_decision first.\n")
	return sb.String()
}

// index maps decision id to title and source path, for messages that name the
// decision behind a finding.
func (s *Server) index() (titles, paths map[string]string) {
	titles, paths = map[string]string{}, map[string]string{}
	ds, err := s.svc.Decisions()
	if err != nil {
		return titles, paths
	}
	for _, d := range ds {
		titles[d.ID] = d.Title
		paths[d.ID] = d.Path
	}
	return titles, paths
}

// orderOf returns the distinct decision ids in findings, sorted.
func orderOf(fs []model.Finding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if !seen[f.Decision] {
			seen[f.Decision] = true
			out = append(out, f.Decision)
		}
	}
	sort.Strings(out)
	return out
}

func argString(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

func argEnforce(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
