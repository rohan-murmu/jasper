# Language support

Four languages. Each is one package under `internal/scan/lang/`, registered
through the `scan.Language` seam.

| Language | Extensions | Import extraction | Manifests | Exactness |
|----------|-----------|-------------------|-----------|-----------|
| Go | `.go` | `go/parser`, stdlib | `go.mod` | exact |
| TypeScript / JavaScript | `.ts .tsx .mts .cts .js .jsx .mjs .cjs` | hand-written tokenizer | `package.json` | very high |
| Python | `.py .pyi` | blank-then-match | `requirements.txt`, `pyproject.toml`, `Pipfile` | high |
| Rust | `.rs` | blank-then-match | `Cargo.toml` | good for the common layout |

## Why not a real parser

`.jasper/decisions/003-no-cgo.yaml` caps Jasper at pure Go with no CGO. A real
grammar for four languages means tree-sitter, which means CGO, which makes
every cross-compile a build-matrix problem and every install a support ticket.
Jasper runs on every commit and in a pre-commit hook; it must start in
milliseconds and install without a toolchain.

The cost is accepted knowingly, and the `scan.Language` seam exists so a single
language can be swapped to tree-sitter later without touching anything else.

Go is the exception and is parsed exactly, because `go/parser` is in the
standard library. That is also why Go was the first language supported — and
why Jasper can check itself with no caveats.

## The two extraction strategies

**Tokenizer (TypeScript).** A real tokenizer that tracks comments, strings,
template literals and regex literals, then looks at what precedes each string
literal. Handles `import x from 'y'`, `export * from 'y'`, bare
`import 'y'`, `require('y')` and `await import('y')`, and marks
`import type` separately.

**Blank-then-match (Python, Rust).** Comments and string literals are
overwritten with spaces — preserving every byte offset, so line numbers stay
exact — and then import statements are matched on what remains. This is what
makes the simple approach correct: a Python module docstring containing
"import requests", or a Rust doc comment containing `use fake::thing;`, is the
obvious false positive and it is removed before any matching happens.

```python
"""Module docstring mentioning import requests, which is not real."""
# import commented_out
import os                    # ← only this one is found
s = "import fake_from_string"
```

Rust's nested block comments (`/* /* */ */`) and raw strings (`r#"..."#`) are
both handled.

---

## Go

**Imports.** `parser.ParseFile(..., parser.ImportsOnly)`. Exact.

**Manifest.** `go.mod`. Both the `require (...)` block and single-line
`require` form. Lines marked `// indirect` are skipped: Jasper governs direct
dependencies, because a transitive dependency is not a decision anyone made.

**Resolution.** The module path is read from `go.mod`. An import of
`github.com/you/mod/internal/scan` resolves to the **directory**
`internal/scan`, because Go imports name packages, not files. This is why the
glob dialect makes a trailing `/**` match the directory itself — `internal/scan/**`
has to match `internal/scan`.

`PackageName` reduces an external import to `host/org/repo`, so
`github.com/spf13/cobra/doc` becomes `github.com/spf13/cobra`.

**Limits.**
- `_test.go` files **are** scanned, so a test importing across a boundary is a
  finding. Usually right, occasionally surprising; exempt them with
  `except: ["**/*_test.go"]`.
- Build tags are ignored. A file excluded by `//go:build` still contributes
  edges.
- `vendor/` is skipped by the walker entirely.

## TypeScript / JavaScript

**Manifest.** `package.json` — `dependencies`, `devDependencies` and
`peerDependencies` are merged into one declared set. The package manager is
inferred from which lockfile sits beside it (`pnpm-lock.yaml`, `yarn.lock`,
`bun.lockb`, `package-lock.json`), defaulting to npm.

**Resolution.** Relative (`./x`, `../x`), root-absolute (`/x`), and
`tsconfig.json` path aliases (`@/*` → `src/*`). Candidate extensions and
`index` files are tried in order, so `./pool` finds `pool.ts`, `pool.d.ts` or
`pool/index.ts`. `tsconfig.json` is read through a JSONC stripper because
tsconfig is JSON-with-comments in practice and `encoding/json` refuses both
comments and trailing commas.

Alias resolution is the fiddliest part of TS support and the most common source
of false positives, so it lives in the resolver rather than being left to each
check.

**Limits.**
- **`tsconfig.json` `extends` is not followed.** The field is parsed and then
  ignored, so a project whose `paths` live in a base config gets no aliases and
  those imports are reported as external packages. This is the most likely
  cause of a surprising result in a monorepo.
- Only the root `tsconfig.json` is read; per-package configs in a workspace are
  not.
- A dynamic specifier built from a template literal is not resolvable, by
  construction.
- Manifests inside `node_modules/` are excluded; other nested `package.json`
  files do contribute dependencies, shallowest-first.

## Python

**Imports.** `import a.b`, `import a as b`, `import a, b`, `from a.b import c`,
and relative forms `from . import x`, `from ..pkg.deep import y`. Imports
inside functions are found too, since the match is not anchored to column zero.

**Manifests.**
- `requirements.txt` — one requirement per line; `#` comments and `-r` / `-e` /
  `--flag` directives skipped; extras and environment markers stripped
  (`psycopg[binary]>=3.1; python_version >= "3.9"` → `psycopg`).
- `pyproject.toml` — PEP 621 `[project] dependencies = [...]` and
  `[tool.poetry.dependencies]`. Reported manager is `poetry` when a
  `[tool.poetry` table is present, else `pip`.
- `Pipfile` — `[packages]`, reported as `pipenv`.

TOML is read with a line scanner rather than a parser, because the only shapes
that matter are a dependencies array and a `[*dependencies]` table, and a TOML
library would be a second dependency. Names are validated against the PyPI
character set, so structural leftovers cannot become phantom packages.

**Resolution.** Relative imports count leading dots and walk up from the
importing file's directory. Absolute dotted paths are tried against the repo
root and then a `src/` root. A module resolves to `pkg/mod.py`, else
`pkg/mod/__init__.py`, else the directory.

**Limits.**
- **Import name ≠ distribution name.** `yaml` ships as `PyYAML`, `cv2` as
  `opencv-python`, `sklearn` as `scikit-learn`. `forbid_dependency` matches the
  manifest name; `confine` on an import matches the import name. When they
  differ, say which one you mean in the decision's `why`.
- Namespace packages (no `__init__.py`) resolve only if the directory exists.
- `setup.py` is not parsed — a project that declares dependencies only in
  `install_requires` will show none.
- Conditional imports in `try/except ImportError` are reported as ordinary
  edges.

## Rust

**Imports.** `use ...;`, `pub use ...;`, and `extern crate ...;`.

**Manifest.** `Cargo.toml` — `[dependencies]`, `[dev-dependencies]`,
`[build-dependencies]`, and the `[dependencies.name]` table form. Both
`name = "1.0"` and `name = { version = "1.0", features = [...] }` shapes.

**Resolution.** `crate::` anchors at `src/`, `self::` at the importing file's
own module directory, `super::` at its parent. A path is then resolved
**longest-prefix-first**: `crate::engine::checks::NoImport` tries
`src/engine/checks/NoImport`, then `src/engine/checks`, then `src/engine`, and
the first that exists on disk wins. This is necessary because the trailing
segments of a `use` are usually item names rather than modules.

`src/a/mod.rs` owns module directory `src/a`; `src/a/b.rs` owns `src/a/b`.

**Limits.**
- `#[path = "..."]` attributes are not honoured.
- Modules declared inline (`mod foo { ... }`) are not modelled; only file
  modules resolve.
- A workspace root `Cargo.toml` with `[workspace] members` does not pull in
  member manifests. Run Jasper per crate, or give each crate its own `.jasper`.
- **Crate names may use `-` where code uses `_`.** `serde_json` in a `use` maps
  to the `serde_json` crate, but a crate published as `rand-chacha` is written
  `rand_chacha` in code. `PackageName` reports the underscore form, so a
  `forbid_dependency` entry should list both spellings if you are unsure.
- `use x as y;` reports `x`, which is correct; the alias is not tracked.
- Items reached through a macro are invisible.

---

## What no language sees

Worth stating plainly, because it bounds what any decision can enforce:

- A raw SQL string. `forbid_dependency: [mongodb]` catches the driver, not a
  hand-rolled wire protocol.
- A shell-out. `exec("psql ...")` is invisible to the import graph.
- A dynamically constructed import path.
- Anything behind reflection, code generation at build time, or a plugin
  loaded at runtime.

Jasper's unit of analysis is the declared dependency and the static import
edge. `forbid_text` is the escape hatch when a decision needs to police
something the graph cannot see, and it is a regexp, not a parser.

## Adding a language

One package, one blank import, no existing file changes. The full recipe with a
worked Java example: **[contributing.md](contributing.md#adding-a-language)**.
