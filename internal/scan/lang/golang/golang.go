// Package golang implements scan.Language for Go using the standard library
// parser. No CGO, no third-party grammar, and exactly correct — which is why
// Go is the first language Jasper supports and why Jasper can check itself.
package golang

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

func init() { scan.Register(Lang{}) }

type Lang struct{}

func (Lang) Name() string { return "go" }

func (Lang) Match(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") ||
		strings.HasSuffix(p, "_test.go")
}

func (Lang) Imports(src []byte) ([]scan.RawImport, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	out := make([]scan.RawImport, 0, len(f.Imports))
	for _, im := range f.Imports {
		spec, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, scan.RawImport{Spec: spec, Line: fset.Position(im.Pos()).Line})
	}
	return out, nil
}

func (Lang) Manifests() []string { return []string{"go.mod"} }

func (Lang) ParseManifest(_ string, src []byte) (string, map[string]string, error) {
	direct := map[string]string{}
	inBlock := false
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			// "// indirect" marks a transitive dep; Jasper only governs direct ones.
			if strings.Contains(line[i:], "indirect") {
				continue
			}
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "require (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			if name, ver, ok := splitReq(line); ok {
				direct[name] = ver
			}
		case strings.HasPrefix(line, "require "):
			if name, ver, ok := splitReq(strings.TrimPrefix(line, "require ")); ok {
				direct[name] = ver
			}
		}
	}
	return "go", direct, nil
}

func splitReq(s string) (string, string, bool) {
	f := strings.Fields(s)
	if len(f) < 2 {
		return "", "", false
	}
	return f[0], f[1], true
}

func (Lang) Resolver(root string) scan.Resolver {
	mod := modulePath(root)
	if mod == "" {
		return nil
	}
	return &resolver{root: root, module: mod}
}

func modulePath(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

type resolver struct {
	root   string
	module string
}

// Resolve maps an in-module import to its package directory. Go imports name
// packages, not files, so the returned FileID is a directory — checks match it
// with globs, which works for both shapes.
func (r *resolver) Resolve(_ model.FileID, spec string) (model.FileID, bool) {
	if spec == r.module {
		return model.FileID("."), true
	}
	if !strings.HasPrefix(spec, r.module+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(spec, r.module+"/")
	if st, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(rel))); err != nil || !st.IsDir() {
		return "", false
	}
	return model.FileID(path.Clean(rel)), true
}

func (r *resolver) PackageName(spec string) string {
	parts := strings.Split(spec, "/")
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		return strings.Join(parts[:3], "/") // host/org/repo
	}
	return spec
}

var _ = fmt.Sprintf
