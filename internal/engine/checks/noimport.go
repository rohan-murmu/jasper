// Package checks contains the check vocabulary. Each file is one primitive.
// Adding a check means adding one file — no existing file changes.
package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(NoImport{}) }

// NoImport forbids an edge between two parts of the repo. This is the workhorse
// for module boundaries and layering.
//
//   - no_import:
//     from: "internal/engine/**"
//     to: "internal/scan/**"
//     except: "internal/engine/adapter.go"
//     message: "The engine is pure; it must not reach into the scanner."
type NoImport struct{}

func (NoImport) Kind() string { return "no_import" }

func (NoImport) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "from", "to", "except", "except_to", "message", "include_type_only"); err != nil {
		return nil, err
	}
	from, err := requireString(cfg, "from")
	if err != nil {
		return nil, err
	}
	to, err := requireString(cfg, "to")
	if err != nil {
		return nil, err
	}
	except := stringList(cfg, "except")
	exceptTo := stringList(cfg, "except_to")
	if err := checkGlobs(append(append([]string{from, to}, except...), exceptTo...)...); err != nil {
		return nil, err
	}
	return &noImportRunner{
		from:     glob.MustCompile(from),
		to:       glob.MustCompile(to),
		except:   except,
		exceptTo: exceptTo,
		msg:      optString(cfg, "message", fmt.Sprintf("%s must not import %s", from, to)),
		types:    optionalBool(cfg, "include_type_only", false),
		title:    d.Title,
		decPath:  d.Path,
	}, nil
}

type noImportRunner struct {
	from, to *glob.Pattern
	except   []string
	exceptTo []string
	msg      string
	types    bool
	title    string
	decPath  string
}

func (r *noImportRunner) Needs() model.Capability { return model.CapImports }

func (r *noImportRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding
	for _, im := range snap.Imports {
		if im.External() {
			continue
		}
		// A type-only import vanishes at runtime, so by default it does not
		// count as a coupling violation.
		if im.Type && !r.types {
			continue
		}
		if !r.from.Match(string(im.From)) || !r.to.Match(string(im.To)) {
			continue
		}
		// `except` exempts importers only. See the note on the type.
		if glob.MatchAny(r.except, string(im.From)) || glob.MatchAny(r.exceptTo, string(im.To)) {
			continue
		}
		out = append(out, model.Finding{
			File:     im.From,
			Line:     im.Line,
			Message:  r.msg,
			Evidence: fmt.Sprintf("imports %q -> %s", im.Spec, im.To),
			Hint:     "Move the shared code behind a public entry point, or record a decision that permits this edge.",
		})
	}
	return out
}
