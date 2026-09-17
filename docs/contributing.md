# Contributing

## Ground rules

```sh
go test -race ./...    # every package
go vet ./...
gofmt -l .             # must print nothing
jasper check           # Jasper must satisfy its own decisions
```

The last one is not ceremonial. `.jasper/decisions/002-layering.yaml` enforces
the dependency direction that the whole design rests on, and
`003-no-cgo.yaml` caps the dependency list at one entry. A change that breaks
either fails its own tool.

Two standards specific to this codebase:

**A check without a rejection test will be asked for one.** Every check must
have a test asserting it *refuses a bad config* — a typo'd field, a missing
required field, a wrong type. A rule that compiles but matches nothing is
invisible, and it is the exact failure mode this project exists to prevent.
Use `mustReject` from `internal/engine/checks/helpers_test.go`.

**Comments explain why, not what.** The codebase is dense with rationale
because most of the decisions in it are non-obvious and would otherwise be
"simplified" away by the next person. `except` exempting importers and never
targets, `init` proposing only rules that already pass, line-framing the MCP
transport — each of those has a comment saying what breaks without it. Match
that.

## Adding a check

One new file in `internal/engine/checks/`. No existing file changes.

### 1. Implement the two-phase interface

```go
package checks

import (
    "fmt"

    "github.com/rohan/jasper/internal/engine"
    "github.com/rohan/jasper/internal/glob"
    "github.com/rohan/jasper/internal/model"
)

func init() { engine.Register(MaxFanOut{}) }

// MaxFanOut caps how many other modules one module may depend on.
//
//   - max_fan_out:
//       within: "src/**"
//       limit: 8
type MaxFanOut struct{}

func (MaxFanOut) Kind() string { return "max_fan_out" }

func (MaxFanOut) Compile(cfg model.RawConfig, d *model.Decision) (engine.Runner, error) {
    // 1. Reject unknown fields FIRST. A typo must be an error, not a no-op.
    if err := knownFields(cfg, "within", "limit", "message"); err != nil {
        return nil, err
    }
    within := optString(cfg, "within", "**")
    if err := checkGlobs(within); err != nil {
        return nil, err
    }
    raw, ok := cfg["limit"]
    if !ok {
        return nil, fmt.Errorf("missing required field %q", "limit")
    }
    limit, ok := raw.(int)          // int, not float64 — see the note below
    if !ok || limit < 1 {
        return nil, fmt.Errorf("field %q must be an integer >= 1", "limit")
    }
    // 2. Compile everything expensive here, once.
    return &maxFanOutRunner{
        within: glob.MustCompile(within),
        limit:  limit,
        msg:    optString(cfg, "message", fmt.Sprintf("a module may depend on at most %d others", limit)),
    }, nil
}

type maxFanOutRunner struct {
    within *glob.Pattern
    limit  int
    msg    string
}

func (r *maxFanOutRunner) Needs() model.Capability { return model.CapImports }

func (r *maxFanOutRunner) Run(snap *model.Snapshot) []model.Finding {
    // Pure. Read only from snap. Never touch the filesystem.
    // Iterate snap.SortedFiles(), never a bare map range — output must be
    // deterministic.
    return nil
}
```

Helpers available in the package: `requireString`, `optString`,
`optionalBool`, `stringList`, `checkGlobs`, `knownFields`.

### 2. Register it

Nothing to do. `init()` puts it in the registry, and
`internal/service/service.go` already blank-imports the whole `checks` package.
`jasper checks` will list it.

### 3. Test it

```go
var _ engine.Check = MaxFanOut{}          // compile-time interface check

func TestMaxFanOutRejectsBadConfig(t *testing.T) {
    mustReject(t, MaxFanOut{}, model.RawConfig{})                          // missing limit
    mustReject(t, MaxFanOut{}, model.RawConfig{"limit": 0})                // out of range
    mustReject(t, MaxFanOut{}, model.RawConfig{"limit": 8, "typo": true})  // unknown field
}

func TestMaxFanOutFlagsAnOverConnectedModule(t *testing.T) {
    snap := snapOf(nil, /* imports... */)
    got := runCheck(t, MaxFanOut{}, model.RawConfig{"limit": 1}, snap)
    // assert count, file, line, and that Hint is actionable
}
```

Snapshot builders in `helpers_test.go`: `snapOf(files, imports...)`,
`snapText(map[FileID]string)`, `snapManifest(direct, files...)`.

### 4. Document it

Add a row to the table in `README.md` and a full section in
[checks.md](checks.md) — every field, its default, and the gotcha if it has one.

### Config types: the one trap

An `enforce` body reaches your `Compile` as `model.RawConfig` (`map[string]any`)
from three different sources with three different Go shapes:

| Source | A list becomes | A number becomes |
|--------|----------------|------------------|
| YAML (`store`) | `[]any` | `int` |
| JSON (MCP `propose_decision`) | `[]any` | `float64` |
| Go literal (`facts` proposals) | `[]string` | `int` |

`service.normalizeJSON` reshapes all three into the YAML form before anything
is validated or written, so `Compile` can assume `[]any` and `int`. Use
`stringList` (which handles a bare string too) rather than type-asserting a
slice yourself.

This is not theoretical: adding validation to the write path once broke
`jasper init` on every real repo, because `facts` hands over `[]string` and
`stringList` only matched `[]any`. There are regression tests
(`TestInitProposalsSurviveValidation`, `TestNormalizeBridgesGoStringSlices`).

### If your check needs file contents

Declare `model.CapFileText` and read through `snap.Text(id)`. Do **not** open
files yourself — that breaks the engine's purity and the layering check will
not catch it, because the import graph looks fine. `Text()` is mutex-guarded
and cached; see
[architecture.md](architecture.md#the-purity-boundary-precisely).

---

## Adding a language

One new package under `internal/scan/lang/`, plus one blank import.

### 1. Implement `scan.Language`

```go
type Language interface {
    Name() string
    Match(path string) bool
    Imports(src []byte) ([]RawImport, error)
    Resolver(root string) Resolver      // may return nil: everything external
    Manifests() []string
    ParseManifest(path string, src []byte) (manager string, direct map[string]string, err error)
}

type Resolver interface {
    Resolve(fromFile model.FileID, spec string) (model.FileID, bool)
    PackageName(spec string) string
}
```

`Imports` must return **exact line numbers** — they end up in PR annotations.
If you strip or rewrite the source before matching, preserve byte offsets. Both
the Python and Rust adapters do this by overwriting comments and string
contents with spaces rather than deleting them.

A file that fails to parse should return an error, not partial results. The
scanner treats that as "contributes no edges" and moves on.

### 2. Worked example: Java

Java is the next obvious language and is mechanical enough to be a good
template. Sketch:

```go
// internal/scan/lang/java/java.go
package java

func init() { scan.Register(Lang{}) }

type Lang struct{}

func (Lang) Name() string        { return "java" }
func (Lang) Match(p string) bool { return strings.HasSuffix(p, ".java") }

// import com.acme.billing.Invoice;  /  import static ...;  /  import com.acme.*;
var reImport = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(?:static[ \t]+)?([\w.*]+)[ \t]*;`)

func (Lang) Imports(src []byte) ([]scan.RawImport, error) {
    code := blank(src)   // wipe // and /* */ comments and "..." strings, keep offsets
    var out []scan.RawImport
    for _, m := range reImport.FindAllSubmatchIndex(code, -1) {
        out = append(out, scan.RawImport{
            Spec: string(code[m[2]:m[3]]),
            Line: 1 + strings.Count(string(code[:m[2]]), "\n"),
        })
    }
    return out, nil
}

func (Lang) Manifests() []string { return []string{"pom.xml"} }

// pom.xml is XML, so encoding/xml from the stdlib handles it — no new
// dependency. Walk <dependencies><dependency><groupId>/<artifactId> and
// report "groupId:artifactId", which is how a Java decision would name a
// dependency anyway.
func (Lang) ParseManifest(_ string, src []byte) (string, map[string]string, error) { ... }

// A package maps to a directory, so com.acme.billing.Invoice resolves under
// src/main/java/com/acme/billing/Invoice.java. Try the standard source roots,
// and fall back to the package directory for a wildcard import.
func (Lang) Resolver(root string) scan.Resolver { ... }
```

What would still need deciding, and should go in a comment:

- `build.gradle` / `build.gradle.kts` are not XML. Either write a line scanner
  for the `dependencies { }` block or declare Gradle unsupported in
  [languages.md](languages.md) — both are defensible, silently returning zero
  dependencies is not.
- Source roots vary (`src/main/java`, `src/test/java`, plain `src/`). Enumerate
  the ones you support.
- `import com.acme.*` is a package, not a file. Resolving it to the directory is
  right, and matches how the Go adapter already behaves.

### 3. Register it

Add one line to `internal/service/service.go`:

```go
_ "github.com/rohan/jasper/internal/scan/lang/java"
```

That is the only existing file that changes.

### 4. Test it

Model the tests on `internal/scan/lang/python/python_test.go`:

- **Imports**, with the false positives explicitly present in the fixture — an
  import inside a comment, inside a string, inside a docstring. Assert they are
  *not* found.
- **Exact line numbers** after whatever stripping you do.
- **Manifest parsing**, including the awkward real-world shapes. The Python
  adapter had a bug where `psycopg[binary]>=3.1` truncated the dependency array
  at its `]`; the regression test for it is
  `TestParsePyprojectExtrasDoNotTruncateTheArray`.
- **Resolution**, against real files in a `t.TempDir()`.
- `var _ scan.Language = Lang{}`.

### 5. Add a fixture

A small project under `testdata/` with its own `.jasper/` and a decision that
fires. The nested-`.jasper` rule means it is skipped by Jasper's own scan, so
it costs nothing. See `testdata/py-service` and `testdata/rs-service`.

### 6. Teach `init` about it

`internal/facts/facts.go` holds two lookup tables — `datastores` and
`capabilities` — keyed by package name. Add the language's ecosystem so
`jasper init` can recognise a datastore choice and spot duplicate
capabilities. Without this, `init` on a project in your language only ever
proposes a dependency allow list.

Also update the support matrix in [languages.md](languages.md), including a
**Limits** subsection. An honest limitation list is worth more than a broad
claim; every adapter here has one.

---

## Adding a port

A port is a package under `internal/ports/` that parses input, calls exactly
one `service` method per operation, and renders. It holds **no** business
logic. That rule is what made the MCP port ~700 lines instead of a refactor.

```
internal/ports/cli/    argv → service → text | json | github annotations
internal/ports/mcp/    JSON-RPC 2.0 over stdio → service → text results
```

Constraints, enforced by `.jasper/decisions/002-layering.yaml`:

- A port must not import `scan` or `store`. Go through `service`.
- If you need a capability `service` does not expose, add a method to
  `service` — do not reach around it. `CanImport` and `CanAddDependency` were
  added to `service` for the MCP port, not implemented inside it.

The obvious next port is LSP: diagnostics in the editor as you type, which is
the only surface earlier than the MCP pre-flight.

## Adding a provider

Not built. The seam is reserved for an LLM-backed design mode that would author
decisions from a design conversation rather than observing them from code —
`model.OriginAuthored` exists for exactly that.

The hard constraint to preserve: `jasper init` must never invent an
architecture. Day one uses no LLM, because a model hallucinating your existing
structure on first run destroys trust permanently. A provider would be a
separate, explicitly-invoked mode, never part of `init`.

## Project layout

```
cmd/jasper/            entrypoint
internal/              everything else — see architecture.md
.jasper/decisions/     Jasper's decisions about Jasper
testdata/              fixtures, each with its own .jasper
docs/                  this directory
.github/workflows/     build, then jasper check --format github
.mcp.json              MCP registration for working on Jasper itself
```
