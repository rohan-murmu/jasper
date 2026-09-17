package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(RequireImport{}) }

// RequireImport asserts that every file in a set imports something.
//
// Every other check in Jasper forbids an edge. This one requires one, which is
// what "no route is unauthenticated" and "every handler goes through the error
// wrapper" actually mean. Those are the decisions whose violation is a security
// incident rather than a tidiness problem, and a deny list cannot express them:
// you cannot enumerate the ways a file might fail to call the auth middleware.
//
//   - require_import:
//     in: "src/routes/**"
//     imports: "src/middlewares/verifyToken.js"
//     except: ["src/routes/public.route.js"]
//     message: "every route must verify the token"
//
// `imports` matches a repo path or an external package name, so it works for
// "must use our logger package" as well as "must use our local middleware".
type RequireImport struct{}

func (RequireImport) Kind() string { return "require_import" }

func (RequireImport) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "in", "imports", "except", "message"); err != nil {
		return nil, err
	}
	in, err := requireString(cfg, "in")
	if err != nil {
		return nil, err
	}
	want := stringList(cfg, "imports")
	if len(want) == 0 {
		return nil, fmt.Errorf("field %q must name at least one import", "imports")
	}
	except := stringList(cfg, "except")
	if err := checkGlobs(append(append([]string{in}, want...), except...)...); err != nil {
		return nil, err
	}
	return &requireImportRunner{
		in:      glob.MustCompile(in),
		want:    want,
		except:  except,
		msg:     optString(cfg, "message", fmt.Sprintf("every file in %s must import %s", in, want[0])),
		wantOne: want[0],
	}, nil
}

type requireImportRunner struct {
	in      *glob.Pattern
	want    []string
	except  []string
	msg     string
	wantOne string
}

func (r *requireImportRunner) Needs() model.Capability { return model.CapImports }

func (r *requireImportRunner) Run(snap *model.Snapshot) []model.Finding {
	// Index the satisfied files in one pass: iterating every import per file
	// would be quadratic on a large repo.
	satisfied := map[model.FileID]bool{}
	for _, im := range snap.Imports {
		target := im.Pkg
		if !im.External() {
			target = string(im.To)
		}
		if target != "" && glob.MatchAny(r.want, target) {
			satisfied[im.From] = true
		}
	}

	var out []model.Finding
	for _, id := range snap.SortedFiles() {
		path := string(id)
		if !r.in.Match(path) || glob.MatchAny(r.except, path) || satisfied[id] {
			continue
		}
		out = append(out, model.Finding{
			File:     id,
			Message:  r.msg,
			Evidence: fmt.Sprintf("does not import %s", r.wantOne),
			Hint: fmt.Sprintf("Import %s here, or add this file to the decision's `except` list "+
				"with a reason.", r.wantOne),
		})
	}
	return out
}
