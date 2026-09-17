package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(PublicAPI{}) }

// PublicAPI restricts a module to one entry point for outside callers.
//
// It states the intent directly, where the equivalent no_import states its
// inverse: "nothing may reach past the front door" rather than "nothing may
// import these particular internals". That matters when a module grows — a new
// private directory is covered automatically instead of needing the rule
// widened, which is the failure mode of encoding a boundary as a deny list.
//
//   - public_api:
//     module: "src/identity/**"
//     entry:  "src/identity/index.ts"
type PublicAPI struct{}

func (PublicAPI) Kind() string { return "public_api" }

func (PublicAPI) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "module", "entry", "except", "message", "include_type_only"); err != nil {
		return nil, err
	}
	module, err := requireString(cfg, "module")
	if err != nil {
		return nil, err
	}
	entry := stringList(cfg, "entry")
	if len(entry) == 0 {
		return nil, fmt.Errorf("field %q must name at least one entry point", "entry")
	}
	except := stringList(cfg, "except")
	if err := checkGlobs(append(append([]string{module}, entry...), except...)...); err != nil {
		return nil, err
	}
	return &publicAPIRunner{
		module: glob.MustCompile(module),
		entry:  entry,
		except: except,
		msg: optString(cfg, "message",
			fmt.Sprintf("import %s only through %s", module, entry[0])),
		entryName: entry[0],
		types:     optionalBool(cfg, "include_type_only", false),
	}, nil
}

type publicAPIRunner struct {
	module    *glob.Pattern
	entry     []string
	except    []string
	msg       string
	entryName string
	types     bool
}

func (r *publicAPIRunner) Needs() model.Capability { return model.CapImports }

func (r *publicAPIRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding
	for _, im := range snap.Imports {
		if im.External() {
			continue
		}
		if im.Type && !r.types {
			continue
		}
		// Only imports that land inside the module are governed.
		if !r.module.Match(string(im.To)) {
			continue
		}
		// The module may use itself freely.
		if r.module.Match(string(im.From)) {
			continue
		}
		// Already arriving through the front door.
		if glob.MatchAny(r.entry, string(im.To)) {
			continue
		}
		// `except` exempts importers, matching no_import's rule: exempting a
		// target would let one entry cancel the boundary it sits inside.
		if glob.MatchAny(r.except, string(im.From)) {
			continue
		}
		out = append(out, model.Finding{
			File:     im.From,
			Line:     im.Line,
			Message:  r.msg,
			Evidence: fmt.Sprintf("imports %q -> %s, bypassing %s", im.Spec, im.To, r.entryName),
			Hint: fmt.Sprintf("Export what you need from %s and import that, in the same change.",
				r.entryName),
		})
	}
	return out
}
