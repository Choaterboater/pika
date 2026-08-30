# Using pika

Step-by-step usage for every pika command. All commands operate on the folder you are standing in — `cd` into the project first.

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
pika check --ci           # same engine CI runs; no LLM calls
pika check --json         # machine-readable report
```

The verification ladder: contract integrity → formatting/lint/compile → tests → smoke → (in agent runs) independent review.

Gate 1 also validates that your `profiles.lock` matches the contract and the embedded pack digests — a hand-edited lock or drifted contract fails here.

---

## 5. Connect an AI agent (MCP)

```sh
cd /path/to/project
pika mcp
```

Serves the kernel over stdio JSON-RPC. Point any MCP-compatible agent harness at the command `pika mcp`. Read tools (inspect repo, read contract, preview plan, run checks) work immediately; mutating tools require a capability envelope at `.project/state/envelope.yaml` — deny-by-default, so an agent cannot touch anything you have not explicitly authorized.

---

## 6. Hand a failed check to Codex

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

## 7. Improve a repository without opening a PR

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
pika check --all  # baseline green (or honest skips for missing toolchains)
```

**Everyday:**

```sh
pika check --all  # after changes
```

---

## Exit codes

All commands: `0` success, `1` failure, `2` usage/config error.

## Where state lives

| Path | What | Committed? |
|---|---|---|
| `.project/contract.yaml` | Live project contract | Yes |
| `.project/profiles.lock` | Pinned profile digests | Yes |
| `.project/exceptions.yaml` | Recorded naming exceptions | Yes |
| `.project/state/` | Envelope, board, recovery journals, raw transcripts | **Never** (gitignored by init) |
| `review/adoption-review.md` | Human-readable adoption review | Yes |
