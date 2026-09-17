package typescript

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

func (Lang) Manifests() []string { return []string{"package.json"} }

type pkgJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	PeerDeps        map[string]string `json:"peerDependencies"`
}

func (Lang) ParseManifest(p string, src []byte) (string, map[string]string, error) {
	var pj pkgJSON
	if err := json.Unmarshal(src, &pj); err != nil {
		return "", nil, err
	}
	direct := map[string]string{}
	for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.PeerDeps} {
		for k, v := range m {
			direct[k] = v
		}
	}
	return manager(filepath.Dir(p)), direct, nil
}

func manager(dir string) string {
	for f, name := range map[string]string{
		"pnpm-lock.yaml":    "pnpm",
		"yarn.lock":         "yarn",
		"bun.lockb":         "bun",
		"package-lock.json": "npm",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return name
		}
	}
	return "npm"
}

// Resolver reads tsconfig.json so that alias imports ("@/lib/foo") resolve to
// real files. Alias resolution is the single fiddliest part of TypeScript
// support and the most common source of false positives, so it is handled here
// rather than left to each check.
func (Lang) Resolver(root string) scan.Resolver {
	r := &resolver{root: root, aliases: map[string][]string{}}
	r.loadTSConfig(filepath.Join(root, "tsconfig.json"))
	return r
}

type resolver struct {
	root    string
	baseURL string
	aliases map[string][]string // "@/*" -> ["src/*"]
}

type tsConfig struct {
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
	Extends string `json:"extends"`
}

func (r *resolver) loadTSConfig(p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var cfg tsConfig
	if err := json.Unmarshal(stripJSONC(b), &cfg); err != nil {
		return
	}
	dir, _ := filepath.Rel(r.root, filepath.Dir(p))
	if dir == "." {
		dir = ""
	}
	r.baseURL = path.Join(dir, cfg.CompilerOptions.BaseURL)
	for k, v := range cfg.CompilerOptions.Paths {
		mapped := make([]string, len(v))
		for i, t := range v {
			mapped[i] = path.Join(r.baseURL, t)
		}
		r.aliases[k] = mapped
	}
}

var candidateExts = []string{
	"", ".ts", ".tsx", ".mts", ".cts", ".d.ts", ".js", ".jsx", ".mjs", ".cjs",
}

func (r *resolver) Resolve(from model.FileID, spec string) (model.FileID, bool) {
	switch {
	case strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"), spec == ".", spec == "..":
		return r.tryPath(path.Join(path.Dir(string(from)), spec))
	case strings.HasPrefix(spec, "/"):
		return r.tryPath(strings.TrimPrefix(spec, "/"))
	}
	for pat, targets := range r.aliases {
		if star := strings.IndexByte(pat, '*'); star >= 0 {
			prefix, suffix := pat[:star], pat[star+1:]
			if strings.HasPrefix(spec, prefix) && strings.HasSuffix(spec, suffix) {
				mid := spec[len(prefix) : len(spec)-len(suffix)]
				for _, t := range targets {
					if id, ok := r.tryPath(strings.Replace(t, "*", mid, 1)); ok {
						return id, true
					}
				}
			}
		} else if pat == spec {
			for _, t := range targets {
				if id, ok := r.tryPath(t); ok {
					return id, true
				}
			}
		}
	}
	return "", false // bare specifier: an external package
}

func (r *resolver) tryPath(rel string) (model.FileID, bool) {
	rel = path.Clean(rel)
	for _, e := range candidateExts {
		if st, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(rel+e))); err == nil && !st.IsDir() {
			return model.FileID(rel + e), true
		}
	}
	for _, e := range candidateExts[1:] {
		if st, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(path.Join(rel, "index"+e)))); err == nil && !st.IsDir() {
			return model.FileID(path.Join(rel, "index"+e)), true
		}
	}
	return "", false
}

func (r *resolver) PackageName(spec string) string {
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// stripJSONC removes comments and trailing commas. tsconfig.json is JSON with
// comments in practice, and encoding/json refuses both.
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\n' || b[j] == '\t' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue // trailing comma
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
