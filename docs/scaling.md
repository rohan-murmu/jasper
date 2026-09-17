# Scaling and adoption

Two different questions: will it run fast enough, and will a team actually keep
it on. The second one is harder.

## Measured performance

All figures from a synthetic TypeScript repo, single run, cold (there is no
cache — see below).

| Repo | Files | Import edges | Checks | Wall | Peak RSS |
|------|-------|--------------|--------|------|----------|
| Jasper itself (Go) | 48 | 130 | 9 | 5 ms | 6 MB |
| A small Express app (JS) | 27 | 70 | 3 | 10 ms | 6 MB |
| Synthetic, graph checks only | 5,000 | 15,000 | 2 | 138 ms | ~19 MB |
| Synthetic, **plus** `forbid_text` over every file | 5,000 | 15,000 | 3 | 177 ms | ~20 MB |

Roughly **25–30k files per second** end to end, including the walk, parsing
every file, resolving every specifier, compiling the decisions and running the
checks. Reading the full text of 5,000 files for `forbid_text` costs about
40 ms on top.

Memory stays flat because the `Snapshot` deliberately does **not** hold source
text; `forbid_text` pulls bytes through an injected loader and the cache is the
only thing that grows. On a repo where that matters, scope the check with `in`.

### Where the time goes

1. **The walk.** `filepath.WalkDir` plus a `Stat` per file. This dominates on
   repos with many non-source files. `.git`, `node_modules`, `vendor`, `dist`,
   `build`, `out`, `target`, `.next`, `.nuxt`, `.venv`, `venv`,
   `__pycache__`, `coverage`, `.turbo` and `.cache` are skipped, as is any
   dot-directory.
2. **Parsing**, fanned out across `runtime.NumCPU()`. Files over 2 MB are
   skipped entirely.
3. **Resolution**, one resolver per language per repo — they read `go.mod` and
   `tsconfig.json`, so building them per file would be wasteful.
4. **Checks**, fanned out across `runtime.NumCPU()`.

### The cache that does not exist yet

`Snapshot.Hash`, `store.Hash` and `Report.Cached` are all computed. Nothing
reads them. Every run is a full scan.

This has not been worth fixing at 5 ms, and the design already accounts for it:
findings are a pure function of the snapshot hash and the decision-set hash, so
identical inputs need no work. `gitRev` returns `""` when the tree is dirty
precisely so a cache can never be keyed on an unstable revision. If you are
running Jasper on something large enough to care, that is the change to make,
and `.jasper/.cache/` is already in `.gitignore`.

The place it bites soonest is MCP: **every pre-flight call is a full scan**. At
5–200 ms that is fine for interactive use; on a very large monorepo an agent
making twenty `can_import` calls in a turn would notice.

## Monorepos

A directory with its own `.jasper` is treated as a **separate project** and
skipped by the parent's walk. That is how `testdata/acme-api` lives inside this
repo without its dependencies becoming Jasper's.

So there are two viable layouts:

**Per-package decisions.** Give each package its own `.jasper/`. Each is
checked independently, `jasper -C packages/api check` works, and CI can run
them in parallel. Boundaries *between* packages need a root `.jasper` too,
which will then skip the packages that govern themselves — so put cross-package
rules at the root and keep per-package rules local. Be deliberate about which
level owns which rule.

**One root decision set.** Simpler, and usually right up to a few thousand
files. Use globs that name packages:

```yaml
- layers:
    order:
      - "packages/shared/**"
      - ["packages/api/**", "packages/worker/**"]   # peers
      - "packages/web/**"
```

Known gaps for monorepos specifically:

- `tsconfig.json` `extends` is not followed, so path aliases defined in a base
  config are invisible and those imports look external. This is the most likely
  cause of a confusing result.
- Only the root `tsconfig.json` is read.
- A Cargo workspace root does not pull in member manifests.
- `store.Config.Include/Exclude/Packs` exist in `jasper.yaml` and are **not**
  wired to the scanner; the ignore list is hardcoded.

## Adopting on an existing codebase

The failure mode is not performance. It is turning Jasper on, getting four
hundred findings, and switching it off for good. Three mechanisms exist to
prevent that, in order of preference.

### 1. Let `init` choose the rules

`jasper init` proposes **only** rules that already pass on HEAD. A fact that
does not hold is shown and deliberately not offered:

```
✓ Datastore: PostgreSQL  no other database driver is declared
✗ Two Python HTTP clients: httpx and requests  one of these is probably drift
```

The first run is green by construction, and the rules start catching the *next*
mistake rather than relitigating every old one.

### 2. Grandfather what you cannot fix yet

```yaml
scope:
  exclude:
    - "src/legacy/**"        # owner: @platform, tracked in PLAT-1182
```

Put an owner and a ticket next to every entry. An exclusion without a name on
it is permanent.

(`scope.since`, which would govern only files touched after a given git rev, is
parsed but not yet applied.)

### 3. Start advisory

A decision with no `enforce` block is a note. It appears in `jasper brief` and
therefore in the agent's context, but it cannot fail a build:

```yaml
id: DEC-007
title: New services are event-driven
status: accepted
why: |
  ...
brief: |
  New services consume events rather than calling each other synchronously.
# no enforce: block — this steers the agent without breaking CI
```

This is the cheapest way to get a direction in front of an agent before you
have worked out how to express it as a check, or whether you can.

## Rollout order that works

1. `jasper init --yes` and commit whatever it proposes. Green from the start.
2. Add `jasper check` to CI. Nothing fails, so nobody objects.
3. Add the MCP server. Agents start asking before they write.
4. `jasper brief >> CLAUDE.md` for agents without MCP.
5. Now add the rules you actually care about, one at a time, each with a `why`
   a reviewer would accept. Use `scope.exclude` for existing violations and
   file a ticket per entry.
6. Add the pre-commit hook last. It is the strictest gate and the most annoying
   one to discover by surprise.

## Watching for decay

The decisions themselves rot, and Jasper does not warn you:

- **`brief` output is a snapshot.** `jasper brief >> CLAUDE.md` is correct the
  moment you run it and stale the moment you add a decision. Regenerate it in
  CI, or tell the agent to call `architecture_brief` rather than trusting a
  baked copy.
- **A large `scope.exclude` is a rule that has stopped meaning anything.** If
  more files are excluded than governed, delete the rule or fix the code.
- **Advisory notes accumulate.** A note nobody has turned into a check in six
  months is a note nobody believes.
- **`jasper ls`** shows which decisions are `enforced`, `advisory` or
  `inactive`. Read it occasionally.

## What Jasper will not scale to

Being explicit about the boundary:

- It analyses declared dependencies and static import edges. A raw SQL string,
  a shell-out, a dynamically built import path, and anything behind reflection
  or codegen are invisible. `forbid_text` is the escape hatch, and it is a
  regexp, not a parser.
- There is no multi-repo batch mode. The engine is pure and would support one;
  no port exists.
- There is no server, no daemon and no incremental mode. Every invocation is a
  cold process, which is why the startup budget matters more than throughput.
