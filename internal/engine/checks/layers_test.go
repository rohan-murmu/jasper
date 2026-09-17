package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

var _ engine.Check = Layers{}

func TestLayersAllowsDownwardImports(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "internal/service/service.go", To: "internal/model/snapshot.go", Line: 3,
	})
	if got := runCheck(t, Layers{}, jasperStack, snap); len(got) != 0 {
		t.Fatalf("service may import model; got %d findings", len(got))
	}
}

func TestLayersDeniesUpwardImports(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "internal/model/snapshot.go", To: "internal/service/service.go", Line: 7,
	})
	got := runCheck(t, Layers{}, jasperStack, snap)
	if len(got) != 1 {
		t.Fatalf("model must not import service; got %d findings", len(got))
	}
	if got[0].Line != 7 {
		t.Errorf("line = %d, want 7", got[0].Line)
	}
}

// The case a direction-only rule gets wrong: two groups on the same tier are
// peers, and a peer import is not "downward" just because it is not upward.
func TestLayersDeniesPeerImports(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "internal/engine/engine.go", To: "internal/scan/scanner.go", Line: 4,
	})
	got := runCheck(t, Layers{}, jasperStack, snap)
	if len(got) != 1 {
		t.Fatalf("engine and scan are peers and must not import each other; got %d", len(got))
	}
	if got[0].Evidence == "" {
		t.Error("a peer violation must explain which two groups collided")
	}
}

func TestLayersAllowsImportsWithinOneGroup(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "internal/engine/checks/noimport.go", To: "internal/engine/engine.go", Line: 8,
	})
	if got := runCheck(t, Layers{}, jasperStack, snap); len(got) != 0 {
		t.Fatalf("one group is one module; got %d findings", len(got))
	}
}

func TestLayersAllowPeersOptsOut(t *testing.T) {
	cfg := model.RawConfig{
		"order":       jasperStack["order"],
		"allow_peers": true,
	}
	snap := snapOf(nil, model.Import{
		From: "internal/engine/engine.go", To: "internal/scan/scanner.go",
	})
	if got := runCheck(t, Layers{}, cfg, snap); len(got) != 0 {
		t.Fatalf("allow_peers should permit a sideways import; got %d", len(got))
	}
}

// A partial stack must be usable on day one, so anything outside it is ignored.
func TestLayersIgnoresUnlistedPaths(t *testing.T) {
	snap := snapOf(nil, model.Import{
		From: "cmd/jasper/main.go", To: "internal/ports/cli/cli.go",
	})
	if got := runCheck(t, Layers{}, jasperStack, snap); len(got) != 0 {
		t.Fatalf("cmd/ is outside the declared stack; got %d", len(got))
	}
}

func TestLayersIgnoresExternalAndTypeOnly(t *testing.T) {
	snap := snapOf(nil,
		model.Import{From: "internal/model/x.go", Pkg: "gopkg.in/yaml.v3", Spec: "gopkg.in/yaml.v3"},
		model.Import{From: "internal/model/x.ts", To: "internal/service/y.ts", Type: true},
	)
	if got := runCheck(t, Layers{}, jasperStack, snap); len(got) != 0 {
		t.Fatalf("external and type-only imports are not couplings; got %d", len(got))
	}
}

func TestLayersRejectsBadConfig(t *testing.T) {
	mustReject(t, Layers{}, model.RawConfig{})
	mustReject(t, Layers{}, model.RawConfig{"order": []any{"only/one/**"}})
	mustReject(t, Layers{}, model.RawConfig{"order": "not-a-list"})
	mustReject(t, Layers{}, model.RawConfig{"order": []any{"a/**", "b/**"}, "typo": 1})
}
