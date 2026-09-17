package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

var _ engine.Check = RequireImport{}

var authRule = model.RawConfig{
	"in":      "src/routes/**",
	"imports": "src/middlewares/verifyToken.js",
	"message": "every route must verify the token",
}

func TestRequireImportFlagsTheFileThatOmitsIt(t *testing.T) {
	snap := snapOf(
		[]model.FileID{"src/routes/auth.route.js", "src/routes/open.route.js"},
		model.Import{From: "src/routes/auth.route.js", To: "src/middlewares/verifyToken.js"},
	)
	got := runCheck(t, RequireImport{}, authRule, snap)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if got[0].File != "src/routes/open.route.js" {
		t.Errorf("file = %s, want the route missing the middleware", got[0].File)
	}
	if got[0].Message != "every route must verify the token" {
		t.Errorf("message = %q, want the decision's own wording", got[0].Message)
	}
}

func TestRequireImportSatisfiedIsSilent(t *testing.T) {
	snap := snapOf(
		[]model.FileID{"src/routes/auth.route.js"},
		model.Import{From: "src/routes/auth.route.js", To: "src/middlewares/verifyToken.js"},
	)
	if got := runCheck(t, RequireImport{}, authRule, snap); len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}

func TestRequireImportExceptExemptsAFile(t *testing.T) {
	cfg := model.RawConfig{
		"in":      "src/routes/**",
		"imports": "src/middlewares/verifyToken.js",
		"except":  []any{"src/routes/public.route.js"},
	}
	snap := snapOf([]model.FileID{"src/routes/public.route.js"})
	if got := runCheck(t, RequireImport{}, cfg, snap); len(got) != 0 {
		t.Fatalf("an exempt route must not be flagged; got %d", len(got))
	}
}

// The same rule must work for "must use our logger package".
func TestRequireImportMatchesExternalPackages(t *testing.T) {
	cfg := model.RawConfig{"in": "src/**", "imports": "@acme/logger"}
	snap := snapOf(
		[]model.FileID{"src/a.ts", "src/b.ts"},
		model.Import{From: "src/a.ts", Pkg: "@acme/logger", Spec: "@acme/logger"},
	)
	got := runCheck(t, RequireImport{}, cfg, snap)
	if len(got) != 1 || got[0].File != "src/b.ts" {
		t.Fatalf("want only src/b.ts flagged; got %v", got)
	}
}

func TestRequireImportOnlyGovernsMatchingFiles(t *testing.T) {
	snap := snapOf([]model.FileID{"src/services/user.service.js", "src/utils/hex.js"})
	if got := runCheck(t, RequireImport{}, authRule, snap); len(got) != 0 {
		t.Fatalf("files outside `in` are not governed; got %d", len(got))
	}
}

func TestRequireImportRejectsBadConfig(t *testing.T) {
	mustReject(t, RequireImport{}, model.RawConfig{"in": "src/**"})
	mustReject(t, RequireImport{}, model.RawConfig{"imports": "x"})
}
