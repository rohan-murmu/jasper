// Package store loads and writes decision files. Decisions live in .jasper/ as
// committed YAML: git already provides versioning, diffing, branching and
// review, so Jasper builds none of those.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rohan/jasper/internal/model"
	"gopkg.in/yaml.v3"
)

const Dir = ".jasper"

type Store struct{ Root string }

func New(root string) *Store { return &Store{Root: root} }

func (s *Store) Path(parts ...string) string {
	return filepath.Join(append([]string{s.Root, Dir}, parts...)...)
}

func (s *Store) Exists() bool {
	st, err := os.Stat(s.Path())
	return err == nil && st.IsDir()
}

// Config is .jasper/jasper.yaml.
type Config struct {
	Version int      `yaml:"version"`
	Name    string   `yaml:"name,omitempty"`
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
	Packs   []string `yaml:"packs,omitempty"`
}

func (s *Store) LoadConfig() (*Config, error) {
	b, err := os.ReadFile(s.Path("jasper.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Version: 1}, nil
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("jasper.yaml: %w", err)
	}
	return &c, nil
}

// decisionFile mirrors model.Decision but keeps `enforce` as raw YAML so each
// check can type its own configuration at compile time. model stays
// dependency-free; the YAML knowledge lives here.
type decisionFile struct {
	model.Decision `yaml:",inline"`
	Enforce        []map[string]yaml.Node `yaml:"enforce"`
}

// Load reads every decision in .jasper/decisions. A malformed file is an error,
// not a warning: silently ignoring a decision means silently dropping a rule.
func (s *Store) Load() ([]*model.Decision, error) {
	dir := s.Path("decisions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*model.Decision
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml")) {
			continue
		}
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var df decisionFile
		if err := yaml.Unmarshal(b, &df); err != nil {
			return nil, fmt.Errorf("%s: %w", relTo(s.Root, p), err)
		}
		d := df.Decision
		d.Path = relTo(s.Root, p)
		if d.ID == "" {
			return nil, fmt.Errorf("%s: missing id", d.Path)
		}
		if d.Status == "" {
			d.Status = model.StatusAccepted
		}
		for _, item := range df.Enforce {
			for kind, node := range item {
				var cfg model.RawConfig
				if err := node.Decode(&cfg); err != nil {
					return nil, fmt.Errorf("%s: enforce %s: %w", d.Path, kind, err)
				}
				d.Enforce = append(d.Enforce, model.EnforceRule{Kind: kind, Cfg: cfg})
			}
		}
		out = append(out, &d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Hash is the cache key for the decision set.
func Hash(ds []*model.Decision) string {
	h := sha256.New()
	for _, d := range ds {
		fmt.Fprintf(h, "%s|%s|%s\n", d.ID, d.Status, d.SupersededBy)
		for _, e := range d.Enforce {
			fmt.Fprintf(h, "  %s|%v\n", e.Kind, e.Cfg)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *Store) Init(cfg *Config) error {
	if err := os.MkdirAll(s.Path("decisions"), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path("jasper.yaml"), b, 0o644)
}

// WriteDecision serialises a decision, preserving the enforce block's
// single-key-map shape so hand-edited and generated files look identical.
func (s *Store) WriteDecision(d *model.Decision, enforceRaw []map[string]any) (string, error) {
	slug := strings.ToLower(strings.NewReplacer(" ", "-", "/", "-", ",", "").Replace(d.Title))
	if len(slug) > 48 {
		slug = slug[:48]
	}
	num := strings.TrimPrefix(d.ID, "DEC-")
	path := s.Path("decisions", fmt.Sprintf("%s-%s.yaml", num, slug))

	type out struct {
		model.Decision `yaml:",inline"`
		Enforce        []map[string]any `yaml:"enforce,omitempty"`
	}
	b, err := yaml.Marshal(out{Decision: *d, Enforce: enforceRaw})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return relTo(s.Root, path), nil
}

// NextID returns the next free DEC- identifier.
func NextID(ds []*model.Decision) string {
	max := 0
	for _, d := range ds {
		var n int
		if _, err := fmt.Sscanf(d.ID, "DEC-%d", &n); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("DEC-%03d", max+1)
}

func relTo(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}
