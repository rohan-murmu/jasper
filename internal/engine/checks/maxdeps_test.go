package checks

import (
	"testing"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

var _ engine.Check = MaxDependencies{}

func TestMaxDependenciesUnderBudgetIsSilent(t *testing.T) {
	snap := snapManifest(map[string]string{"a": "1", "b": "1"}, "package.json")
	if got := runCheck(t, MaxDependencies{}, model.RawConfig{"count": 5}, snap); len(got) != 0 {
		t.Fatalf("2 deps under a budget of 5; got %d", len(got))
	}
}

// The boundary itself: a budget of N allows exactly N.
func TestMaxDependenciesAllowsExactlyTheBudget(t *testing.T) {
	snap := snapManifest(map[string]string{"a": "1", "b": "1"}, "package.json")
	if got := runCheck(t, MaxDependencies{}, model.RawConfig{"count": 2}, snap); len(got) != 0 {
		t.Fatalf("a budget of 2 permits 2; got %d", len(got))
	}
}

func TestMaxDependenciesOverBudgetReportsOnce(t *testing.T) {
	snap := snapManifest(map[string]string{"a": "1", "b": "1", "c": "1"}, "go.mod")
	got := runCheck(t, MaxDependencies{}, model.RawConfig{"count": 2}, snap)
	if len(got) != 1 {
		t.Fatalf("a budget is one fact about the project, not one per package; got %d", len(got))
	}
	if got[0].File != "go.mod" {
		t.Errorf("file = %s, want the manifest", got[0].File)
	}
	if got[0].Evidence == "" || got[0].Hint == "" {
		t.Error("the finding must say the count and what to do about it")
	}
}

func TestMaxDependenciesRejectsBadConfig(t *testing.T) {
	mustReject(t, MaxDependencies{}, model.RawConfig{})
	mustReject(t, MaxDependencies{}, model.RawConfig{"count": 0})
	mustReject(t, MaxDependencies{}, model.RawConfig{"count": "twenty"})
}
