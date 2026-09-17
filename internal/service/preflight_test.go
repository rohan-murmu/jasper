package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rohan/jasper/internal/model"
)

func TestValidateEnforceRejectsTypo(t *testing.T) {
	err := ValidateEnforce([]map[string]any{
		{"no_import": map[string]any{"from": "a/**", "to": "b/**", "mesage": "typo"}},
	})
	if err == nil {
		t.Fatal("a typo must be rejected before the file is written, not enforce nothing silently")
	}
}

func TestValidateEnforceRejectsUnknownKind(t *testing.T) {
	if err := ValidateEnforce([]map[string]any{
		{"no_imports": map[string]any{"from": "a/**", "to": "b/**"}},
	}); err == nil {
		t.Fatal("an unknown check kind must be rejected")
	}
}

func TestValidateEnforceAcceptsValidRule(t *testing.T) {
	if err := ValidateEnforce([]map[string]any{
		{"confine": map[string]any{"package": "pg", "to": "src/db/**"}},
	}); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
}

// A decision with no rules is a note, which the model explicitly allows.
func TestValidateEnforceAllowsNoRules(t *testing.T) {
	if err := ValidateEnforce(nil); err != nil {
		t.Fatalf("a note is not an error: %v", err)
	}
}

// encoding/json makes every number a float64, but no_cycles asserts an int.
// Without normalisation a rule that is valid in YAML is rejected over MCP.
func TestValidateEnforceAcceptsJSONNumbers(t *testing.T) {
	enforce := NormalizeEnforce([]map[string]any{
		{"no_cycles": map[string]any{"within": "internal/**", "depth": float64(2)}},
	})
	if err := ValidateEnforce(enforce); err != nil {
		t.Fatalf("depth arriving as a JSON number must still compile: %v", err)
	}
	inner := enforce[0]["no_cycles"].(map[string]any)
	if _, ok := inner["depth"].(int); !ok {
		t.Errorf("depth = %T, want int so the written YAML says 2 and not 2.0", inner["depth"])
	}
}

// Two identical violations must not collapse into one, or adding a second
// offending import would read as introducing nothing.
func TestDiffFindingsCountsMultiplicity(t *testing.T) {
	f := model.Finding{Decision: "DEC-001", Rule: "no_import", File: "a.ts", Line: 1, Message: "no"}
	got := diffFindings([]model.Finding{f}, []model.Finding{f, f})
	if len(got) != 1 {
		t.Fatalf("got %d introduced findings, want 1", len(got))
	}
}

func TestDiffFindingsIgnoresPreExisting(t *testing.T) {
	f := model.Finding{Decision: "DEC-001", File: "a.ts", Line: 1, Message: "no"}
	if got := diffFindings([]model.Finding{f}, []model.Finding{f}); len(got) != 0 {
		t.Fatalf("got %d, want 0 — an unchanged tree introduces nothing", len(got))
	}
}

// tempRepo copies the fixture somewhere writable.
func tempRepo(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "repo")
	if err := os.CopyFS(dst, os.DirFS("../../testdata/acme-api")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	return dst
}

// The property the whole write boundary rests on: an agent may file a
// proposal, but a proposal enforces nothing until a human accepts it.
func TestProposedDecisionDoesNotBind(t *testing.T) {
	svc, err := New(tempRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Check()
	if err != nil {
		t.Fatal(err)
	}

	// A rule broad enough to condemn every file in the fixture.
	d, err := svc.Record(&Proposal{
		Title: "Nothing may import anything",
		Why:   "deliberately absurd, to prove a proposal cannot bind",
		Brief: "n/a",
		Enforce: []map[string]any{
			{"no_import": map[string]any{"from": "**", "to": "**"}},
		},
	}, model.OriginAuthored, model.StatusProposed)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if d.Path == "" {
		t.Error("the written path must come back so the agent can tell the user where to review it")
	}

	after, err := svc.Check()
	if err != nil {
		t.Fatalf("check after proposal: %v", err)
	}
	if len(after.Findings) != len(before.Findings) {
		t.Fatalf("findings went from %d to %d — a proposed decision must not be enforced",
			len(before.Findings), len(after.Findings))
	}
	if after.Checks != before.Checks {
		t.Errorf("compiled checks went from %d to %d — proposals must not compile",
			before.Checks, after.Checks)
	}
}

// The counterpart: the same rule accepted *does* bind. Without this, the test
// above would also pass if Record were silently broken.
func TestAcceptedDecisionDoesBind(t *testing.T) {
	svc, err := New(tempRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Check()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(&Proposal{
		Title: "Nothing may import anything",
		Why:   "deliberately absurd",
		Brief: "n/a",
		Enforce: []map[string]any{
			{"no_import": map[string]any{"from": "**", "to": "**"}},
		},
	}, model.OriginAuthored, model.StatusAccepted); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Check()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Findings) <= len(before.Findings) {
		t.Fatalf("an accepted rule must bind: findings %d -> %d",
			len(before.Findings), len(after.Findings))
	}
}

func TestCanImportRejectsUnknownLanguage(t *testing.T) {
	svc, err := New("../../testdata/acme-api")
	if err != nil {
		t.Fatal(err)
	}
	// Must be a language Jasper does not handle — see the note in mcp_test.go.
	if _, err := svc.CanImport("app/main.rb", "sinatra"); err == nil {
		t.Fatal("a file type jasper cannot parse must error, not silently allow")
	}
}

// Every proposal `jasper init` offers must survive the validation Record
// performs before writing.
//
// This is a regression test. facts builds enforce bodies from Go literals, so
// a package list arrives as []string, while the checks are written against
// what YAML produces ([]any). Nothing validated on the init path until Record
// started to, at which point forbid_dependency rejected its own proposal as
// "must list at least one package" and `jasper init` stopped writing the
// datastore decision on every real repo.
func TestInitProposalsSurviveValidation(t *testing.T) {
	svc, err := New(tempRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.Plan()
	if err != nil {
		t.Fatal(err)
	}
	proposed := 0
	for _, f := range plan.Facts {
		if f.Proposal == nil || !f.Holds {
			continue
		}
		proposed++
		if _, err := svc.Record(f.Proposal, model.OriginObserved, model.StatusAccepted); err != nil {
			t.Errorf("init could not write %q: %v", f.Proposal.Title, err)
		}
	}
	if proposed == 0 {
		t.Fatal("fixture should yield at least one proposal, or this test proves nothing")
	}
}

// The shape bridge itself: a Go-literal []string must reach the checks as a
// list they recognise.
func TestNormalizeBridgesGoStringSlices(t *testing.T) {
	enforce := NormalizeEnforce([]map[string]any{
		{"forbid_dependency": map[string]any{"packages": []string{"mongodb", "mongoose"}}},
	})
	if err := ValidateEnforce(enforce); err != nil {
		t.Fatalf("a []string package list must validate: %v", err)
	}
	inner := enforce[0]["forbid_dependency"].(map[string]any)
	if _, ok := inner["packages"].([]any); !ok {
		t.Errorf("packages = %T, want []any — the shape stringList matches", inner["packages"])
	}
}

// A repo that is already over budget must not be told that adding one more
// dependency is fine. The violation gets worse, so the answer is DENIED.
func TestPreflightCatchesWorseningViolation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json",
		`{"name":"over","dependencies":{"a":"1","b":"1","c":"1","d":"1","e":"1"}}`)
	write(t, root, ".jasper/jasper.yaml", "version: 1\n")
	write(t, root, ".jasper/decisions/001-budget.yaml", `id: DEC-001
title: Dependency budget
status: accepted
why: keep the tree auditable
brief: budget is 3 direct dependencies
enforce:
  - max_dependencies:
      count: 3
`)

	svc, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	v, err := svc.CanAddDependency("lodash", "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Allowed {
		t.Fatalf("adding a 6th dependency to a repo already over a budget of 3 was ALLOWED; "+
			"verdict=%+v", v)
	}
	if len(v.Findings) != 1 {
		t.Errorf("got %d findings, want 1", len(v.Findings))
	}
}

// The converse still has to hold: a violation that is untouched by the change
// must not be reported as introduced, or every answer becomes DENIED and the
// agent stops asking.
func TestPreflightIgnoresUnrelatedExistingViolation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"x","dependencies":{"mongoose":"1"}}`)
	write(t, root, ".jasper/jasper.yaml", "version: 1\n")
	write(t, root, ".jasper/decisions/001-store.yaml", `id: DEC-001
title: Datastore is PostgreSQL
status: accepted
why: transactions
brief: PostgreSQL only
enforce:
  - forbid_dependency:
      packages: [mongoose]
`)

	svc, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.CanAddDependency("zod", "")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allowed {
		t.Fatalf("adding an unrelated package should be ALLOWED; verdict=%+v", v)
	}
	if v.Existing != 1 {
		t.Errorf("Existing = %d, want 1 (the pre-existing mongoose violation)", v.Existing)
	}
}

// write creates a file and any parent directories, for tests that need a repo
// shaped differently from the shared fixture.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
