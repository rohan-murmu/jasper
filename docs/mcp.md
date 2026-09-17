# MCP integration

Jasper ships an MCP server so a coding agent can ask a question *while
deciding*, rather than finding out at commit time. It is the difference between
"you may not do that" and "you should not have done that."

## Setup

```sh
claude mcp add jasper -- jasper mcp
```

Use `--scope user` to register once for every project. The server resolves the
repository root from its own working directory, and MCP clients launch servers
with the cwd set to the project, so one registration follows you around.

Or commit a `.mcp.json` so the whole team gets it:

```json
{
  "mcpServers": {
    "jasper": { "command": "jasper", "args": ["mcp"] }
  }
}
```

This repo's own `.mcp.json` uses `go run ./cmd/jasper mcp` so it works from a
clone without installing the binary.

Verify with `/mcp` in Claude Code — you should see seven tools. The server also
runs in a repo with no `.jasper` directory; the tools simply report that
nothing is enforced yet.

## Transport

JSON-RPC 2.0, one JSON object per line, on stdin/stdout. Methods handled:
`initialize`, `notifications/initialized`, `notifications/cancelled`, `ping`,
`tools/list`, `tools/call`. Protocol revisions `2024-11-05`, `2025-03-26` and
`2025-06-18` are recognised; an unknown revision gets the newest known one
rather than an error, so a newer client can negotiate down.

**stdout carries protocol frames only.** Anything diagnostic goes to stderr, or
it corrupts the stream.

Line framing is not incidental. A `json.Decoder` streaming straight from stdin
cannot resynchronise after a syntax error — it returns the same error forever —
so a single malformed frame would spin a CPU core. Reading a line at a time
means a bad frame costs exactly one frame, and the parse error is answered with
a null-id response as the spec requires.

You can drive the server by hand, which is by far the fastest way to test a
decision:

```sh
cd your-project
cat > /tmp/t.jsonl <<'EOF'
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"can_add_dependency","arguments":{"package":"pg"}}}
EOF
jasper mcp < /tmp/t.jsonl
```

## Tools

### `architecture_brief`

No arguments. Returns the binding decisions' `brief` fields, advisory notes
separately, and the module list. This is the same content `jasper brief`
produces, and it is what an agent should read before working in an unfamiliar
area.

### `can_import`

| Argument | Required | Meaning |
|----------|----------|---------|
| `from` | yes | repo-relative path of the importing file — **need not exist** |
| `spec` | yes | the specifier exactly as it would be written |

```
can_import(from="src/billing/tax.ts", spec="pg")

DENIED — import of "pg" by src/billing/tax.ts would violate a recorded decision.
"pg" is an external package (pg)

DEC-002 Datastore is PostgreSQL
  the database driver stays behind the db layer
  fix: Access it through the layer at src/db/** instead of importing it directly.
  rationale: .jasper/decisions/002-datastore.yaml (explain_decision id=DEC-002)
```

Only `from`'s extension (to pick the language) and directory (to anchor
relative specifiers) are used, which is what lets an agent ask about a file it
is about to create. A file type Jasper cannot parse is a tool error, not a
silent ALLOWED.

### `can_add_dependency`

| Argument | Required | Meaning |
|----------|----------|---------|
| `package` | yes | package name |
| `version` | no | constraint; used only in the explanation |

### `check_architecture`

No arguments. Runs every decision against the tree as it is on disk. Equivalent
to what CI will run, so a clean result means CI passes. An agent should call
this after a batch of edits, before claiming it is done.

### `explain_decision`

| Argument | Required | Meaning |
|----------|----------|---------|
| `id` | yes | e.g. `DEC-002`; matching is case-insensitive and substring |

Returns `why`, `brief`, the compiled rules, and the source path. The rationale
usually names the failure the decision exists to prevent, which is what makes
it possible for an agent to argue with it intelligently rather than route
around it.

### `list_decisions`

No arguments. Id, enforcement status, and title for each decision.

### `propose_decision`

| Argument | Required | Meaning |
|----------|----------|---------|
| `title` | yes | one line |
| `why` | yes | rationale for a human in six months |
| `brief` | yes | ~60 tokens of constraint, imperative |
| `enforce` | no | list of single-key objects naming check kinds |

Writes `status: proposed`, which **enforces nothing**. `engine.Compile` skips
any decision that is not accepted, so `check_architecture` keeps denying
exactly what it denied before. A human must change the status for it to bind.

This is the write boundary: an agent can argue for a rule change in git, but it
cannot approve itself out of a constraint. The enforce block is normalised and
compiled before the file is written — an unvalidated write would make
`store.Load` fail and break `jasper check` repo-wide.

## Verdict semantics

Both pre-flight tools report only what the change would **introduce**, computed
by running every check twice: once with the change absent, once with it
present. See [workflows.md](workflows.md#3-mcp-pre-flight-answering-before-the-code-exists).

Three outcomes to expect:

```
ALLOWED — ... introduces no violation.
(2 checks evaluated; 3 unrelated violation(s) already exist in this repo)
```

The existing violations are disclosed but not blamed on the caller. A repo with
pre-existing problems must not answer DENIED to everything, or the agent learns
the tool is noise.

```
ALLOWED — ... is already in the tree and violates nothing.
```

```
DENIED — dependency mongoose is ALREADY in the tree and is violating a decision
right now. This is a live failure, not a hypothetical one: CI is red on it today.
```

That last case matters: asking about something the code already does must not
read as approval.

A DENIED verdict is a **successful** tool call — `isError` is false. `isError`
is reserved for the tool failing to run (an unparseable file type, a malformed
decision file).

## Getting an agent to actually call these

Tool descriptions carry the instruction, and the `initialize` response includes
server instructions telling the agent when to reach for each one. That is
usually enough with a cooperative model. To make it reliable, say it in your
project instructions too:

```markdown
## Architecture

This project records architectural decisions in `.jasper/`. The `jasper` MCP
server is available.

- Before adding any dependency, call `can_add_dependency`.
- Before adding an import that crosses a module boundary, call `can_import`.
- After a batch of edits, call `check_architecture` before reporting done.
- If a call is DENIED, change your approach. Read `explain_decision` first;
  only use `propose_decision` if the decision itself looks stale, and say so
  to the user rather than treating the proposal as approval.
```

### Testing that it works

Write a prompt that requires violating a decision, and see what the agent does.
On a MongoDB project with a "Datastore is MongoDB" decision:

```
Add an audit log for project updates. Store it in Postgres using the pg package.
```

**Pass:** the agent calls `can_add_dependency("pg")`, gets denied by name, and
comes back with the rationale and an alternative.

**Fail:** it writes `require("pg")` and reports success. Then run `jasper check`
yourself — exit 1 proves Jasper knew and the agent never asked.

**The control run is the real measurement.** Same prompt with
`claude mcp remove jasper`. The gap between the two is what Jasper is worth on
your codebase; everything else is theory.

## Limits

MCP is **advisory**. An agent can decline to call a tool, ignore a denial, or
not support MCP at all. Nothing here intercepts a file write — there is no
daemon, no watcher, no filesystem hook.

Keep a gate that does not depend on cooperation:

```sh
jasper check --format github          # CI
echo 'jasper check' >> .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
```

Also: every pre-flight call is a full repository scan (5ms on this repo, and
there is no cache yet — see [scaling.md](scaling.md)). And the server handles
one frame at a time, which is correct for local stdio and would need revisiting
for a remote transport.
