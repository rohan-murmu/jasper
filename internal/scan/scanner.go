package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/rohan/jasper/internal/model"
)

// Scanner walks a repository and produces a model.Snapshot. It is the only
// component that reads source files; everything downstream is pure.
type Scanner struct {
	Root    string
	Ignore  []string
	MaxSize int64
}

var defaultIgnore = []string{
	".git", "node_modules", "vendor", "dist", "build", "out", "target",
	".next", ".nuxt", ".venv", "venv", "__pycache__", "coverage", ".turbo", ".cache",
}

func New(root string) *Scanner {
	return &Scanner{Root: root, Ignore: defaultIgnore, MaxSize: 2 << 20}
}

func (s *Scanner) Scan() (*model.Snapshot, error) {
	paths, err := s.walk()
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", s.Root, err)
	}

	snap := &model.Snapshot{
		Root:  s.Root,
		Rev:   gitRev(s.Root),
		Files: make(map[model.FileID]*model.File, len(paths)),
	}
	snap.SetLoader(func(id model.FileID) []byte {
		b, _ := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(string(id))))
		return b
	})

	// One resolver per language per repo: they read tsconfig.json / go.mod, so
	// building them per file would be wasteful.
	resolvers := map[string]Resolver{}
	for _, l := range Languages() {
		if r := l.Resolver(s.Root); r != nil {
			resolvers[l.Name()] = r
		}
	}

	type job struct {
		path string
		lang Language
	}
	var jobs []job
	manifests := map[string]Language{}

	for _, p := range paths {
		if lang, ok := For(p); ok {
			jobs = append(jobs, job{p, lang})
			continue
		}
		base := filepath.Base(p)
		for _, l := range Languages() {
			for _, m := range l.Manifests() {
				// Only root-level and workspace manifests count as declaring
				// direct dependencies.
				if base == m && !strings.Contains(p, "node_modules/") {
					manifests[p] = l
				}
			}
		}
	}

	var (
		mu      sync.Mutex
		imports []model.Import
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, runtime.NumCPU())

	for _, j := range jobs {
		st, err := os.Stat(filepath.Join(s.Root, filepath.FromSlash(j.path)))
		if err != nil || st.Size() > s.MaxSize {
			continue
		}
		id := model.FileID(j.path)
		snap.Files[id] = &model.File{ID: id, Lang: j.lang.Name(), Size: st.Size()}

		wg.Add(1)
		sem <- struct{}{}
		go func(j job, id model.FileID) {
			defer wg.Done()
			defer func() { <-sem }()

			src, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(j.path)))
			if err != nil {
				return
			}
			raws, err := j.lang.Imports(src)
			if err != nil {
				return // a file that does not parse contributes no edges
			}
			res := resolvers[j.lang.Name()]
			local := make([]model.Import, 0, len(raws))
			for _, ri := range raws {
				im := model.Import{From: id, Spec: ri.Spec, Line: ri.Line, Type: ri.Type}
				if res != nil {
					if to, ok := res.Resolve(id, ri.Spec); ok {
						im.To = to
					} else {
						im.Pkg = res.PackageName(ri.Spec)
					}
				}
				local = append(local, im)
			}
			mu.Lock()
			imports = append(imports, local...)
			mu.Unlock()
		}(j, id)
	}
	wg.Wait()

	sort.Slice(imports, func(i, j int) bool {
		if imports[i].From != imports[j].From {
			return imports[i].From < imports[j].From
		}
		return imports[i].Line < imports[j].Line
	})
	snap.Imports = imports
	snap.Manifest = s.readManifests(manifests)
	snap.Modules = inferModules(snap)
	snap.Hash = hashSnapshot(snap)
	return snap, nil
}

func (s *Scanner) readManifests(found map[string]Language) model.Manifest {
	m := model.Manifest{Direct: map[string]string{}}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	// Shallowest manifest wins as the manager label; all contribute deps.
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		mgr, direct, err := found[p].ParseManifest(p, b)
		if err != nil {
			continue
		}
		if m.Manager == "" {
			m.Manager = mgr
		}
		for k, v := range direct {
			if _, exists := m.Direct[k]; !exists {
				m.Direct[k] = v
			}
		}
		m.Files = append(m.Files, model.FileID(p))
	}
	return m
}

// inferModules clusters files by their top two path segments under a source
// root. This is a heuristic used only to give `init` something to show; no
// check depends on it.
func inferModules(snap *model.Snapshot) []model.Module {
	counts := map[string]int{}
	for id := range snap.Files {
		parts := strings.Split(string(id), "/")
		if len(parts) < 2 {
			continue
		}
		key := parts[0]
		if (key == "src" || key == "internal" || key == "pkg" || key == "lib" || key == "app") && len(parts) > 2 {
			key = parts[0] + "/" + parts[1]
		}
		counts[key]++
	}
	out := make([]model.Module, 0, len(counts))
	for k, n := range counts {
		if n < 2 {
			continue
		}
		name := k
		if i := strings.LastIndex(k, "/"); i >= 0 {
			name = k[i+1:]
		}
		out = append(out, model.Module{Name: name, Glob: k + "/**", Files: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Glob < out[j].Glob
	})
	return out
}

// hashSnapshot produces the cache key. Findings are a pure function of the
// snapshot hash and the decision-set hash, so identical inputs need no work.
func hashSnapshot(snap *model.Snapshot) string {
	h := sha256.New()
	ids := make([]string, 0, len(snap.Files))
	for id := range snap.Files {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := snap.Files[model.FileID(id)]
		fmt.Fprintf(h, "%s|%s|%d\n", f.ID, f.Lang, f.Size)
	}
	for _, im := range snap.Imports {
		fmt.Fprintf(h, "%s>%s|%s|%d\n", im.From, im.To, im.Spec, im.Line)
	}
	keys := make([]string, 0, len(snap.Manifest.Direct))
	for k := range snap.Manifest.Direct {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "dep:%s@%s\n", k, snap.Manifest.Direct[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *Scanner) walk() ([]string, error) {
	var out []string
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		rel, rerr := filepath.Rel(s.Root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if s.ignored(d.Name(), rel) {
				return filepath.SkipDir
			}
			// A directory with its own .jasper is a separate project: a test
			// fixture, an example, or a workspace member that governs itself.
			// Its dependencies and files are not this project's.
			if st, err := os.Stat(filepath.Join(p, ".jasper")); err == nil && st.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

func (s *Scanner) ignored(name, rel string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, ig := range s.Ignore {
		if name == ig || rel == ig {
			return true
		}
	}
	return false
}

// gitRev returns HEAD only when the tree is clean. A dirty tree has no stable
// revision, so the cache must not be keyed on one.
func gitRev(root string) string {
	if out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output(); err != nil || len(out) > 0 {
		return ""
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
