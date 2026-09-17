package checks

import (
	"fmt"
	"sort"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(ForbidDependency{}) }

// ForbidDependency bans packages outright. This is how a technology choice
// becomes enforceable: "we chose PostgreSQL" is prose, "mongodb must never
// appear in the manifest" is a rule.
//
//   - forbid_dependency:
//     packages: [mongodb, mongoose, "dynamodb*", mysql2]
//     message: "Persistence is PostgreSQL (see why)."
type ForbidDependency struct{}

func (ForbidDependency) Kind() string { return "forbid_dependency" }

func (ForbidDependency) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "packages", "message"); err != nil {
		return nil, err
	}
	pkgs := stringList(cfg, "packages")
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("field \"packages\" must list at least one package")
	}
	if err := checkGlobs(pkgs...); err != nil {
		return nil, err
	}
	return &forbidDepRunner{
		pkgs: pkgs,
		msg:  optString(cfg, "message", "forbidden dependency"),
	}, nil
}

type forbidDepRunner struct {
	pkgs []string
	msg  string
}

// Both the manifest and the import graph are consulted: a package can be
// vendored or transitively present without being declared, and an import of it
// is just as much a violation of the decision.
func (r *forbidDepRunner) Needs() model.Capability {
	return model.CapManifest | model.CapImports
}

func (r *forbidDepRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding

	names := make([]string, 0, len(snap.Manifest.Direct))
	for name := range snap.Manifest.Direct {
		names = append(names, name)
	}
	sort.Strings(names)

	manifestFile := model.FileID("package.json")
	if len(snap.Manifest.Files) > 0 {
		manifestFile = snap.Manifest.Files[0]
	}
	for _, name := range names {
		if !glob.MatchAny(r.pkgs, name) {
			continue
		}
		out = append(out, model.Finding{
			File:     manifestFile,
			Message:  r.msg,
			Evidence: fmt.Sprintf("declared dependency %s@%s", name, snap.Manifest.Direct[name]),
			Hint:     fmt.Sprintf("Remove %s, or supersede the decision that forbids it.", name),
		})
	}

	seen := map[string]bool{}
	for _, im := range snap.Imports {
		if !im.External() || im.Pkg == "" || !glob.MatchAny(r.pkgs, im.Pkg) {
			continue
		}
		key := string(im.From) + "|" + im.Pkg
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.Finding{
			File:     im.From,
			Line:     im.Line,
			Message:  r.msg,
			Evidence: fmt.Sprintf("imports %q", im.Spec),
			Hint:     fmt.Sprintf("Remove the import of %s, or supersede the decision that forbids it.", im.Pkg),
		})
	}
	return out
}
