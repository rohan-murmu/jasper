package scan

import (
	"fmt"

	"github.com/rohan/jasper/internal/model"
)

// ResolveSpec resolves one import specifier exactly as Scan would, so a caller
// can evaluate an import that has not been written to disk yet.
//
// from need not exist: only its extension (to pick the language) and its
// directory (to anchor relative specifiers) are used. That is what lets an
// agent ask "may the file I am about to create import this?".
func ResolveSpec(root string, from model.FileID, spec string) (model.Import, error) {
	im := model.Import{From: from, Spec: spec}
	lang, ok := For(string(from))
	if !ok {
		return im, fmt.Errorf("no language handles %q — jasper understands Go and TypeScript/JavaScript", from)
	}
	r := lang.Resolver(root)
	if r == nil {
		// No resolver (e.g. Go outside a module): everything is external.
		im.Pkg = spec
		return im, nil
	}
	if to, ok := r.Resolve(from, spec); ok {
		im.To = to
	} else {
		im.Pkg = r.PackageName(spec)
	}
	return im, nil
}
