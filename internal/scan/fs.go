package scan

import (
	"os"
	"path/filepath"
)

// Existence helpers for Resolver implementations. Every language resolver has
// to answer "is there a file here?" while translating a specifier, and each one
// re-deriving the repo-relative-to-OS path conversion is how a Windows bug gets
// written five times.

func FileExists(root, rel string) bool {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && !st.IsDir()
}

func DirExists(root, rel string) bool {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && st.IsDir()
}
