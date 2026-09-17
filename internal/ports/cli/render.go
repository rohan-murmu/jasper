package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rohan/jasper/internal/engine"
	"github.com/rohan/jasper/internal/model"
)

type style struct{ dim, red, yellow, green, bold, cyan, reset string }

func newStyle(w io.Writer) style {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || !isTTY(w) {
		return style{}
	}
	return style{
		dim: "\033[2m", red: "\033[31m", yellow: "\033[33m", green: "\033[32m",
		bold: "\033[1m", cyan: "\033[36m", reset: "\033[0m",
	}
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// renderText groups findings by decision, because a developer fixes a
// violation by understanding the decision behind it, not by reading a flat list
// of file paths.
func renderText(w io.Writer, rep *engine.Report, titles map[string]string, paths map[string]string) {
	s := newStyle(w)

	if len(rep.Findings) == 0 {
		fmt.Fprintf(w, "%s✓%s %d checks passed %s· %d decisions · %d files · %dms%s\n",
			s.green, s.reset, rep.Checks, s.dim, rep.Decisions, rep.Files, rep.ElapsedMS, s.reset)
		return
	}

	var order []string
	byDecision := map[string][]model.Finding{}
	failed := map[string]bool{} // distinct decision+rule pairs that fired
	for _, f := range rep.Findings {
		if _, seen := byDecision[f.Decision]; !seen {
			order = append(order, f.Decision)
		}
		byDecision[f.Decision] = append(byDecision[f.Decision], f)
		failed[f.Decision+"/"+f.Rule] = true
	}

	for _, id := range order {
		fs := byDecision[id]
		fmt.Fprintf(w, "\n%s✗%s %s%s%s  %s\n", s.red, s.reset, s.bold, id, s.reset, titles[id])
		for _, f := range fs {
			fmt.Fprintf(w, "    %s%s%s\n", s.cyan, f.Location(), s.reset)
			if f.Evidence != "" {
				fmt.Fprintf(w, "      %s%s%s\n", s.dim, f.Evidence, s.reset)
			}
			fmt.Fprintf(w, "      %s\n", f.Message)
		}
		if h := fs[0].Hint; h != "" {
			fmt.Fprintf(w, "    %s→ %s%s\n", s.dim, h, s.reset)
		}
		if p := paths[id]; p != "" {
			fmt.Fprintf(w, "    %swhy → %s%s\n", s.dim, p, s.reset)
		}
	}

	word := "violations"
	if rep.Errors() == 1 {
		word = "violation"
	}
	fmt.Fprintf(w, "\n%s%d %s%s · %d checks passed %s· %d files · %dms%s\n",
		s.red, rep.Errors(), word, s.reset, rep.Checks-len(order), s.dim, rep.Files, rep.ElapsedMS, s.reset)
}

// renderGitHub emits workflow commands so violations appear inline on the pull
// request diff, which is where the developer already is.
func renderGitHub(w io.Writer, rep *engine.Report, titles map[string]string) {
	for _, f := range rep.Findings {
		level := "error"
		if f.Severity == model.SevWarn {
			level = "warning"
		}
		msg := fmt.Sprintf("%s (%s): %s", f.Decision, titles[f.Decision], f.Message)
		if f.Hint != "" {
			msg += " — " + f.Hint
		}
		line := f.Line
		if line < 1 {
			line = 1
		}
		fmt.Fprintf(w, "::%s file=%s,line=%d,title=%s::%s\n",
			level, f.File, line, f.Decision, escapeGH(msg))
	}
}

func escapeGH(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}
