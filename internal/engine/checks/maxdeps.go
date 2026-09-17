package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(MaxDependencies{}) }

// MaxDependencies caps the number of declared direct dependencies.
//
// A budget is a different instrument from an allow list: approved_dependencies
// asks "was this one chosen deliberately?", while a budget asks "is the total
// still something we can audit, upgrade and ship?". Teams that answer only the
// first question end up with four hundred deliberate dependencies.
//
//   - max_dependencies:
//     count: 25
type MaxDependencies struct{}

func (MaxDependencies) Kind() string { return "max_dependencies" }

func (MaxDependencies) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "count", "message"); err != nil {
		return nil, err
	}
	raw, ok := cfg["count"]
	if !ok {
		return nil, fmt.Errorf("missing required field %q", "count")
	}
	n, ok := raw.(int)
	if !ok {
		return nil, fmt.Errorf("field %q must be an integer", "count")
	}
	if n < 1 {
		return nil, fmt.Errorf("field %q must be >= 1", "count")
	}
	return &maxDepsRunner{max: n, msg: optString(cfg, "message", "")}, nil
}

type maxDepsRunner struct {
	max int
	msg string
}

func (r *maxDepsRunner) Needs() model.Capability { return model.CapManifest }

func (r *maxDepsRunner) Run(snap *model.Snapshot) []model.Finding {
	have := len(snap.Manifest.Direct)
	if have <= r.max {
		return nil
	}
	msg := r.msg
	if msg == "" {
		msg = fmt.Sprintf("this project budgets %d direct dependencies", r.max)
	}
	file := model.FileID("package.json")
	if len(snap.Manifest.Files) > 0 {
		file = snap.Manifest.Files[0]
	}
	return []model.Finding{{
		File:     file,
		Message:  msg,
		Evidence: fmt.Sprintf("%d direct dependencies declared, budget is %d", have, r.max),
		Hint: fmt.Sprintf("Remove %d %s, or raise the budget in the decision and say in `why` "+
			"what changed.", have-r.max, pluralize(have-r.max, "dependency", "dependencies")),
	}}
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
