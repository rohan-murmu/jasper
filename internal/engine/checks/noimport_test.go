package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

func run(t *testing.T, cfg model.RawConfig, snap *model.Snapshot) []model.Finding {
	t.Helper()
	r, err := NoImport{}.Compile(cfg, &model.Decision{ID: "DEC-001"})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return r.Run(snap)
}

func snapWith(imports ...model.Import) *model.Snapshot {
	return &model.Snapshot{Files: map[model.FileID]*model.File{}, Imports: imports}
}

// A broad `except` must not cancel the rule it sits inside. This is the bug
// that made a boundary rule silently enforce nothing.
func TestExceptDoesNotExemptTargets(t *testing.T) {
	snap := snapWith(model.Import{
		From: "src/billing/invoice.ts",
		To:   "src/identity/internal/session.ts",
		Spec: "@/identity/internal/session",
		Line: 3,
	})
	got := run(t, model.RawConfig{
		"from":   "src/**",
		"to":     "src/identity/internal/**",
		"except": "src/identity/**",
	}, snap)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1 — `except` must exempt importers, not targets", len(got))
	}
	if got[0].Line != 3 {
		t.Errorf("line = %d, want 3", got[0].Line)
	}
}

func TestExceptExemptsImporter(t *testing.T) {
	snap := snapWith(model.Import{
		From: "src/identity/index.ts",
		To:   "src/identity/internal/session.ts",
		Line: 1,
	})
	if got := run(t, model.RawConfig{
		"from":   "src/**",
		"to":     "src/identity/internal/**",
		"except": "src/identity/**",
	}, snap); len(got) != 0 {
		t.Fatalf("got %d findings, want 0 — identity may use its own internals", len(got))
	}
}

func TestTypeOnlyImportsIgnoredByDefault(t *testing.T) {
	snap := snapWith(model.Import{
		From: "src/billing/invoice.ts",
		To:   "src/identity/internal/types.ts",
		Type: true,
	})
	cfg := model.RawConfig{"from": "src/**", "to": "src/identity/internal/**"}
	if got := run(t, cfg, snap); len(got) != 0 {
		t.Fatalf("type-only import should not count as coupling, got %d", len(got))
	}
	cfg["include_type_only"] = true
	if got := run(t, cfg, snap); len(got) != 1 {
		t.Fatalf("include_type_only should catch it, got %d", len(got))
	}
}

func TestExternalImportsIgnored(t *testing.T) {
	snap := snapWith(model.Import{From: "src/billing/invoice.ts", Pkg: "zod", Spec: "zod"})
	if got := run(t, model.RawConfig{"from": "src/**", "to": "src/**"}, snap); len(got) != 0 {
		t.Fatalf("external import must not match a path rule, got %d", len(got))
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := NoImport{}.Compile(model.RawConfig{
		"from": "a/**", "to": "b/**", "mesage": "typo",
	}, &model.Decision{ID: "DEC-001"})
	if err == nil {
		t.Fatal("a typo in a decision file must be an error, not a silently ignored rule")
	}
}

var _ engine.Check = NoImport{}
