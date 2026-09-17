// Package rust implements scan.Language for Rust.
//
// Rust resolution is the least mechanical of the languages Jasper supports: a
// path in a `use` statement names items in a module tree, not files, and the
// last segments are usually types rather than modules. So a path is resolved
// longest-prefix-first — crate::a::b::Thing tries a/b/Thing, then a/b, then a —
// and the first prefix that exists on disk wins. That is right for the common
// layout and wrong for a `#[path]` attribute or a module declared inline, which
// is an accepted limitation recorded in docs/languages.md.
package rust

import (
	"path"
	"regexp"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

func init() { scan.Register(Lang{}) }

type Lang struct{}

func (Lang) Name() string { return "rust" }

func (Lang) Match(p string) bool { return strings.HasSuffix(p, ".rs") }

var (
	reUse    = regexp.MustCompile(`(?m)^[ \t]*(?:pub[ \t]+)?use[ \t]+([^;{]+)`)
	reExtern = regexp.MustCompile(`(?m)^[ \t]*extern[ \t]+crate[ \t]+([A-Za-z0-9_]+)`)
)

func (Lang) Imports(src []byte) ([]scan.RawImport, error) {
	code := blank(src)
	var out []scan.RawImport

	for _, m := range reUse.FindAllSubmatchIndex(code, -1) {
		spec := strings.TrimSpace(string(code[m[2]:m[3]]))
		spec = strings.TrimSuffix(spec, "::")
		if spec == "" {
			continue
		}
		out = append(out, scan.RawImport{Spec: spec, Line: lineAt(code, m[2])})
	}
	for _, m := range reExtern.FindAllSubmatchIndex(code, -1) {
		out = append(out, scan.RawImport{
			Spec: string(code[m[2]:m[3]]),
			Line: lineAt(code, m[2]),
		})
	}
	return out, nil
}

func lineAt(src []byte, off int) int {
	return 1 + strings.Count(string(src[:off]), "\n")
}

// blank wipes comments and string literals, preserving offsets and newlines.
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
		case hasAt(src, i, "//"):
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			wipe(i, j)
			i = j

		case hasAt(src, i, "/*"):
			// Rust block comments nest.
			depth, j := 1, i+2
			for j < len(src) && depth > 0 {
				switch {
				case hasAt(src, j, "/*"):
					depth++
					j += 2
				case hasAt(src, j, "*/"):
					depth--
					j += 2
				default:
					j++
				}
			}
			wipe(i, j)
			i = j

		case src[i] == 'r' && (hasAt(src, i, `r"`) || hasAt(src, i, "r#")):
			// Raw string: r"..." or r#"..."# with any number of hashes.
			j := i + 1
			hashes := 0
			for j < len(src) && src[j] == '#' {
				hashes++
				j++
			}
			if j >= len(src) || src[j] != '"' {
				i++
				continue
			}
			j++
			closer := `"` + strings.Repeat("#", hashes)
			for j < len(src) && !hasAt(src, j, closer) {
				j++
			}
			j += len(closer)
			wipe(i, j)
			i = j

		case src[i] == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
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

func (Lang) Manifests() []string { return []string{"Cargo.toml"} }

var reCargoDep = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=\s*(.+)$`)

func (Lang) ParseManifest(_ string, src []byte) (string, map[string]string, error) {
	direct := map[string]string{}
	inDeps := false

	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "#"); i == 0 {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section := strings.Trim(line, "[]")
			// [dependencies], [dev-dependencies], [build-dependencies], and
			// the [dependencies.name] table form.
			if strings.HasSuffix(section, "dependencies") {
				inDeps = true
				continue
			}
			if i := strings.Index(section, "dependencies."); i >= 0 {
				name := section[i+len("dependencies."):]
				if name != "" {
					direct[name] = "*"
				}
				inDeps = false
				continue
			}
			inDeps = false
			continue
		}
		if !inDeps || line == "" {
			continue
		}
		if m := reCargoDep.FindStringSubmatch(line); m != nil {
			direct[m[1]] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
		}
	}
	return "cargo", direct, nil
}

func (Lang) Resolver(root string) scan.Resolver { return &resolver{root: root} }

type resolver struct{ root string }

func (r *resolver) Resolve(from model.FileID, spec string) (model.FileID, bool) {
	segs := strings.Split(spec, "::")
	if len(segs) == 0 {
		return "", false
	}

	switch segs[0] {
	case "crate":
		return r.longestPrefix("src", segs[1:])
	case "self":
		return r.longestPrefix(moduleDir(string(from)), segs[1:])
	case "super":
		return r.longestPrefix(path.Dir(moduleDir(string(from))), segs[1:])
	}
	return "", false // an external crate, std, or a re-export
}

// moduleDir returns the directory a file's own module owns: for src/a/mod.rs
// that is src/a, and for src/a/b.rs it is src/a/b.
func moduleDir(file string) string {
	dir, base := path.Dir(file), path.Base(file)
	switch base {
	case "mod.rs", "lib.rs", "main.rs":
		return dir
	}
	return path.Join(dir, strings.TrimSuffix(base, ".rs"))
}

// longestPrefix walks the path from longest to shortest, because trailing
// segments in a `use` are usually item names rather than modules.
func (r *resolver) longestPrefix(base string, segs []string) (model.FileID, bool) {
	for n := len(segs); n >= 0; n-- {
		rel := path.Join(append([]string{base}, segs[:n]...)...)
		if rel == "" || rel == "." {
			continue
		}
		if scan.FileExists(r.root, rel+".rs") {
			return model.FileID(rel + ".rs"), true
		}
		if scan.FileExists(r.root, path.Join(rel, "mod.rs")) {
			return model.FileID(path.Join(rel, "mod.rs")), true
		}
		if scan.DirExists(r.root, rel) {
			return model.FileID(rel), true
		}
	}
	return "", false
}

// PackageName maps a use path to a crate name. Cargo allows hyphens where Rust
// requires underscores, so serde_json in code may be serde-json in Cargo.toml;
// the underscore form is reported and docs/languages.md notes the caveat.
func (r *resolver) PackageName(spec string) string {
	top := spec
	if i := strings.Index(top, "::"); i >= 0 {
		top = top[:i]
	}
	return strings.TrimSpace(top)
}
