package checks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(ForbidText{}) }

// ForbidText forbids a regular expression from appearing in a set of files.
//
// Not every decision is about the import graph. "Configuration is read once, in
// config/" and "no debug logging in production paths" are architectural
// choices with a real cost when they erode, and both are invisible to every
// other check here.
//
//   - forbid_text:
//     pattern: "process\\.env"
//     in: "src/**"
//     except: ["src/configs/**"]
//     message: "read configuration through src/configs, not process.env"
//
// This is the only check that reads file contents, so it declares CapFileText
// and pays for the text it asks for.
type ForbidText struct{}

func (ForbidText) Kind() string { return "forbid_text" }

func (ForbidText) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
	if err := knownFields(cfg, "pattern", "in", "except", "message"); err != nil {
		return nil, err
	}
	pattern, err := requireString(cfg, "pattern")
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	in := optString(cfg, "in", "**")
	except := stringList(cfg, "except")
	if err := checkGlobs(append([]string{in}, except...)...); err != nil {
		return nil, err
	}
	return &forbidTextRunner{
		re:     re,
		src:    pattern,
		in:     glob.MustCompile(in),
		except: except,
		msg:    optString(cfg, "message", fmt.Sprintf("%q must not appear in %s", pattern, in)),
	}, nil
}

type forbidTextRunner struct {
	re     *regexp.Regexp
	src    string
	in     *glob.Pattern
	except []string
	msg    string
}

func (r *forbidTextRunner) Needs() model.Capability { return model.CapFileText }

func (r *forbidTextRunner) Run(snap *model.Snapshot) []model.Finding {
	var out []model.Finding
	for _, id := range snap.SortedFiles() {
		path := string(id)
		if !r.in.Match(path) || glob.MatchAny(r.except, path) {
			continue
		}
		body := snap.Text(id)
		if len(body) == 0 || !r.re.Match(body) {
			continue // cheap reject before the per-line walk
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !r.re.MatchString(line) {
				continue
			}
			out = append(out, model.Finding{
				File:     id,
				Line:     i + 1,
				Message:  r.msg,
				Evidence: fmt.Sprintf("matches /%s/: %s", r.src, excerpt(line)),
				Hint:     "Move this behind the boundary the decision names, or record an exception.",
			})
		}
	}
	return out
}

// excerpt trims a source line down to something readable in a terminal and in
// a PR annotation.
func excerpt(line string) string {
	s := strings.TrimSpace(line)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
