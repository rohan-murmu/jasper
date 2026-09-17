package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"src/**", "src/a/b.ts", true},
		{"src/**", "src/a.ts", true},
		{"src/**", "lib/a.ts", false},
		// A trailing /** governs the directory itself, not only its children.
		{"src/**", "src", true},
		{"internal/scan/**", "internal/scan", true},
		{"internal/scan/**", "internal/scanner", false},
		{"internal/scan/**", "internal/scan/lang/golang", true},
		{"src/identity/internal/**", "src/identity/internal/session.ts", true},
		{"src/identity/internal/**", "src/identity/index.ts", false},
		{"internal/**/*.go", "internal/model/snapshot.go", true},
		{"internal/**/*.go", "internal/snapshot.go", true},
		{"*.json", "package.json", true},
		{"*.json", "a/package.json", false},
		{"src/*/index.ts", "src/billing/index.ts", true},
		{"src/*/index.ts", "src/billing/sub/index.ts", false},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}
