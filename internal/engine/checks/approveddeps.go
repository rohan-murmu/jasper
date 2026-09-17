package checks

import (
	"fmt"
	"sort"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(ApprovedDependencies{}) }

// ApprovedDependencies requires every declared direct dependency to be covered
// by an allow entry. It exists because the most common thing a coding agent
// does without asking is add a package.
//
// `jasper init` seeds the allow list from what the repo already declares, so
// enabling it never fails on day one; it only fires on what arrives next.
//
//   - approved_dependencies:
//     allow: [react, react-dom, "@types/*", zod]
type ApprovedDependencies struct{}

func (ApprovedDependencies) Kind() string { return "approved_dependencies" }

func (ApprovedDependencies) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "allow", "message"); err != nil {
		return nil, err
	}
	allow := stringList(cfg, "allow")
	if err := checkGlobs(allow...); err != nil {
		return nil, err
	}
	return &approvedDepRunner{
		allow: allow,
		msg:   optString(cfg, "message", "dependency is not covered by any decision"),
	}, nil
}

type approvedDepRunner struct {
	allow []string
	msg   string
}

func (r *approvedDepRunner) Needs() model.Capability { return model.CapManifest }

func (r *approvedDepRunner) Run(snap *model.Snapshot) []model.Finding {
	names := make([]string, 0, len(snap.Manifest.Direct))
	for name := range snap.Manifest.Direct {
		names = append(names, name)
	}
	sort.Strings(names)

	manifestFile := model.FileID("package.json")
	if len(snap.Manifest.Files) > 0 {
		manifestFile = snap.Manifest.Files[0]
	}

	var out []model.Finding
	for _, name := range names {
		if glob.MatchAny(r.allow, name) {
			continue
		}
		out = append(out, model.Finding{
			File:     manifestFile,
			Message:  r.msg,
			Evidence: fmt.Sprintf("%s@%s", name, snap.Manifest.Direct[name]),
			// No `jasper decide` command exists; pointing an agent at one sends
			// it to an "unknown command" exit 2.
			Hint: fmt.Sprintf("Remove %s, or add it to this decision's allow list "+
				"with a note saying why the project needs it.", name),
		})
	}
	return out
}
