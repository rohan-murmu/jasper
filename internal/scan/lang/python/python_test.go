package python

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rohan/jasper/internal/model"
	"github.com/rohan/jasper/internal/scan"
)

var _ scan.Language = Lang{}

func specs(in []scan.RawImport) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.Spec
	}
	return out
}

func TestImports(t *testing.T) {
	src := []byte(`"""Module docstring.

This mentions import requests and from django import x, neither of which
is real. A docstring is the most common false positive in Python.
"""
# import commented_out
import os
import os.path
import numpy as np
import json, csv
from django.db import models
from . import siblings
from .. import grandparent
from .local import thing
from ..pkg.deep import other
s = "import fake_from_string"
t = 'from fake import also'

def later():
    import lazy_import
    return lazy_import
`)
	got, err := Lang{}.Imports(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"os": true, "os.path": true, "numpy": true, "json": true, "csv": true,
		"django.db": true, ".": true, "..": true, ".local": true, "..pkg.deep": true,
		"lazy_import": true,
	}
	for _, s := range specs(got) {
		if !want[s] {
			t.Errorf("unexpected import %q — docstrings, comments and strings must be blanked", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("missed import %q", s)
	}
}

func TestImportLineNumbersAreExact(t *testing.T) {
	src := []byte("x = 1\n\n\nimport requests\n")
	got, err := Lang{}.Imports(src)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, err %v", specs(got), err)
	}
	if got[0].Line != 4 {
		t.Errorf("line = %d, want 4 — blanking must preserve offsets", got[0].Line)
	}
}

func TestParseRequirements(t *testing.T) {
	src := []byte(`# production deps
Django>=4.2,<5
requests==2.31.0
PyYAML
psycopg[binary]>=3.1
uvicorn ; python_version >= "3.9"
-r dev-requirements.txt
-e .
`)
	mgr, direct, err := Lang{}.ParseManifest("requirements.txt", src)
	if err != nil {
		t.Fatal(err)
	}
	if mgr != "pip" {
		t.Errorf("manager = %q, want pip", mgr)
	}
	for _, want := range []string{"Django", "requests", "PyYAML", "psycopg", "uvicorn"} {
		if _, ok := direct[want]; !ok {
			t.Errorf("missing %q in %v", want, direct)
		}
	}
	if len(direct) != 5 {
		t.Errorf("got %d deps, want 5 (-r and -e are directives, not packages): %v", len(direct), direct)
	}
}

func TestParsePyprojectPEP621(t *testing.T) {
	src := []byte(`[project]
name = "acme"
dependencies = ["fastapi>=0.110", "pydantic", "httpx==0.27.0"]

[project.optional-dependencies]
dev = ["pytest"]
`)
	_, direct, err := Lang{}.ParseManifest("pyproject.toml", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fastapi", "pydantic", "httpx"} {
		if _, ok := direct[want]; !ok {
			t.Errorf("missing %q in %v", want, direct)
		}
	}
}

func TestParsePyprojectPoetry(t *testing.T) {
	src := []byte(`[tool.poetry]
name = "acme"

[tool.poetry.dependencies]
python = "^3.11"
requests = "^2.31"
sqlalchemy = { version = "^2.0", extras = ["asyncio"] }
`)
	mgr, direct, err := Lang{}.ParseManifest("pyproject.toml", src)
	if err != nil {
		t.Fatal(err)
	}
	if mgr != "poetry" {
		t.Errorf("manager = %q, want poetry", mgr)
	}
	if _, ok := direct["python"]; ok {
		t.Error("the interpreter is not a dependency")
	}
	for _, want := range []string{"requests", "sqlalchemy"} {
		if _, ok := direct[want]; !ok {
			t.Errorf("missing %q in %v", want, direct)
		}
	}
}

func TestResolver(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"app/__init__.py", "app/main.py", "app/db/__init__.py", "app/db/pool.py",
		"app/billing/invoice.py", "src/lib/util.py",
	} {
		full := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := Lang{}.Resolver(root)

	cases := []struct {
		from, spec string
		want       model.FileID
		resolves   bool
	}{
		{"app/billing/invoice.py", "app.db.pool", "app/db/pool.py", true},
		{"app/billing/invoice.py", "app.db", "app/db/__init__.py", true},
		{"app/billing/invoice.py", "..db.pool", "app/db/pool.py", true},
		{"app/db/pool.py", ".", "app/db/__init__.py", true},
		{"app/main.py", "lib.util", "src/lib/util.py", true}, // src/ layout
		{"app/main.py", "requests", "", false},
		{"app/main.py", "django.db", "", false},
	}
	for _, c := range cases {
		got, ok := r.Resolve(model.FileID(c.from), c.spec)
		if ok != c.resolves {
			t.Errorf("Resolve(%s, %q) resolved=%v, want %v", c.from, c.spec, ok, c.resolves)
			continue
		}
		if ok && got != c.want {
			t.Errorf("Resolve(%s, %q) = %s, want %s", c.from, c.spec, got, c.want)
		}
	}
}

func TestPackageName(t *testing.T) {
	r := Lang{}.Resolver(t.TempDir())
	for spec, want := range map[string]string{
		"django.db.models": "django",
		"requests":         "requests",
		"os.path":          "os",
	} {
		if got := r.PackageName(spec); got != want {
			t.Errorf("PackageName(%q) = %q, want %q", spec, got, want)
		}
	}
}

// Regression: an extras spec carries a ']' of its own. Treating it as the end
// of the dependencies array silently dropped everything listed after it.
func TestParsePyprojectExtrasDoNotTruncateTheArray(t *testing.T) {
	src := []byte(`[project]
dependencies = [
  "fastapi>=0.110",
  "psycopg[binary]>=3.1",
  "httpx==0.27.0",
  "requests>=2.31",
]
`)
	_, direct, err := Lang{}.ParseManifest("pyproject.toml", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fastapi", "psycopg", "httpx", "requests"} {
		if _, ok := direct[want]; !ok {
			t.Errorf("missing %q — the array was truncated at the extras bracket: %v", want, direct)
		}
	}
	if len(direct) != 4 {
		t.Errorf("got %d deps, want 4: %v", len(direct), direct)
	}
}

// The array scanner hands over whatever sits between commas, so junk must be
// filtered rather than trusted.
func TestParseManifestRejectsNonPackageTokens(t *testing.T) {
	for _, junk := range []string{"]", "[", "", "  ", ">=1.0", "-r other.txt"} {
		if name, _ := splitPin(junk); name != "" {
			t.Errorf("splitPin(%q) = %q, want it dropped", junk, name)
		}
	}
	if name, ver := splitPin("requests>=2.31"); name != "requests" || ver != ">=2.31" {
		t.Errorf("splitPin(requests>=2.31) = %q,%q", name, ver)
	}
	if name, _ := splitPin("PyYAML"); name != "PyYAML" {
		t.Errorf("a bare name must survive, got %q", name)
	}
}

// A single-line dependencies array must not lose its last entry. The array's
// closing bracket used to reach the package-name filter attached to the final
// item ("psycopg2\"]"), which silently dropped it — and dropped everything in a
// one-dependency array. Every dependency check reads this map, so a miss here
// is a false negative in exactly the check meant to catch an unapproved
// package.
func TestManifestSingleLineArray(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"three inline", `[project]
dependencies = ["fastapi", "sqlalchemy", "psycopg2"]`,
			[]string{"fastapi", "sqlalchemy", "psycopg2"}},

		{"one inline", `[project]
dependencies = ["requests"]`,
			[]string{"requests"}},

		{"extras carry their own bracket", `[project]
dependencies = ["psycopg[binary]>=3.1", "redis"]`,
			[]string{"psycopg", "redis"}},

		{"multiline still works", `[project]
dependencies = [
  "fastapi",
  "psycopg2",
]`,
			[]string{"fastapi", "psycopg2"}},

		{"trailing content after close", `[project]
dependencies = ["httpx"]  # pinned upstream`,
			[]string{"httpx"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, got, err := Lang{}.ParseManifest("pyproject.toml", []byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d dependencies %v, want %d %v", len(got), mapKeys(got), len(c.want), c.want)
			}
			for _, w := range c.want {
				if _, ok := got[w]; !ok {
					t.Errorf("missing %q (got %v)", w, mapKeys(got))
				}
			}
		})
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
