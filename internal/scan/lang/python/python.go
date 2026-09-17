// Package python implements scan.Language for Python.
//
// Imports are found by blanking out comments and string literals first, then
// matching import statements on the remaining code. Python's own ast module is
// not available to a Go binary, and a full grammar would mean CGO, which
// DEC-003 rules out. Blanking first is what makes the simple approach correct:
// a module docstring that happens to contain "import requests" is the obvious
// false positive, and it is handled before any matching happens.
package python

import (
	"path"
	"regexp"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

func init() { scan.Register(Lang{}) }

type Lang struct{}

func (Lang) Name() string { return "python" }

func (Lang) Match(p string) bool {
	return strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".pyi")
}

var (
	// import a.b, c as d
	reImport = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+([^\n#]+)`)
	// from .a.b import x   |   from . import x
	reFrom = regexp.MustCompile(`(?m)^[ \t]*from[ \t]+([.\w]+)[ \t]+import[ \t]`)
)

func (Lang) Imports(src []byte) ([]scan.RawImport, error) {
	code := blank(src)
	var out []scan.RawImport

	for _, m := range reFrom.FindAllSubmatchIndex(code, -1) {
		out = append(out, scan.RawImport{
			Spec: string(code[m[2]:m[3]]),
			Line: lineAt(code, m[2]),
		})
	}
	for _, m := range reImport.FindAllSubmatchIndex(code, -1) {
		line := lineAt(code, m[2])
		// "import a.b as c, d" is several imports in one statement.
		for _, part := range strings.Split(string(code[m[2]:m[3]]), ",") {
			name := strings.TrimSpace(part)
			if i := strings.Index(name, " as "); i >= 0 {
				name = strings.TrimSpace(name[:i])
			}
			if name == "" || strings.ContainsAny(name, "()\\") {
				continue
			}
			out = append(out, scan.RawImport{Spec: name, Line: line})
		}
	}
	return out, nil
}

func lineAt(src []byte, off int) int {
	return 1 + strings.Count(string(src[:off]), "\n")
}

// blank replaces the contents of comments and string literals with spaces,
// preserving every byte offset and newline so line numbers stay exact.
func blank(src []byte) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	wipe := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}

	for i := 0; i < len(src); {
		switch {
		case src[i] == '#':
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			wipe(i, j)
			i = j

		case hasAt(src, i, `"""`), hasAt(src, i, `'''`):
			q := src[i : i+3]
			j := i + 3
			for j < len(src) && !hasAt(src, j, string(q)) {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			end := j + 3
			if end > len(src) {
				end = len(src)
			}
			wipe(i, end)
			i = end

		case src[i] == '"' || src[i] == '\'':
			q := src[i]
			j := i + 1
			for j < len(src) && src[j] != q && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(src) {
				j++
			}
			wipe(i, j)
			i = j

		default:
			i++
		}
	}
	return out
}

func hasAt(src []byte, i int, s string) bool {
	return i+len(s) <= len(src) && string(src[i:i+len(s)]) == s
}

func (Lang) Manifests() []string {
	return []string{"requirements.txt", "pyproject.toml", "Pipfile"}
}

var reTOMLDep = regexp.MustCompile(`^\s*["']?([A-Za-z0-9_.\-]+)["']?\s*=`)

func (Lang) ParseManifest(p string, src []byte) (string, map[string]string, error) {
	direct := map[string]string{}
	base := path.Base(p)

	switch base {
	case "requirements.txt":
		for _, line := range strings.Split(string(src), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
				continue // comments, and -r/-e/--flag directives
			}
			if name, ver := splitPin(line); name != "" {
				direct[name] = ver
			}
		}
		return "pip", direct, nil

	case "pyproject.toml", "Pipfile":
		// Deliberately a line scanner, not a TOML parser: the only shapes that
		// matter are a dependencies array and a [*dependencies] table, and a
		// TOML library would be a second dependency.
		section, inArray := "", false
		for _, raw := range strings.Split(string(src), "\n") {
			line := strings.TrimSpace(raw)
			if i := strings.Index(line, "#"); i == 0 {
				continue
			}
			if strings.HasPrefix(line, "[") {
				section, inArray = strings.Trim(line, "[]"), false
				continue
			}
			// dependencies = ["requests>=2", "django"]
			if strings.HasPrefix(line, "dependencies") && strings.Contains(line, "[") {
				inArray = true
				line = line[strings.Index(line, "[")+1:]
			}
			if inArray {
				for _, item := range strings.Split(line, ",") {
					item = strings.Trim(strings.TrimSpace(item), `"'`)
					if item == "" {
						continue
					}
					if name, ver := splitPin(item); name != "" {
						direct[name] = ver
					}
				}
				// The closing bracket only counts outside a string. An extras
				// spec such as "psycopg[binary]>=3.1" carries a ] of its own,
				// and treating that as the end of the array silently drops
				// every dependency listed after it.
				if strings.Contains(outsideQuotes(line), "]") {
					inArray = false
				}
				continue
			}
			// [tool.poetry.dependencies] / [packages]
			if strings.Contains(section, "dependencies") || section == "packages" {
				if m := reTOMLDep.FindStringSubmatch(line); m != nil {
					name := m[1]
					if strings.EqualFold(name, "python") {
						continue // the interpreter is not a dependency
					}
					ver := strings.TrimSpace(line[strings.Index(line, "=")+1:])
					direct[name] = strings.Trim(ver, `"' `)
				}
			}
		}
		mgr := "pip"
		if base == "Pipfile" {
			mgr = "pipenv"
		} else if strings.Contains(string(src), "[tool.poetry") {
			mgr = "poetry"
		}
		return mgr, direct, nil
	}
	return "pip", direct, nil
}

// outsideQuotes blanks the contents of quoted strings so structural characters
// can be found without a TOML parser.
func outsideQuotes(line string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// pyName is the set of characters PyPI allows in a distribution name. It is
// applied as a filter, not a parser: the array scanner above hands over
// whatever sits between two commas, including structural leftovers like a bare
// "]", and anything that cannot be a package name is dropped here.
var pyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// splitPin separates "requests>=2.31" into name and constraint.
func splitPin(s string) (string, string) {
	if i := strings.IndexAny(s, ";["); i >= 0 {
		s = s[:i] // drop environment markers and extras
	}
	s = strings.TrimSpace(s)

	name, ver := s, "*"
	if i := strings.IndexAny(s, "=<>!~ "); i >= 0 {
		name, ver = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	}
	if !pyName.MatchString(name) {
		return "", ""
	}
	return name, ver
}

func (Lang) Resolver(root string) scan.Resolver { return &resolver{root: root} }

type resolver struct{ root string }

// srcRoots are the layouts a package may sit under. Checked in order so that a
// src/ layout resolves before a flat one.
var srcRoots = []string{"", "src/"}

func (r *resolver) Resolve(from model.FileID, spec string) (model.FileID, bool) {
	// Relative: "." is the containing package, ".." its parent, and so on.
	if strings.HasPrefix(spec, ".") {
		dots := 0
		for dots < len(spec) && spec[dots] == '.' {
			dots++
		}
		dir := path.Dir(string(from))
		for i := 1; i < dots; i++ {
			dir = path.Dir(dir)
		}
		rest := strings.TrimLeft(spec, ".")
		if rest == "" {
			return r.tryModule(dir)
		}
		return r.tryModule(path.Join(dir, strings.ReplaceAll(rest, ".", "/")))
	}

	rel := strings.ReplaceAll(spec, ".", "/")
	for _, sr := range srcRoots {
		if id, ok := r.tryModule(sr + rel); ok {
			return id, true
		}
	}
	return "", false
}

// tryModule maps a dotted module path to a file or package directory.
func (r *resolver) tryModule(rel string) (model.FileID, bool) {
	rel = path.Clean(rel)
	if rel == "." || rel == "/" {
		return "", false
	}
	if scan.FileExists(r.root, rel+".py") {
		return model.FileID(rel + ".py"), true
	}
	if scan.FileExists(r.root, path.Join(rel, "__init__.py")) {
		return model.FileID(path.Join(rel, "__init__.py")), true
	}
	if scan.DirExists(r.root, rel) {
		return model.FileID(rel), true
	}
	return "", false
}

// PackageName reduces a dotted specifier to its distribution name. This is
// approximate by nature — "yaml" ships as PyYAML, "cv2" as opencv-python — so
// a decision about such a package should name the import, not the distribution.
func (r *resolver) PackageName(spec string) string {
	top := spec
	if i := strings.Index(top, "."); i >= 0 {
		top = top[:i]
	}
	return top
}
