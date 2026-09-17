// Package model holds the core data types. It has zero dependencies, by design:
// everything above it (scan, engine, service, ports) may import model, and model
// imports nothing. See .jasper/decisions/001-layering.yaml.
package model

import (
	"sort"
	"sync"
)

// FileID is a repo-relative, slash-separated path. For file-oriented languages
// (TypeScript) it names a file; for package-oriented languages (Go) an import
// target may name a directory. Checks match it with globs, so both work.
type FileID string

// Capability describes what part of a Snapshot a check needs. The engine uses
// it to skip expensive work: a lockfile-only check must not force a full import
// graph on a 50k-file repo.
type Capability uint8

const (
	CapImports Capability = 1 << iota
	CapManifest
	CapFileText
)

func (c Capability) Has(o Capability) bool { return c&o != 0 }

// Snapshot is the complete, immutable view of a repository at one revision.
// Everything the engine sees comes from here. Producing it is the only part of
// the pipeline that touches the filesystem.
type Snapshot struct {
	Root     string           `json:"root"`
	Rev      string           `json:"rev"` // git sha, or "" when the tree is dirty
	Files    map[FileID]*File `json:"files"`
	Imports  []Import         `json:"imports"`
	Modules  []Module         `json:"modules"`
	Manifest Manifest         `json:"manifest"`
	Hash     string           `json:"hash"`

	// mu guards text. Engine.Run executes checks concurrently, so any two
	// text-reading checks would otherwise race on the cache and crash with a
	// concurrent map write.
	mu     sync.Mutex
	text   map[FileID][]byte // lazily loaded, see Text()
	loader func(FileID) []byte
}

type File struct {
	ID   FileID `json:"id"`
	Lang string `json:"lang"`
	Size int64  `json:"size"`
}

// Import is one edge of the graph. To is empty for external packages, in which
// case Pkg names the package ("react", "github.com/spf13/cobra").
type Import struct {
	From FileID `json:"from"`
	To   FileID `json:"to,omitempty"`
	Spec string `json:"spec"`
	Pkg  string `json:"pkg,omitempty"`
	Line int    `json:"line"`
	Type bool   `json:"type,omitempty"` // type-only import (TS `import type`)
}

func (i Import) External() bool { return i.To == "" }

type Module struct {
	Name  string `json:"name"`
	Glob  string `json:"glob"`
	Files int    `json:"files"`
}

// Manifest is the declared dependency set, not the resolved tree. Jasper cares
// about what the project asked for, because that is what a decision covers.
type Manifest struct {
	Manager string            `json:"manager"` // "go", "npm", "pnpm", "yarn"
	Direct  map[string]string `json:"direct"`  // name -> version constraint
	Files   []FileID          `json:"files"`
}

// SetLoader installs the lazy file-text reader. Snapshot deliberately does not
// hold source text: most checks never need it, and holding it would make the
// cached snapshot unbounded on large repos.
func (s *Snapshot) SetLoader(fn func(FileID) []byte) { s.loader = fn }

// Text returns a file's bytes, loading it once and caching the result. Safe
// for concurrent use.
//
// This is the one place the engine reaches outside its Snapshot, and it does so
// through a loader injected by scan — so the engine still imports no I/O
// package and a test can supply text without a filesystem. See
// docs/architecture.md for why that distinction is drawn where it is.
func (s *Snapshot) Text(id FileID) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.text == nil {
		s.text = map[FileID][]byte{}
	}
	if b, ok := s.text[id]; ok {
		return b
	}
	var b []byte
	if s.loader != nil {
		b = s.loader(id)
	}
	s.text[id] = b
	return b
}

// SortedFiles returns every file id in a stable order. Checks that walk files
// need this: map iteration order would make findings non-deterministic.
func (s *Snapshot) SortedFiles() []FileID {
	out := make([]FileID, 0, len(s.Files))
	for id := range s.Files {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ImportsFrom returns every edge originating at id.
func (s *Snapshot) ImportsFrom(id FileID) []Import {
	var out []Import
	for _, im := range s.Imports {
		if im.From == id {
			out = append(out, im)
		}
	}
	return out
}
