package checks

import (
	"fmt"

	"github.com/rohan/jasper/internal/glob"
	"github.com/rohan/jasper/internal/model"
)

func requireString(cfg model.RawConfig, key string) (string, error) {
	v, ok := cfg[key]
	if !ok {
		return "", fmt.Errorf("missing required field %q", key)
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("field %q must be a non-empty string", key)
	}
	return s, nil
}

func optString(cfg model.RawConfig, key, def string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func optionalBool(cfg model.RawConfig, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// stringList accepts a bare string or a list; both read naturally in YAML.
func stringList(cfg model.RawConfig, key string) []string {
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

func checkGlobs(pats ...string) error {
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

// knownFields rejects unrecognised keys, so a typo in a decision file is an
// error rather than a rule that silently enforces nothing.
func knownFields(cfg model.RawConfig, keys ...string) error {
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
