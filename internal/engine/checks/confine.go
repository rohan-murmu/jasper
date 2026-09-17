package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(Confine{}) }

// Confine restricts where a package may be imported from. It is how a decision
// like "the database driver stays behind the repository layer" stops being a
// convention and starts being a rule.
//
//   - confine:
//     package: pg
//     to: "src/db/**"
type Confine struct{}

func (Confine) Kind() string { return "confine" }

func (Confine) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "package", "packages", "to", "message"); err != nil {
		return nil, err
	}
	pkgs := stringList(cfg, "packages")
	if p, err := requireString(cfg, "package"); err == nil {
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("provide \"package\" or \"packages\"")
	}
	to, err := requireString(cfg, "to")
	if err != nil {
		return nil, err
	}
	if err := checkGlobs(append(pkgs, to)...); err != nil {
		return nil, err
	}
	return &confineRunner{
		pkgs: pkgs,
		to:   glob.MustCompile(to),
		toS:  to,
		msg:  optString(cfg, "message", fmt.Sprintf("this package may only be imported from %s", to)),
	}, nil
}

type confineRunner struct {
	pkgs []string
	to   *glob.Pattern
	toS  string
	msg  string
}

func (r *confineRunner) Needs() model.Capability { return model.CapImports }

func (r *confineRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding
	for _, im := range snap.Imports {
		name := im.Pkg
		if !im.External() {
			name = string(im.To)
		}
		if name == "" || !glob.MatchAny(r.pkgs, name) {
			continue
		}
		if r.to.Match(string(im.From)) {
			continue
		}
		out = append(out, model.Finding{
			File:     im.From,
			Line:     im.Line,
			Message:  r.msg,
			Evidence: fmt.Sprintf("imports %q from outside %s", im.Spec, r.toS),
			Hint:     fmt.Sprintf("Access it through the layer at %s instead of importing it directly.", r.toS),
		})
	}
	return out
}
