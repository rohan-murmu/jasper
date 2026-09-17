# Workflows

Five flows: `init`, `check`, MCP pre-flight, `propose`, and the loop they form.

## The loop

```
                    ┌──────────────────────────────────────────┐
                    │                                          │
                    ▼                                          │
       ┌────────────────────────┐                              │
       │  jasper init           │  observe code → propose      │
       │  (or hand-write YAML)  │  rules that already pass     │
       └───────────┬────────────┘                              │
                   │                                           │
                   ▼                                           │
       .jasper/decisions/*.yaml  ── committed to git ──┐        │
                   │                                   │       │
         ┌─────────┴──────────┐                        │       │
         ▼                    ▼                        ▼       │
   jasper brief         MCP pre-flight           jasper check   │
   (static text         (agent asks              (the gate)     │
    into CLAUDE.md)      before writing)              │         │
         │                    │                       │         │
         └────────┬───────────┘                       │         │
                  ▼                                   │         │
            agent writes code ────────────────────────┘         │
                  │                                             │
                  └── new architectural choice ─────────────────┘
                      (propose_decision, or a human edit)
```

The cycle is the product: code → `init` → YAML → `brief`/MCP → agent → code →
`check`. Each arrow is a separate command, and none of them runs in the
background. Jasper is a gate you position, not a guard that patrols.

## 1. `jasper check` — the load-bearing path

```
main.go: os.Exit(cli.Main(os.Args[1:]))
│
└─ cli.Main                          strip -C, dispatch
   ├─ service.FindRoot(dir)          walk up for .jasper, else .git
   ├─ service.New(root)              blank imports have already run every init()
   └─ cmdCheck
      ├─ svc.Check()
      │  │
      │  ├─ scan.New(root).Scan() ───────────────── IMPURE, once
      │  │  ├─ walk()                   skip ignored dirs + nested .jasper
      │  │  ├─ scan.For(path) → Language     registry lookup by extension
      │  │  ├─ lang.Imports(src)             parallel, NumCPU-bounded
      │  │  ├─ Resolver.Resolve / PackageName    internal edge vs external pkg
      │  │  ├─ readManifests()               declared direct deps
      │  │  ├─ inferModules()                heuristic, display only
      │  │  └─ hashSnapshot()            → *model.Snapshot
      │  │
      │  ├─ store.Load() ──────────────────────── IMPURE, once
      │  │     .jasper/decisions/*.yaml → []*Decision
      │  │     a malformed file is a hard error, never a warning
      │  │
      │  ├─ engine.Compile(decisions) ─────────── PURE from here down
      │  │     skip !Active(); for each enforce rule:
      │  │       registry[kind].Compile(cfg, d) → Runner
      │  │       globs and regexps compiled once
      │  │
      │  └─ eng.Run(snap)
      │        parallel runner.Run(snap) → []Finding
      │        → scopedOut filter → stamp Decision/Rule/Severity
      │        → sort by (decision, file, line)
      │
      ├─ decisionIndex(svc)           id → title, id → source path
      ├─ renderText | renderGitHub | json
      └─ exit 0 if rep.OK(), else 1
```

A "check" in the summary line is one **enforce rule**, not one decision. This
repo's two decisions compile to nine runners, and `jasper check` reports `9
checks passed`.

## 2. `jasper init` — onboarding without a wall of failures

```
cmdInit
├─ svc.InitStore("")              create .jasper/jasper.yaml + decisions/
├─ svc.Plan()
│  ├─ Snapshot()                  same scanner as check
│  └─ facts.All(snap)             PURE, zero inference
│     ├─ dependencySet()          → approved_dependencies over what exists
│     ├─ datastore()              → forbid_dependency for every store NOT in use
│     ├─ duplicates()             → two HTTP clients, two test runners, ...
│     └─ privateDirs()            → no_import for boundaries already respected
│
├─ print each Fact with ✓ (holds) or ✗ (drift)
│
└─ for each Fact where Proposal != nil AND Holds:
      prompt [a]ccept / [s]kip        (--yes accepts all; Enter = accept)
      └─ svc.Accept(proposal, OriginObserved)
         └─ svc.Record(p, origin, StatusAccepted)
            ├─ NormalizeEnforce(p.Enforce)    reshape []string → []any etc.
            ├─ ValidateEnforce(...)           compile it before writing
            ├─ store.NextID(existing)         DEC-00N
            └─ store.WriteDecision(d, enforce)
```

The `AND Holds` guard is the whole property. A fact that does **not** hold —
two HTTP clients, a boundary already breached — is reported and deliberately
never proposed:

```
✓ 5 direct dependencies declared  fastapi, httpx, psycopg, requests, sqlalchemy
✓ Datastore: PostgreSQL  no other database driver is declared
✗ Two Python HTTP clients: httpx and requests  one of these is probably drift
```

Without it, the first run on a real repo emits hundreds of failures and the
user disables Jasper permanently.

`ValidateEnforce` before writing is not optional: `store.Load` treats a
malformed decision file as a hard error, so one bad write would break
`jasper check` repo-wide — including checks that have nothing to do with the
bad file.

## 3. MCP pre-flight: answering before the code exists

This is the flow that could not exist at commit time.

```
can_import(from="src/billing/tax.ts", spec="pg")
│
└─ service.CanImport
   ├─ scan.ResolveSpec(root, from, spec)
   │     pick Language by `from`'s extension; build its Resolver;
   │     resolve → model.Import{To: ...} or {Pkg: ...}
   │     `from` need NOT exist: only its extension and directory are used
   │
   └─ service.preflight(baseline, candidate)

          scan once ──► Snapshot ──► engine.Compile(decisions)
                            │
              ┌─────────────┴─────────────┐
              │                           │
         baseline(snap)              candidate(snap)
       remove the edge              add the edge
       if it is present
              │                           │
              ▼                           ▼
          eng.Run  ──►  before      eng.Run  ──►  after
              │                           │
              └────────► diffFindings ◄───┘
                              │
                              ▼
                  introduced findings only
                  Allowed = len(introduced) == 0
```

Two properties fall out of the two-snapshot diff:

**Only the delta is reported.** A repo with three existing violations must not
answer DENIED to every question, or the agent learns the tool is noise and
stops calling it. `Verdict.Existing` discloses the rest without blaming the
caller for it.

**Removing the change first is what makes the answer honest.** Without the
baseline step, asking about an import that *already exists* compares the tree
to itself, finds no new violation, and reads as approval for something failing
CI right now. With it, `Verdict.Present` is set and the answer is explicit:

```
DENIED — dependency mongoose is ALREADY in the tree and is violating a decision
right now. This is a live failure, not a hypothetical one: CI is red on it today.
```

`can_add_dependency` is the same flow with `Manifest.Direct` mutated instead of
`Imports`.

## 4. `propose_decision` — the write boundary

```
propose_decision(title, why, brief, enforce)
│
├─ service.NormalizeEnforce(enforce)     JSON float64 → int, []string → []any
├─ service.ValidateEnforce(enforce)      compile it; reject typos and bad kinds
└─ service.Record(p, OriginAuthored, StatusProposed)
      └─ store.WriteDecision → .jasper/decisions/00N-slug.yaml
                               with  status: proposed
```

Then, on the very next `check_architecture`:

```
engine.Compile(decisions)
  for _, d := range decisions {
      if !d.Active() { continue }        // Active() == accepted && !superseded
      ...
  }
```

`status: proposed` never compiles, so it never binds. An agent can file an
argument for a rule change in git; it cannot approve itself out of a
constraint. Two tests hold the line:
`TestProposedDecisionDoesNotBind` writes a deliberately absurd
`no_import: {from: "**", to: "**"}` and asserts the finding count does not
move, and `TestAcceptedDecisionDoesBind` asserts the same rule *accepted* does
fire — so the first test cannot pass vacuously.

## 5. CI and pre-commit

```yaml
# .github/workflows/jasper.yml
name: architecture
on: [pull_request]
jobs:
  jasper:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.24' }
      - run: go build -o jasper ./cmd/jasper
      - run: ./jasper check --format github
```

`--format github` emits `::error file=...,line=...,title=...::message` workflow
commands, so violations annotate the changed lines of the diff rather than
hiding in a log. Three lines of real work, which is only possible because of
the no-CGO decision that `.jasper/decisions/003-no-cgo.yaml` enforces.

```sh
# pre-commit — the gate that does not depend on agent cooperation
echo 'jasper check' >> .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
```

Note there is no `jasper install-hook`; appending by hand will clobber an
existing hook's shebang if one is there.

## When each gate catches drift

| Wiring | Caught at | Cost of the catch |
|--------|-----------|-------------------|
| MCP pre-flight | while the agent is deciding | one tool call |
| agent runs `check_architecture` | end of the agent's turn | a few edits to undo |
| pre-commit hook | `git commit` | a working tree to unwind |
| CI | pull request | possibly dozens of commits built on the mistake |

All four are worth having. Only the last two are enforcement; the first two are
cooperation, and an agent can decline.
