# Using pika

Step-by-step usage for every pika command.

Every command takes `--root <dir>`. Without it, pika discovers the repository root by walking up from your working directory looking for `.project/contract.yaml`, then `.project/contract.yaml.draft`, then `.git` — so you can run `pika check` from any subdirectory and get the repository, not the folder.

`pika init` is the one deliberate exception: **it never discovers.** It scaffolds where you stand. Running `init` inside a subdirectory of an existing repository creates a new project there rather than silently re-scaffolding the enclosing repository.

Run `pika help` for the command list and `pika help <command>` for one command's flags; both are generated from the dispatch table, so they cannot drift from the commands that actually exist.

---

## 1. Start a brand-new project

```sh
mkdir my-service && cd my-service
pika init --profile go --name my-service
```

| Flag | Purpose |
|---|---|
| `--profile` | Language stack: `go`, `typescript`, `python`, `swift`, `rust` (repeatable for multi-stack) |
| `--name` | Project name (default: directory name, kebab-cased) |
| `--module` | Go module path (default: derived from name) |
| `--force` | Regenerate managed files in an already-initialized repo (never touches your own files outside `.project/`) |
| `--json` | Emit the created-file manifest as JSON |

What you get: `.project/contract.yaml` (the project contract), `.project/profiles.lock`, `.project/exceptions.yaml`, a `docs/` spine, `AGENTS.md`, `README.md`, `CONTRIBUTING.md`, a GitHub Actions workflow, a `.gitignore` protecting `.project/state/`, and a language-owned source scaffold.

Then verify:

```sh
pika check --all
```

---

## 2. Adopt an existing project

```sh
cd /path/to/existing-repo
pika adopt
```

This is **read-only analysis**. It inventories the repository, detects the stack, compares your conventions against the contract, and writes:

| Output | Location |
|---|---|
| Adoption review (human-readable markdown) | `review/adoption-review.md` — **open this first** |
| Draft contract | `.project/contract.yaml.draft` |
| Draft profile lock | `.project/profiles.lock.draft` |

The review file lists detected profiles, conventions (match / conflict / exception), proposed changes as a checklist, and proposed naming exceptions with a suggested action per path.

**Before applying, review two things:**

1. `review/adoption-review.md` — decide per proposed exception whether to keep it or rename the file instead (edit `.project/contract.yaml.draft` to change the decision).
2. The draft contract itself, if you want to adjust verification commands or agent mappings.

---

## 3. Apply the adoption

```sh
pika apply
```

This promotes the drafts transactionally:

- contract and lock become live
- `exceptions.yaml` is written
- missing core files (AGENTS.md, CONTRIBUTING.md, PR template, CI workflow) are created from templates
- your own files are never overwritten (create-if-missing; user files always win)

If anything fails mid-apply, the transaction rolls back and the report says so honestly — including the failure case where a rollback itself could not complete (it points you at `.project/state/recovery/`).

After a successful apply, the review file is rewritten with status **APPLIED** and the gate-1 result.

Refusals (safe, no mutation):

| Message | Meaning |
|---|---|
| `already adopted` | A committed contract exists — use `pika check` |
| missing drafts | Nothing to apply — run `pika adopt` first |
| invalid draft | The draft fails validation — fix or re-run `pika adopt` |

---

## 4. Check a project

```sh
pika check --all          # every gate
pika check --changed      # narrow to what changed since the merge base
pika check --ci           # same engine CI runs; no LLM calls
pika check --json         # machine-readable report
```

| Flag | Purpose |
|---|---|
| `--all` | Run every gate (the default scope) |
| `--changed` | Resolve a change set from git and skip the package gates only when the tree is provably clean |
| `--ci` | CI mode: implies `--all`, no interactive prompts, no LLM calls |
| `--json` | Emit the report as JSON on stdout |
| `--contract <path>` | Use a contract other than `<root>/.project/contract.yaml` (a relative path resolves against the root) |
| `--root <dir>` | Repository root (default: discovered) |

`--all`, `--changed` and `--ci` are mutually exclusive.

The verification ladder: contract integrity → formatting/lint/compile → tests → smoke → (in agent runs) independent review.

Gate 1 also validates that your `profiles.lock` matches the contract and the embedded pack digests — a hand-edited lock or drifted contract fails here.

### What `--changed` actually narrows

The change set is a real git diff: everything differing from the merge base with the upstream default branch (`@{upstream}`, then `origin/HEAD`, `origin/main`, `origin/master`), plus staged, unstaged and untracked changes.

Gate 1 always runs — it validates the contract itself, and no change set can put that out of scope. Only the package gates (format, lint, typecheck, test, smoke) can be skipped, and only in one case: **the change set is known to be empty.**

Attribution is not a narrowing signal. The gates are repository-wide commands from `contract.commands`, and a changed file often belongs to no declared package while being able to break all of them — a root `go.mod`, a CI workflow, the contract itself. So "this file is outside every package root" never means "skip the gates".

**Degradation is loud, and it always widens.** When the change set cannot be trusted, pika prints a warning naming the reason and runs every gate anyway:

```
warning: --changed could not resolve a change set (shallow clone: no reliable merge base); running every gate
```

It degrades when:

| Situation | Why it cannot be trusted |
|---|---|
| `git` is not on `PATH`, or the directory is not a work tree | There is no diff to compute |
| The clone is shallow | Any merge base it reports is an artifact of the depth cut, not the real fork point |
| A ref resolves but shares no history with `HEAD` | Grafted or unrelated history: there is no common ancestor to diff against |
| A `git` invocation fails | Unknown is not the same as unchanged |

A repository with one branch and no remote is *not* a degradation: it legitimately has nothing to fork from, so the staged and working-tree diffs stand on their own.

---

## 5. Diagnose a repository without running anything

```sh
pika doctor
```

`doctor` answers "why is this repository not working?" without executing a single gate. It is safe to run anywhere, at any time, and it is the first thing to reach for when `check` behaves unexpectedly.

| Flag | Purpose |
|---|---|
| `--json` | Emit the report as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

What it inspects:

| Check | What it tells you |
|---|---|
| root | The resolved repository root, and how it was found (`contract`, `draft`, `git`) |
| contract | Schema version and selected profiles, or the parse error |
| lock | Whether `profiles.lock` pins the contract's profiles at digests matching this binary's embedded packs |
| exceptions | Whether `.project/exceptions.yaml` loads and every record is complete |
| envelope | The grants in `.project/state/envelope.yaml`, or a warning that agents will be denied |
| `gate.*` | Per gate: the command that will run, or the pack's suggested hint when no command is configured |
| git | Whether git is available |

Worked example, on this repository:

```
$ pika doctor
root  /home/you/pika (contract)

ok    contract       schema 1, profiles [core@1 go@1]
ok    lock           pinned digests match the embedded registry
ok    exceptions     exceptions record loads
warn  envelope       no capability envelope at /home/you/pika/.project/state/envelope.yaml
                     → run "pika authorize --scope project"; without it every mutating MCP tool is denied
ok    gate.format    gofmt -l -w .
ok    gate.lint      go vet ./...
ok    gate.typecheck go build -o /dev/null ./...
ok    gate.test      go test ./... -count=1
ok    gate.smoke     go run ./cmd/pika version
ok    git            git is available
```

Exit code is `0` unless something is error-severity. A missing envelope is a **warning**, not an error: the human CLI does not need one.

---

## 6. Understand a rule, a gate, or an error code

```sh
pika explain naming-kebab-case
pika explain file-size-review
pika explain test
pika explain envelope_denied
```

| Flag | Purpose |
|---|---|
| `--json` | Emit the entry as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

`explain` covers three id families, resolved from the contract's own profiles so it explains *your* rules, not a generic list:

| Family | Ids |
|---|---|
| Naming rules | `naming-kebab-case`, `naming-catch-all`, `file-size-review`, `generated-owner` |
| Verification gates | `contract`, `format`, `lint`, `typecheck`, `test`, `smoke` |
| MCP error codes | `envelope_denied`, `contract_invalid`, `already_adopted`, `invalid_params`, `unavailable`, `internal` |

Run `pika explain` with an unknown id to print the ids this repository actually knows.

For a naming rule it prints the owning pack, the severity, what the rule matches, the rationale, the remediation, and a copy-pasteable exception record that parses as-is:

```
$ pika explain naming-kebab-case
naming-kebab-case (naming-rule)

owner:
  core@1

severity:
  warning

matches:
  scope path-segments; pattern ^[a-z0-9][a-z0-9._-]*$; exempt README, AGENTS, ...

rationale:
  Mixed-case path segments are the classic cross-platform trap: ...

remediation:
  Rename the offending segment to lowercase with dash, dot, or underscore separators ...

record an exception:

# .project/exceptions.yaml
<repo-relative path>:
  rule-id: naming-kebab-case
  reason: <why this path must keep its name>
  owner: <who accepts this>
  review-condition: <the condition that reopens this decision>
```

All four fields are mandatory. A record missing any of them fails gate 1 rather than silently widening the rule. An exception on a directory covers everything beneath it.

---

## 7. Authorize an agent (capability envelope)

```sh
pika authorize --scope project
```

Before this command existed, the only way to let an agent write anything was to hand-author `.project/state/envelope.yaml` correctly — the single largest barrier to actually using pika with an agent. `authorize` generates it.

| Flag | Purpose |
|---|---|
| `--scope` | `read`, `project` (default), or `repo` — see the table below |
| `--exec` | Command to authorize, as a **whole argv line**: `--exec "make test"`, not `--exec make` (repeatable; never granted implicitly) |
| `--network` | Host or `host:port` to authorize (repeatable; never granted implicitly) |
| `--credential` | Credential name to authorize (repeatable; never granted implicitly) |
| `--github` | GitHub scope to authorize (repeatable; never granted implicitly) |
| `--force` | Replace an existing envelope (without it, pika prints the diff and refuses) |
| `--json` | Emit the result as JSON on stdout |
| `--root <dir>` | Repository root (default: discovered) |

### Scopes

| Scope | `fs_write` grants | `exec` grants |
|---|---|---|
| `read` | none — nothing mutating at all | none, unless you pass `--exec` |
| `project` | `.project`, `docs`, `review` — the directories pika owns | every gate command in your contract, plus any `--exec` |
| `repo` | `.` — the whole repository tree | every gate command in your contract, plus any `--exec` |

`read` works in a repository that was never adopted, because it authorizes no change and therefore needs no contract.

`project` and `repo` work there too: the write grant does not depend on a contract. Exec grants are derived from the gates a contract resolves to, so before `pika init` or `pika adopt` there are none to derive — `authorize` says so on stderr, writes the envelope with no `exec`, and exits 0. That is the state `preview_plan` runs in, and it is why the remediation `pika doctor` prints works before adoption. Re-run `pika authorize --force` afterwards to pick up the contract's gate commands. A contract that exists and does not parse is still an error: it is a defect to fix, not a grant to skip.

`exec` grants are **whole argv lines**, whether derived or explicit: `go build -o /dev/null ./...`, not `go`; `--exec "make test"`, not `--exec make`. That is what the enforcement side matches against — `Envelope.Allows` compares an entry element-wise against the whole line the call site asks about, so `make` alone authorizes a bare `make` and denies `make test`. It is also the tighter grant: authorizing bare `go` would additionally authorize `go build -o /anywhere`, a command no gate runs.

`--exec` is the only way to authorize a command no contract declares — a discovered check command `preview_plan` would run before adoption, for instance. pika deliberately never derives those grants from discovery: `preview_plan` exists to inspect repositories nobody has vetted, and letting an unvetted tree grant itself execution would make the envelope a rubber stamp rather than a grant. Deriving from a *committed* contract is safe for the opposite reason — the contract is operator-owned. When a denial happens, the message names the exact invocation, e.g. `run "pika authorize --exec \"make test\""`.

Generated for this repository:

```yaml
# Generated by "pika authorize". Local-only: .project/state/ is gitignored.
# Deny-by-default: anything absent here is refused.
schema: 1
allow:
  fs_write:
  - .project
  - docs
  - review
  exec:
  - go build -o /dev/null ./...
  - go run ./cmd/pika version
  - go test ./... -count=1
  - go vet ./...
  - gofmt -l -w .
rollback_boundary: repository
```

### The envelope is local-only

`.project/state/envelope.yaml` is **written at mode 0600, lives under the gitignored `.project/state/`, and is never committed.** It records what *you* have authorized on *this* machine; it is not a project-level policy others inherit. Re-running `authorize` on an existing envelope tightens the file back to 0600 and, unless `--force` is given, prints the delta and refuses rather than silently widening or narrowing what an agent may do.

`budget` is deliberately never written. No code in the binary compares spend against a ceiling, and a ceiling nothing enforces is a lie in a file whose entire job is to be true. `network`, `credential`, `github` and `fs_read` are written when you ask for them but have no enforcement call site yet — see [../reference/m1-5-delta.md](../reference/m1-5-delta.md).

Only the **MCP surface** authorizes. `pika check` from your shell runs its gates directly and needs no envelope: the envelope exists to bound what an agent may do on your behalf, not to make you ask permission to run your own tests.

---

## 8. Get help

```sh
pika              # same as pika help
pika help         # every command, one line each
pika help check   # one command's usage line and flags
pika --version
```

The help text is generated from the dispatch table, so a command that exists is listed and a command that is listed exists. There is no second copy to fall out of date.

---

## 9. Connect an AI agent (MCP)

```sh
cd /path/to/project
pika mcp
```

Serves the kernel over stdio JSON-RPC. Point any MCP-compatible agent harness at the command `pika mcp`. Read tools (inspect repo, read contract, preview plan, run checks) work immediately; mutating tools require a capability envelope at `.project/state/envelope.yaml` — deny-by-default, so an agent cannot touch anything you have not explicitly authorized.

---

## 10. Hand a failed check to Codex

```sh
pika handoff
```

`handoff` runs `pika check --all --json`, selects only failed check gates, and invokes the configured `agents.builder` when its runtime is `codex`. It runs Codex locally with a writable-workspace sandbox, automatic review, and network access disabled; it never uses a danger or approval-bypass mode. Pika also refuses the handoff if the agent changes Git history.

The private bundle is written under `.project/state/handoffs/<run-id>/`:

| File | Purpose |
| --- | --- |
| `checks-before.json` | Redacted baseline Pika report |
| `prompt.md` | Failed gates and safe repair instructions |
| `codex-last-message.md` | Codex's final response |

Warnings are not repair tasks. Intentional vendor assets, public filenames, and generated outputs must instead be covered by the applicable record in `.project/exceptions.yaml`; covered findings are excluded before the handoff is built.

Choose another configured Codex agent with `pika handoff --agent <name>`. Its configured `model` and `effort` are forwarded to Codex. Use `--json` for automation.

---

## 11. Improve a repository without opening a PR

```sh
pika improve
```

`improve` requires a clean worktree. It runs a baseline check; if it is already green, it exits without creating a branch or calling Codex. For failures, it creates `chore/pika-improve`, performs the Codex handoff, reruns the same Pika checks, and commits only a non-empty, verified diff with the message `chore: improve verified findings`.

Use `--branch <name>` to choose another local branch and `--agent <name>` to select a configured Codex builder. On a failed handoff or recheck, Pika leaves the branch and agent edits uncommitted so they can be inspected. `improve` never pushes, opens a pull request, or merges anything.

Your contract needs a Codex builder, for example:

```yaml
agents:
  builder:
    runtime: codex
```

---

## Typical loops

**New project, end to end:**

```sh
mkdir my-app && cd my-app
pika init --profile typescript
pika check --all
git init && git add -A && git commit -m "init: pika scaffold"
```

**Existing project, end to end:**

```sh
cd my-legacy-repo
pika adopt        # read-only; review/adoption-review.md tells you what it found
pika apply        # transactional; review/adoption-review.md becomes APPLIED
pika doctor       # what is configured, without running a gate
pika check --all  # baseline green (or honest skips for missing toolchains)
```

**Handing the repository to an agent:**

```sh
pika authorize --scope project   # writes .project/state/envelope.yaml (0600, local-only)
pika doctor                      # confirms the grants
pika mcp                         # serve the kernel to the agent
```

**Everyday:**

```sh
pika check --changed  # while iterating; degrades loudly to --all when git cannot answer
pika check --all      # before pushing
```

**Something is off:**

```sh
pika doctor                       # root, contract, lock, exceptions, envelope, gates, git
pika explain <rule-or-gate-or-code>
```

---

## Exit codes

All commands: `0` success, `1` failure, `2` usage/config error.

## JSON output

Every `--json` payload is the same envelope, whatever produced it:

```json
{
  "schema": 1,
  "command": "check",
  "ok": true,
  "result": { }
}
```

`result` is the command's own report, unchanged — `check` nests the verification report, `doctor` its findings, `init` the created-file manifest. `ok` is the boolean the exit code is derived from: `ok:false` means exit `1`.

A usage or configuration error (exit `2`) replaces `result` with `error` and prints nothing on stderr:

```json
{
  "schema": 1,
  "command": "check",
  "ok": false,
  "error": {"code": "usage", "message": "unexpected argument \"junk\""}
}
```

`code` is `usage` (the invocation was wrong) or `config` (the repository state prevents the command from running). If `--json` itself could not be parsed — an unknown flag, for instance — the error is plain text on stderr instead, because there is no payload to put it in.

`schema` is the envelope version and only changes on a breaking shape change; `pika mcp` is not covered by it, as it speaks JSON-RPC rather than `--json`.

## Where state lives

| Path | What | Committed? |
|---|---|---|
| `.project/contract.yaml` | Live project contract | Yes |
| `.project/profiles.lock` | Pinned profile digests | Yes |
| `.project/exceptions.yaml` | Recorded naming exceptions | Yes |
| `.project/state/` | Board, recovery journals, handoff bundles, raw transcripts | **Never** (gitignored by init) |
| `.project/state/envelope.yaml` | Capability envelope — mode 0600, local-only, machine-specific | **Never** |
| `review/adoption-review.md` | Human-readable adoption review | Yes |

## Upgrading: `profiles.lock` written by an older pika

M1.5 edited the embedded profile packs, which rotated the pack digests. Any `.project/profiles.lock` written by an earlier build now fails gate 1 with a digest mismatch — the lock is doing its job; the packs really did change.

```sh
pika init --force     # rewrites the managed files, including profiles.lock
pika check --all      # gate 1 goes green again
```

`--force` regenerates the managed files under `.project/` and never touches your own files outside it. There is deliberately no in-place lock repair: a lock you can hand-edit back to green proves nothing.
