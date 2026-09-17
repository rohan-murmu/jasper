// Package scan turns a directory of source files into a model.Snapshot. It is
// the only layer that touches the filesystem.
package scan

import (
	"sort"
	"sync"

	"github.com/rohan/jasper/internal/model"
)

// RawImport is one import statement, before resolution.
type RawImport struct {
	Spec string // the literal specifier: "./foo", "react", "github.com/x/y"
	Line int
	Type bool // TS `import type` — recorded so boundary rules can ignore it
}

// Resolver turns a specifier into a repo path, or reports it as external.
// It is language-specific and usually project-specific (tsconfig paths, the Go
// module path), so a Language builds one per repo root.
type Resolver interface {
	// Resolve maps a specifier to a repo-relative path. ok is false for
	// external packages and unresolvable specifiers.
	Resolve(fromFile model.FileID, spec string) (model.FileID, bool)
	// PackageName reduces an external specifier to the dependency it belongs
	// to: "@scope/pkg/sub" -> "@scope/pkg", "react-dom/client" -> "react-dom".
	PackageName(spec string) string
}

// Language is seam 1 of 4. Adding a language means adding one package and one
// blank import — no existing file changes.
type Language interface {
	Name() string
	Match(path string) bool
	Imports(src []byte) ([]RawImport, error)
	// Resolver may return nil, in which case every import is treated as
	// external.
	Resolver(root string) Resolver
	// Manifests names dependency files this language owns, relative to root.
	Manifests() []string
	// ParseManifest reads one of those files into the declared direct
	// dependency set.
	ParseManifest(path string, src []byte) (manager string, direct map[string]string, err error)
}

var (
	mu   sync.RWMutex
	regs = map[string]Language{}
)

// Register is called from each language package's init().
func Register(l Language) {
	mu.Lock()
	defer mu.Unlock()
	regs[l.Name()] = l
}

func Languages() []Language {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Language, 0, len(regs))
	for _, l := range regs {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func For(path string) (Language, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, l := range regs {
		if l.Match(path) {
			return l, true
		}
	}
	return nil, false
}
