package rust

import (
	"os"
	"path/filepath"
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
	src := []byte("// use fake::commented;\n" +
		"/* use fake::block;\n   /* nested */\n   still a comment: use fake::nested; */\n" +
		"use std::collections::HashMap;\n" +
		"use serde::{Serialize, Deserialize};\n" +
		"pub use crate::engine::Check;\n" +
		"use super::helpers;\n" +
		"use self::inner::Thing;\n" +
		"extern crate libc;\n" +
		"let s = \"use fake::from_string;\";\n" +
		"let r = r#\"use fake::from_raw;\"#;\n")

	got, err := Lang{}.Imports(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"std::collections::HashMap": true,
		"serde":                     true,
		"crate::engine::Check":      true,
		"super::helpers":            true,
		"self::inner::Thing":        true,
		"libc":                      true,
	}
	for _, s := range specs(got) {
		if !want[s] {
			t.Errorf("unexpected import %q — comments and strings must be blanked", s)
		}
		delete(want, s)
	}
	for s := range want {
		t.Errorf("missed import %q", s)
	}
}

func TestParseCargoToml(t *testing.T) {
	src := []byte(`[package]
name = "acme"
version = "0.1.0"

[dependencies]
serde = { version = "1.0", features = ["derive"] }
tokio = "1.38"
# comment = "ignored"

[dev-dependencies]
criterion = "0.5"

[dependencies.sqlx]
version = "0.7"

[profile.release]
lto = true
`)
	mgr, direct, err := Lang{}.ParseManifest("Cargo.toml", src)
	if err != nil {
		t.Fatal(err)
	}
	if mgr != "cargo" {
		t.Errorf("manager = %q, want cargo", mgr)
	}
	for _, want := range []string{"serde", "tokio", "criterion", "sqlx"} {
		if _, ok := direct[want]; !ok {
			t.Errorf("missing %q in %v", want, direct)
		}
	}
	// [package] name and [profile.release] keys must not leak in as deps.
	for _, unwanted := range []string{"name", "version", "lto", "comment"} {
		if _, ok := direct[unwanted]; ok {
			t.Errorf("%q is not a dependency: %v", unwanted, direct)
		}
	}
}

func TestResolver(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"src/lib.rs", "src/engine/mod.rs", "src/engine/checks.rs",
		"src/scan/mod.rs", "src/model.rs",
	} {
		full := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("// x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := Lang{}.Resolver(root)

	cases := []struct {
		from, spec string
		want       model.FileID
		resolves   bool
	}{
		// The trailing segment is a type, so the longest existing prefix wins.
		{"src/lib.rs", "crate::engine::checks::NoImport", "src/engine/checks.rs", true},
		{"src/lib.rs", "crate::engine", "src/engine/mod.rs", true},
		{"src/lib.rs", "crate::model::Snapshot", "src/model.rs", true},
		{"src/engine/checks.rs", "super::mod", "src/engine/mod.rs", true},
		{"src/lib.rs", "serde::Deserialize", "", false},
		{"src/lib.rs", "std::fmt", "", false},
	}
	for _, c := range cases {
		got, ok := r.Resolve(model.FileID(c.from), c.spec)
		if ok != c.resolves {
			t.Errorf("Resolve(%s, %q) resolved=%v, want %v (got %s)", c.from, c.spec, ok, c.resolves, got)
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
		"serde::Deserialize": "serde",
		"tokio":              "tokio",
		"serde_json::Value":  "serde_json",
	} {
		if got := r.PackageName(spec); got != want {
			t.Errorf("PackageName(%q) = %q, want %q", spec, got, want)
		}
	}
}
