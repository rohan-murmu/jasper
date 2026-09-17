package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

var _ engine.Check = PublicAPI{}

var identityAPI = model.RawConfig{
	"module": "src/identity/**",
	"entry":  "src/identity/index.ts",
}

func TestPublicAPIDeniesBypass(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "src/billing/invoice.ts",
		To:   "src/identity/internal/session.ts",
		Spec: "@/identity/internal/session",
		Line: 3,
	})
	got := runCheck(t, PublicAPI{}, identityAPI, snap)
	if len(got) != 1 {
		t.Fatalf("reaching past the entry point must be denied; got %d", len(got))
	}
	if got[0].Line != 3 {
		t.Errorf("line = %d, want 3", got[0].Line)
	}
}

func TestPublicAPIAllowsTheEntryPoint(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "src/billing/invoice.ts", To: "src/identity/index.ts", Spec: "@/identity",
	})
	if got := runCheck(t, PublicAPI{}, identityAPI, snap); len(got) != 0 {
		t.Fatalf("the front door is the point; got %d findings", len(got))
	}
}

// A module must be free to use its own internals, or the rule makes the module
// unimplementable.
func TestPublicAPIAllowsInternalUse(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "src/identity/index.ts", To: "src/identity/internal/session.ts",
	})
	if got := runCheck(t, PublicAPI{}, identityAPI, snap); len(got) != 0 {
		t.Fatalf("identity may use its own internals; got %d", len(got))
	}
}

// A new private directory should be covered without widening the rule — the
// property public_api has and a no_import deny list does not.
func TestPublicAPICoversNewInternalsAutomatically(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "src/billing/invoice.ts", To: "src/identity/brand-new/thing.ts",
	})
	if got := runCheck(t, PublicAPI{}, identityAPI, snap); len(got) != 1 {
		t.Fatalf("a directory added later is still behind the boundary; got %d", len(got))
	}
}

func TestPublicAPIExceptExemptsImporter(t *testing.T) {
	cfg := model.RawConfig{
		"module": "src/identity/**",
		"entry":  "src/identity/index.ts",
		"except": "src/testing/**",
	}
	snap := snapOf(nil, model.Import{
		From: "src/testing/fixtures.ts", To: "src/identity/internal/session.ts",
	})
	if got := runCheck(t, PublicAPI{}, cfg, snap); len(got) != 0 {
		t.Fatalf("except must exempt the importer; got %d", len(got))
	}
}

func TestPublicAPIRejectsBadConfig(t *testing.T) {
	mustReject(t, PublicAPI{}, model.RawConfig{"module": "src/identity/**"})
	mustReject(t, PublicAPI{}, model.RawConfig{"entry": "src/identity/index.ts"})
	mustReject(t, PublicAPI{}, model.RawConfig{
		"module": "a/**", "entry": "a/i.ts", "mesage": "typo"})
}
