# Architecture

## The one equation

Everything in Jasper is arranged to protect this:

```
(repo bytes, .jasper/ files) → Snapshot → []Finding
```

A pure function with a thin impure shell. `scan` reads the filesystem once and
produces an immutable `Snapshot`; every check is a function of that snapshot
and nothing else. That property is what lets the identical engine run in a CLI,
an MCP server, a CI job, and (later) a batch over hundreds of repositories
without modification — and it is why the MCP port could answer "may I import
this?" for a file that does not exist: a hypothetical is just a second
`Snapshot`.

The property survives only while the dependency direction does. One import from
`engine` into `scan` makes the engine impure and every guarantee above is gone
quietly, in a single commit nobody reviews carefully. So it is enforced, by
Jasper, on Jasper: `.jasper/decisions/002-layering.yaml`.

## Package map

```
cmd/jasper/            main() — 9 lines, os.Exit(cli.Main(os.Args[1:]))
│
└── internal/
    ├── ports/         L4  argv or JSON-RPC in, rendered output out
    │   ├── cli/           commands, text/json/github renderers
    │   └── mcp/           JSON-RPC 2.0 over stdio, 7 tools
    ├── service/       L3  orchestration + composition root
    ├── engine/        L2  PURE: Snapshot → []Finding
    │   └── checks/        one file per check primitive
    ├── facts/         L2  PURE: Snapshot → []Fact (observations for init)
    ├── scan/          L1  the ONLY package that reads source files
    │   └── lang/          go · typescript · python · rust
    ├── store/         L1  .jasper/*.yaml → []*Decision
    ├── glob/          L0  path-matching dialect with **
    └── model/         L0  zero dependencies
```

## It is a diamond, not a stack

The tidy L0–L4 ladder above is how the layers *read*. It is not the whole
constraint, and the difference matters enough that it cost a wrong refactor to
learn.

```
                    ports
                      │
                   service
          ┌─────┬──────┴──────┬─────┐
          │     │             │     │
        scan  store        engine  facts
          └─────┴──────┬──────┴─────┘
                       │
                  model · glob
```

`scan`, `store`, `engine` and `facts` are **peers**. None of them may import
another. `service` is the only package that knows all four: it calls `scan` to
build a snapshot, `store` to load decisions, then hands both to `engine`.

This is why a purely directional rule cannot express Jasper's own layering.
`engine → scan` is *downward* in the ladder, so a direction-only check permits
it — but it is forbidden, because **the engine must stay pure** and `scan` does
I/O. That is a property constraint, not an ordering constraint. Likewise "ports
must not skip past service" is a hop rule, not an ordering.

The `layers` check handles direction and peer isolation (two glob groups in one
tier may not import each other). The constraints that are about *properties*
stay explicit `no_import` rules. When you read
`.jasper/decisions/002-layering.yaml` and wonder why it is seven rules instead
of one tidy `layers` block, this is the reason.

## The purity boundary, precisely

"The engine is pure" means: no package under `engine/` or `facts/` imports
`os`, `net`, `exec`, `scan`, `store`, or `service`. You can verify it:

```sh
go list -f '{{join .Imports "\n"}}' ./internal/engine/... | sort -u
```

There is exactly one place the engine reaches outside its `Snapshot`, and it is
worth understanding rather than hiding:

```go
// model/snapshot.go
func (s *Snapshot) SetLoader(fn func(FileID) []byte)
func (s *Snapshot) Text(id FileID) []byte
```

The `forbid_text` check needs file contents, which no snapshot field carries —
holding every file's bytes would make the snapshot unbounded on a large repo.
So `scan` injects a loader closure, and `Text()` calls it lazily.

That is dependency injection, not a leak. The engine still imports no I/O
package; a test supplies text with `snap.SetLoader(func(id) []byte {...})` and
never touches a filesystem. The honest phrasing is: **the engine is pure given
its injected loader.** If that ever stops being true — if a check starts opening
paths itself — the property is gone and the layering decision should be
tightened to say so.

`Text()` caches what it loads, and `Engine.Run` executes checks concurrently,
so the cache is mutex-guarded. Without the mutex two text checks on one
snapshot are a concurrent map write; `TestForbidTextConcurrentRunsAreSafe`
pins it and fails under `-race` if the lock is removed.

## Core types

All in `model`, which imports only `sort`, `sync` and `fmt`.

```go
type Snapshot struct {          // immutable view of a repo at one revision
    Root     string
    Rev      string             // git sha, or "" when the tree is dirty
    Files    map[FileID]*File
    Imports  []Import           // every edge, sorted
    Modules  []Module           // heuristic, for init's display only
    Manifest Manifest           // DECLARED direct deps, not the resolved tree
    Hash     string             // cache key (computed, not yet used)
}

type Import struct {            // one edge
    From FileID
    To   FileID                 // "" when external
    Spec string                 // the literal specifier
    Pkg  string                 // package name when external
    Line int
    Type bool                   // TS `import type` — no runtime coupling
}

type Decision struct {          // why / brief / enforce
    ID, Title    string
    Status       Status         // accepted | proposed | superseded
    Origin       Origin         // observed | proposed | authored
    Why, Brief   string
    Enforce      []EnforceRule
    Scope        *Scope         // grandfathering
}

type Finding struct {           // one violation, actionable without a human
    Decision, Rule string
    File           FileID
    Line           int
    Message        string       // the decision's own wording
    Hint           string       // what to do about it
    Evidence       string       // what was observed
    Severity       Severity
}
```

Two deliberate choices:

**`RawConfig map[string]any`.** An `enforce` rule body arrives undecoded.
`model` has no YAML dependency, `store` owns the YAML knowledge, and each check
types its own config at compile time. The cost is that the same rule reaches the
engine as three different Go shapes depending on the source — YAML gives
`[]any`, JSON gives `float64` numbers, a Go literal from `facts` gives
`[]string` — which is why `service.normalizeJSON` exists as a single
reshaping boundary. Loosening every check to accept every shape instead was the
alternative, and it is worse.

**`Manifest.Direct` is declared, not resolved.** Jasper cares what the project
*asked for*, because that is what a decision covers. A transitive dependency is
not a decision anyone made.

## The four seams

Only four extension points, by design.

| Seam | Interface | To extend |
|------|-----------|-----------|
| **Language** | `scan.Language` | new package under `scan/lang/` + `init(){scan.Register}` + one blank import in `service` |
| **Check** | `engine.Check` / `engine.Runner` | new file in `engine/checks/` + `init(){engine.Register}` |
| **Port** | *(no interface — a package that calls `service`)* | new package under `ports/` |
| **Provider** | *not yet built* | reserved for LLM-backed design mode (`OriginAuthored`) |

Both registries are maps populated by `init()`, so adding either changes no
existing file except one blank import line in `service/service.go`:

```go
import (
    _ "github.com/rohan/jasper/internal/engine/checks"
    _ "github.com/rohan/jasper/internal/scan/lang/golang"
    _ "github.com/rohan/jasper/internal/scan/lang/python"
    _ "github.com/rohan/jasper/internal/scan/lang/rust"
    _ "github.com/rohan/jasper/internal/scan/lang/typescript"
)
```

`service` is the composition root. Ports never register anything.

### Check, in two phases

```go
type Check interface {
    Kind() string
    Compile(cfg model.RawConfig, d *model.Decision) (Runner, error)
}
type Runner interface {
    Needs() model.Capability
    Run(*model.Snapshot) []model.Finding
}
```

`Compile` runs once, at load time, and validates configuration. A typo in a
decision file fails immediately with a filename instead of silently enforcing
nothing — the single most important property in the codebase, because a rule
that compiles but matches nothing is invisible. Every check calls
`knownFields(...)` and rejects unrecognised keys.

`Run` is then a closure over already-parsed, already-compiled config: globs are
compiled once, regexps once.

`Needs()` declares which parts of the snapshot a check reads. It is currently
**advisory** — `Engine.Run` does not yet consult it to skip work. It is
implemented on every runner so the optimisation is a one-place change when a
50k-file repo makes it worth doing.

## Determinism

Findings are sorted by `(decision, file, line)` before returning. The scanner
sorts its walk, sorts imports by `(from, line)`, and sorts manifest keys before
hashing. Checks that iterate files use `Snapshot.SortedFiles()` rather than
ranging a map.

This is not tidiness. Non-deterministic output means a CI diff that changes
without the code changing, and `TestForbidTextFindingsAreDeterministic` runs the
same check six times to assert map iteration order never leaks into results.

## Concurrency

Two fan-outs, both bounded by `runtime.NumCPU()`:

- `scan.Scan` parses files in parallel, collecting imports under a mutex.
- `Engine.Run` executes compiled checks in parallel, collecting findings under a
  mutex, then sorts.

Everything else is sequential. The MCP server handles one frame at a time;
interleaved writes to stdout would corrupt the protocol stream, and a local
stdio server has no throughput problem worth that risk.

## What is computed but not yet used

Being explicit so nobody mistakes these for working features:

- **`Snapshot.Hash`, `store.Hash`, `Report.Cached`** — a cache key exists;
  there is no cache. Every run is a full scan.
- **`Capability` / `Needs()`** — declared by every runner, read by nothing.
- **`Scope.Since`** — parsed from YAML, never applied. Only `Scope.Exclude`
  filters findings.
- **`store.Config.Include/Exclude/Packs`** — loaded from `jasper.yaml`, never
  passed to the scanner. The ignore list is hardcoded in `scan/scanner.go`.

## Further reading

- **[workflows.md](workflows.md)** — how the pieces run, end to end
- **[checks.md](checks.md)** — the check vocabulary
- **[contributing.md](contributing.md)** — adding to a seam
