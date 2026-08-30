# pika M3 Design Specification — Trust and the Upgrade Path

**Status:** Approved
**Date:** 2026-08-30
**Product:** `pika`
**Builds on:** [2026-08-30-pika-m2-durable-work-design.md](2026-08-30-pika-m2-durable-work-design.md)

## 1. Purpose

M1.5 made the kernel legible. M2 gave its automation memory and made the kernel the thing that attests.

Neither made pika safe to live with. A repository that adopts pika today inherits CI that silently rots, and the upgrade command our own documentation recommends destroys the operator's hand-written files. The private-state filter that M2 hardened is defeated by a non-ASCII filename. And the record pika writes about a run — which the filter exists to keep out of history — turns out to contain the operator's goal and every gate's output, unredacted.

M3 closes that gap. It adds no new commands and no new machinery. It makes what already exists trustworthy.

## 2. Goals

1. Make `pika init --force` safe to run on a live repository.
2. Give an adopted repository a signal when the templates that scaffolded it have been corrected since.
3. Make the private-state filter proof against paths git chooses to quote.
4. Stop writing unredacted operator and agent text into local state that one filter bug separates from history.
5. Enforce the one capability kind whose operations actually occur, and tell the truth about the four whose do not.

## 3. Non-goals

Deferred, and scope creep into any fails review:

- parallel writers, write-scope leases with conflict detection, a task graph, multi-agent collaboration;
- enabling `apply_plan`;
- content-based copy detection (see §7);
- SQLite (still unjustified — no second concurrent writer exists);
- any new command.

## 4. Current-state findings

Verified read-only against `main` at `3a53feb`.

| Finding | Evidence |
|---|---|
| `pika init --force` rewrites README.md, AGENTS.md, CONTRIBUTING.md, .gitignore, the PR template, the CI workflow and the language scaffold | `internal/initcmd/init.go` managed-file set |
| It resets `.project/exceptions.yaml` to `{}` — destroying every recorded exception, each of which carries a rationale, owner and review condition | `internal/initcmd/init.go` |
| It rebuilds the contract from the profiles named **on the command line**, never reading the existing contract, so a bare `--force` in a Go repo yields a core-only contract with `commands: {}` | `internal/initcmd/init.go` |
| `--name` is likewise not read back; a mismatch writes `module <dirname>` and a stray `cmd/<dirname>/main.go` | `internal/initcmd/init.go` |
| **This command is the documented upgrade remedy** for a rotated profile digest | `docs/guides/usage.md` upgrading section |
| `PackDigest()` and `PackDigestFor()` hash **pack YAML bytes only**. `core@1.yaml` declares `templates: []`, so not even template filenames are hashed; the templates' separate `go:embed` is entirely uncovered | `internal/profiles/registry.go` |
| `pika apply` renders a core file only when it is **missing** (`Lstat` skip in `buildPlan`/`promote`), so it never refreshes a corrected one | `internal/apply/apply.go` |
| Consequence: every repo adopted before the M2 CI-template correction keeps an `@latest` workflow, and nothing in `CheckLock` or `apply` will ever tell it | composition of the two rows above |
| Git C-quotes any path with non-ASCII, whitespace or control bytes (`core.quotePath` default on), so the parser receives `".project/state/w\303\251ird.json"` **with a leading `"`** | `internal/improve/improve.go:826-843`, `:802-804` |
| `isPrivateState` is `HasPrefix(path, ".project/state/")`, which is false for that literal — so `privateStateMoved` does not refuse and `changePaths` does not drop. **Both guards fail open** | `internal/improve/improve.go:756, 788, 791, 796` |
| Two other call sites parse the same porcelain and share the hole | `internal/changed/changed.go:60-62,111-118`; `internal/improve/receipt.go:208-217` |
| **`record.json` is written unredacted** and carries the operator's goal plus full baseline and recheck reports including every gate's `OutputTail` | `internal/workrec/record.go:66-73` — no `redact` import |
| `board.jsonl` is written unredacted from agent-supplied strings | `internal/mcp/server.go:795, 812, 856` |
| The raw agent transcript is unredacted on disk from the moment Codex writes it until `createHandoff` returns, and survives forever if the process is killed | `internal/improve/handoff.go:133` |
| Only `fs_write` and `exec` have enforcement call sites — nine in total, all behind one choke point | `internal/mcp/server.go:833` |
| **No pika source imports any `net/*` package directly**, and `net`, `net/http`, `net/rpc` and `crypto/tls` are absent from the build closure entirely. `net/url` and `net/netip` ARE in the closure — reached via `jsonschema/v6` format validation and `text/template`'s urlquery escaper — but both are pure parsers with no dialing capability. Corrected from an earlier, overstated draft of this row during implementation | repo-wide; verified at implementation time |
| It reads no credential, passes none to a child, and performs no GitHub operation — `contract.github.merge` is scaffolded and never acted upon | repo-wide |
| `fs_read` is the one unenforced kind whose operation class **does** occur: `inspect_repo` walks the tree and `read_contract` loads a caller-supplied path | `internal/mcp/server.go` |

## 5. Design

### 5.1 A non-destructive `--force`

`--force` stops meaning "rebuild from my flags" and starts meaning "regenerate what the kernel owns, from what this repository already declares".

When a contract exists and the corresponding flag is absent, `init` reads back: `profiles` (via `contract.Load` and `checks.ProfileRefs`) and `project.name`. The contract has no `module` field, so a module can only be recovered from `go.mod` via `discover`; when neither a flag nor a `go.mod` supplies it, `--force` refuses rather than inventing one from the directory name.

`.project/exceptions.yaml` is never reset. Each record carries a rationale, an owner and a review condition that a human wrote; discarding them is destroying evidence, not regenerating a managed file.

Files the operator can edit — `README.md`, `AGENTS.md`, `CONTRIBUTING.md` — are no longer rewritten by `--force`. `--force` regenerates only what the kernel unambiguously owns: `.project/profiles.lock`, the PR template, and the CI workflow. A new `--reset-docs` opt-in restores the old behavior for an operator who genuinely wants the templates back, so nothing becomes impossible — only non-default.

An explicit flag always wins over the read-back value, so scripted use is unchanged.

### 5.2 Templates inside the digest

`PackDigest` and `PackDigestFor` hash the pack's templates alongside its YAML, in a stable order.

This makes a corrected template visible: `CheckLock` already compares per-pack and top-level digests, so an adopted repository whose scaffolded CI came from a since-corrected template now fails gate 1 with a specific message rather than silently keeping a workflow that verifies a published release instead of its own commit.

`pika apply` gains the ability to refresh a **kernel-owned** file whose content differs from the current template — the PR template and the CI workflow only. It still never touches a file the operator owns. A refresh is reported in the apply report, never silent.

This rotates every lock a third time. That cost is the reason it is paired with §5.1: an operator told to run `--force` must first be able to run it safely.

### 5.3 A parser that survives quoting

All three porcelain parsers move to `-z` (NUL-delimited) output, where git emits paths verbatim and never quotes.

`-z` changes rename encoding and the change is not cosmetic: v1 `-z` **omits** the `->` and **reverses** field order, emitting `XY <to>\0<from>\0`. The origin therefore arrives as a separate trailing field *after* the destination, inverting the origin-first assumption currently baked into `statusEntries`' struct, `changePaths`' `{origin, path}` ordering, and the literal-porcelain unit tests. Those must be updated together, and the tests must feed real NUL-delimited fixtures rather than arrow-joined strings.

All three call sites move: `internal/improve/improve.go`, `internal/changed/changed.go`, `internal/improve/receipt.go`. Leaving one behind would preserve the hole in a different command.

### 5.4 Redaction at the boundary that writes

`workrec` redacts before writing `record.json`. The goal and every gate's `OutputTail` pass through `redact.Apply`, the same treatment `evidence.Build` already gives every string it emits.

The board's ad-hoc appends redact likewise.

This is defence in depth, deliberately: §5.3 closes the filter hole that exists today, and §5.4 reduces what a future filter bug could leak. A guarantee that rests on exactly one correct prefix test is one bug from a disclosure, and this milestone exists because that test was already wrong once.

The raw agent transcript keeps its existing `defer os.Remove`, but the window is documented precisely — including that it survives a kill — so an operator knows what a crashed run leaves behind.

### 5.5 Enforce what occurs; document what cannot

`fs_read` gains its call site, on the MCP paths that read on an agent's behalf. It is cheap: `allowsRead` already implements a repo-inside default and `contract.NormalizeRepoPath` already bounds the targets.

`network`, `credential` and `github` get **no** enforcement, because the binary contains no operation of any of those classes. An authorization check for an operation that never happens is dead code wearing a safety costume, and it would make the envelope look more protective than it is. `budget` likewise: nothing accounts spend, and `pika authorize` already refuses to emit a budget key.

Instead the delta document states plainly, per kind, that the operation class does not occur in the kernel — and names where the real boundary lives for network: the Codex child process, which pika sandboxes through argv (`--sandbox workspace-write`, `network_access=false`), and generated CI's module fetches. That is the honest description, and it is more useful to an operator than four unreachable checks.

## 6. Error handling

- `--force` without a recoverable module and without a flag refuses; it does not guess from the directory name.
- A read-back that finds an unparseable contract refuses rather than silently falling back to flags — a corrupt contract is a fact to report.
- A stale-template lock failure names the pack and the remedy.
- `apply`'s refresh reports every file it rewrites; silence would make a kernel-owned rewrite indistinguishable from an operator's own edit.
- The `-z` parsers must fail closed on a malformed record: an unparseable status entry refuses the run rather than being skipped, because a skipped entry is exactly how the current hole leaks.

## 7. Explicitly not fixed: the copy leak

A `cp` out of `.project/state` produces only `?? public/notes.md` — an untracked add of a public path with no relationship to private state in any git output. No path-based filter can see it.

Content-based detection would mean hashing or scanning every to-be-committed blob against private-state contents. Its false-positive surface is large and structural: gate output legitimately quotes the same compiler and test text the reports contain, so a file that reproduces a failing test's output is indistinguishable from a leak of the report that captured it.

M3 does not attempt it. §5.4 is the mitigation that actually helps: if the private content is redacted at the point of writing, a copy carries less. This is recorded as a known limit, not silently assumed closed.

## 8. Testing

- `--force`: reads back profiles and name; an explicit flag wins; exceptions survive; operator-owned files survive; `--reset-docs` restores them; a missing module refuses.
- Digest: editing a template rotates the pack digest; `CheckLock` fails a repo whose lock predates a template correction with a message naming the pack; `apply` refreshes a kernel-owned file and reports it, and still never touches an operator file.
- Parser: a non-ASCII private path is refused, proving the C-quoting hole closed — the test must use a genuinely quoted path, not a plain one. A `-z` rename fixture in real `XY <to>\0<from>\0` order. All three call sites covered.
- Redaction: a record containing credential-shaped text is redacted on disk; the goal is redacted; gate output is redacted.
- `fs_read`: a read outside the granted scope is denied on the MCP path; the human CLI still needs no envelope.
- E2E: `--force` on a repo with a hand-written README and a recorded exception preserves both.

## 9. Completion definition

M3 is complete when:

1. `pika init --force` preserves operator-owned files and recorded exceptions, and reads profiles and name back from the contract;
2. `--reset-docs` restores the previous behavior explicitly;
3. templates are inside `PackDigest`, and a repo scaffolded from a since-corrected template fails gate 1 with a message naming the pack;
4. `pika apply` refreshes kernel-owned files and reports each one, and never touches an operator's;
5. all three porcelain parsers use `-z`, handle its reversed rename order, and refuse a malformed entry rather than skipping it;
6. a non-ASCII path under `.project/state` is refused, proving the quoting hole closed;
7. `record.json` and the board are redacted at write time;
8. `fs_read` is enforced on the MCP read paths;
9. the delta document states, per unenforced kind, that the operation class does not occur in the kernel, and names the real network boundary;
10. `go test ./... -count=1` passes, `CGO_ENABLED=0 go build ./...` succeeds, `go.mod` still declares exactly two direct dependencies, and `pika check --ci` passes on this repository in GitHub Actions.
