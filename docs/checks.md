# Check reference

Ten primitives. `jasper checks` lists the registered kinds; each is one file in
`internal/engine/checks/`.

Every check rejects unrecognised fields at load time. A typo is an error with a
filename, not a rule that silently enforces nothing:

```
jasper: .jasper/decisions/001-boundaries.yaml: DEC-001: no_import:
  unknown field "mesage" (allowed: [from to except except_to message include_type_only])
```

## The glob dialect

Used by every path field. Implemented in `internal/glob` — `path.Match` has no
`**`, and a dependency for sixty lines of regexp translation was not worth it.

| Pattern | Matches |
|---------|---------|
| `**/` | zero or more leading path segments |
| `**` | anything, including separators |
| `*` | anything except a separator |
| `?` | one character except a separator |

One rule worth knowing: **a trailing `/**` also matches the directory itself.**
`src/db/**` matches both `src/db` and `src/db/pool.ts`. This exists because Go
imports resolve to package *directories*, and a rule about a directory should
govern the directory. `internal/scan/**` matches `internal/scan` but not
`internal/scanner`.

An empty `except` list matches nothing, which is the safe default.

---

## Import-graph checks

### `no_import`

Forbids a specific edge. The workhorse.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `from` | yes | — | glob: the importing file |
| `to` | yes | — | glob: the import target |
| `except` | no | none | globs: **importers** exempted |
| `except_to` | no | none | globs: targets exempted |
| `message` | no | `"<from> must not import <to>"` | shown on violation |
| `include_type_only` | no | `false` | also flag TS `import type` |

```yaml
- no_import:
    from: "internal/engine/**"
    to: "internal/scan/**"
    message: "the engine is pure — it must never read the filesystem"
```

**`except` exempts importers, never targets.** This is deliberate and was a
real bug once: exempting a target lets one broad exception silently cancel the
rule it sits inside. `except_to` exists for when you genuinely mean the target,
and it is named differently so you cannot reach for it by accident.

Type-only imports are skipped by default: `import type { X }` vanishes at
runtime and is not a coupling. Set `include_type_only: true` when you are
policing conceptual dependencies rather than runtime ones.

### `layers`

Declares a dependency direction for the whole repo in one rule.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `order` | yes | — | list of tiers, **lowest first**; each entry is a glob or a list of globs |
| `allow_peers` | no | `false` | permit imports between different groups in the same tier |
| `include_type_only` | no | `false` | also flag TS `import type` |
| `message` | no | `"layers depend downward only"` | shown on violation |

```yaml
- layers:
    order:
      - "app/core/**"
      - "app/db/**"
      - "app/services/**"
      - "app/api/**"
```

A file may import its own group and anything below it. Importing **upward** is
always a violation. Within one tier, each glob is a **peer group**: files in the
same group import each other freely, but two different groups in the same tier
may not — that is how you say "scan and engine both sit below service, and
neither may see the other":

```yaml
- layers:
    order:
      - ["internal/model/**", "internal/glob/**"]
      - ["internal/scan/**", "internal/engine/**"]   # peers: mutually invisible
      - "internal/service/**"
    allow_peers: false    # the default
```

Files matching no tier are ignored, so a partial declaration is useful on day
one.

**What `layers` cannot express.** Anything that is not about direction. "The
engine must not import the scanner because the engine must stay pure" is a
*property*, and the scanner sits *below* the engine — a direction-only rule
permits it. "Ports must not skip past service" is a hop rule, not an ordering.
Both stay explicit `no_import` rules. See
[architecture.md](architecture.md#it-is-a-diamond-not-a-stack).

### `public_api`

Restricts a module to one entry point for outside callers.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `module` | yes | — | glob: what the boundary encloses |
| `entry` | yes | — | glob or list: the permitted front door(s) |
| `except` | no | none | globs: **importers** exempted |
| `message` | no | `"import <module> only through <entry>"` | shown on violation |
| `include_type_only` | no | `false` | also flag TS `import type` |

```yaml
- public_api:
    module: "src/identity/**"
    entry: "src/identity/index.ts"
    message: "use the identity public API"
```

Prefer this over `no_import` for module boundaries. It states the intent
directly — "nothing may reach past the front door" — where `no_import` states
the inverse, "nothing may import these particular internals". The difference
shows when the module grows: a new private directory is covered automatically,
instead of needing the rule widened. Encoding a boundary as a deny list means
the boundary decays every time someone adds a folder.

The module may always use its own internals, or the rule would make the module
unimplementable.

### `no_cycles`

Forbids circular dependencies between directories.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `within` | no | `"**"` | glob: only consider files matching this |
| `depth` | no | `2` | path segments used to group files into nodes |
| `message` | no | `"circular dependency between modules"` | shown on violation |

```yaml
- no_cycles:
    within: "internal/**"
    depth: 2
```

Collapses the file graph to a directory graph at `depth` segments, then runs
Tarjan's algorithm; components larger than one are cycles. One finding is
emitted per participating edge, so every file in the loop is actionable rather
than one opaque finding for the whole cycle.

File-level cycles are common and often harmless. Directory-level cycles mean
two modules cannot be understood, tested or extracted independently.

**Note for Go projects:** the compiler already rejects package import cycles,
so the value here is catching cycles between *subsystems* that the compiler
permits — `engine/checks → scan/lang/golang` while `scan/scanner.go → engine`
is two distinct packages and compiles fine, but the two trees are now
mutually entangled. In TypeScript and Python, cycles compile and ship, then
fail intermittently at module-init time.

### `confine`

Restricts where a package may be imported from.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `package` | one of | — | a single package name or glob |
| `packages` | one of | — | a list of them |
| `to` | yes | — | glob: the only place it may be imported |
| `message` | no | `"this package may only be imported from <to>"` | shown on violation |

```yaml
- confine:
    package: pg
    to: "src/db/**"
    message: "the database driver stays behind the db layer"
```

How "the database driver stays behind the repository layer" stops being a
convention and starts being a rule. Matches external package names *and*
internal paths, so it also works for "this generated client is only touched by
one module".

### `require_import`

Asserts that every file in a set imports something. The only check that
requires an edge rather than forbidding one.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `in` | yes | — | glob: files governed by the rule |
| `imports` | yes | — | glob or list: what they must import |
| `except` | no | none | globs: files exempted |
| `message` | no | `"every file in <in> must import <imports>"` | shown on violation |

```yaml
- require_import:
    in: "src/routes/**"
    imports: "src/middlewares/verifyToken.js"
    except: ["src/routes/public.route.js"]
    message: "every route must verify the token"
```

"No route is unauthenticated" and "every handler goes through the error
wrapper" are decisions whose violation is an incident rather than a tidiness
problem, and a deny list cannot express them — you cannot enumerate the ways a
file might fail to call the auth middleware.

`imports` matches a repo path or an external package name, so it covers "must
use our logger package" too.

---

## Manifest checks

These read the **declared** direct dependencies, not the resolved tree. Jasper
cares what the project asked for, because that is what a decision covers.

### `forbid_dependency`

Bans packages outright.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `packages` | yes | — | list of names or globs (at least one) |
| `message` | no | `"forbidden dependency"` | shown on violation |

```yaml
- forbid_dependency:
    packages: [mongodb, mongoose, mysql2, "dynamodb*"]
    message: "this project stores data in PostgreSQL"
```

How a technology choice becomes enforceable: "we chose PostgreSQL" is prose,
"mongodb must never appear" is a rule.

**Checks the manifest *and* the import graph.** A package can be vendored or
transitively present without being declared, and importing it is just as much a
violation of the decision. Sneaking around `package.json` does not work:

```
✗ DEC-002  Datastore is MongoDB
    src/configs/pgpool.js:1
      imports "pg"
```

### `approved_dependencies`

Requires every declared direct dependency to be covered by an allow entry.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `allow` | no | none | list of names or globs |
| `message` | no | `"dependency is not covered by any decision"` | shown on violation |

```yaml
- approved_dependencies:
    allow: [react, react-dom, "@types/*", zod]
```

The most common thing a coding agent does without asking is add a package.
`jasper init` seeds the allow list from what the repo already declares, so
enabling it never fails on day one — it only fires on what arrives next.

Note `allow` is not required: an empty allow list forbids **every** dependency,
which is occasionally what you want and is otherwise a sharp edge.

### `max_dependencies`

A dependency budget.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `count` | yes | — | integer ≥ 1; the maximum permitted |
| `message` | no | `"this project budgets N direct dependencies"` | shown on violation |

```yaml
- max_dependencies:
    count: 25
```

A different instrument from an allow list. `approved_dependencies` asks "was
this one chosen deliberately?"; a budget asks "is the total still something we
can audit, upgrade and ship?". Teams that answer only the first question end up
with four hundred deliberate dependencies. Emits one finding for the project,
not one per package.

---

## Source-text checks

### `forbid_text`

Forbids a regular expression from appearing in a set of files.

| Field | Required | Default | Meaning |
|-------|----------|---------|---------|
| `pattern` | yes | — | Go regexp (RE2); compiled at load time |
| `in` | no | `"**"` | glob: files to scan |
| `except` | no | none | globs: files exempted |
| `message` | no | `"<pattern> must not appear in <in>"` | shown on violation |

```yaml
- forbid_text:
    pattern: "os\\.environ|getenv"
    in: "app/**"
    except: ["app/core/**"]
    message: "read configuration from app.core.config, not the environment"
```

Not every decision is about the import graph. "Configuration is read once, in
config/" and "no debug logging in production paths" are architectural choices
with a real cost when they erode, and both are invisible to every other check
here.

Reports one finding per matching **line**, with the line number and an excerpt:

```
✗ DEC-002  Configuration is read once, in app.core.config
    app/api/admin.py:8
      matches /os\.environ|getenv/: print(os.environ["ADMIN_TOKEN"])
```

Two things to know. It is the only check that reads file contents, so it is the
only one that costs I/O — see
[architecture.md](architecture.md#the-purity-boundary-precisely) for why that is
injected rather than leaked. And a regexp is not a parser: a pattern will match
inside a comment or a string literal. Scope it with `in` and keep the pattern
specific.

---

## Grandfathering

Any decision can carry a `scope` block:

```yaml
scope:
  exclude:
    - "src/legacy/**"      # known exceptions, each ideally with an owner
```

Findings whose file matches `scope.exclude` are dropped. Without this, enabling
a rule on an existing repo produces hundreds of failures at once and the user
turns Jasper off permanently.

`scope.since` (a git rev, to govern only files touched after it) is parsed but
**not yet applied**.

## Choosing between checks

| You want to say | Reach for |
|-----------------|-----------|
| "this module has a front door" | `public_api` |
| "these two specific things must not touch" | `no_import` |
| "the whole repo flows one direction" | `layers` |
| "these two modules are independent" | `layers` with peer groups, or `no_import` both ways |
| "this library belongs to one layer" | `confine` |
| "we chose X, not Y" | `forbid_dependency` |
| "nothing new without a decision" | `approved_dependencies` |
| "keep the total small" | `max_dependencies` |
| "everything here must do X" | `require_import` |
| "this pattern does not belong here" | `forbid_text` |
| "these modules must stay untangled" | `no_cycles` |

Adding a new primitive: **[contributing.md](contributing.md)**.
