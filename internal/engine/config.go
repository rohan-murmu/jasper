package engine

import (
	"fmt"

	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

// Small typed accessors over model.RawConfig. model deliberately has no YAML
// dependency, so enforce-rule bodies arrive as plain maps and are typed here.

func str(cfg model.RawConfig, key string) (string, bool) {
	v, ok := cfg[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func reqStr(cfg model.RawConfig, key string) (string, error) {
	s, ok := str(cfg, key)
	if !ok || s == "" {
		return "", fmt.Errorf("missing required field %q", key)
	}
	return s, nil
}

func optStr(cfg model.RawConfig, key, def string) string {
	if s, ok := str(cfg, key); ok {
		return s
	}
	return def
}

func optBool(cfg model.RawConfig, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// strList accepts both a bare string and a list, because both read naturally in
// a decision file.
func strList(cfg model.RawConfig, key string) []string {
	v, ok := cfg[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func validateGlobs(pats ...string) error {
	for _, p := range pats {
		if p == "" {
			continue
		}
		if _, err := glob.Compile(p); err != nil {
			return fmt.Errorf("bad pattern %q: %w", p, err)
		}
	}
	return nil
}

// known rejects unrecognised keys so a typo in a decision file is an error
// rather than a rule that silently does nothing.
func known(cfg model.RawConfig, keys ...string) error {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	for k := range cfg {
		if !allowed[k] {
			return fmt.Errorf("unknown field %q (allowed: %v)", k, keys)
		}
	}
	return nil
}
