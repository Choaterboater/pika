# pika

Small, cute, relentless: a provider-neutral project operating system kernel.

Pika gives AI coding agents (and the humans steering them) one coherent workflow for creating new repositories, adopting existing ones, and running features, fixes, review, verification, delivery, and maintenance — backed by a deterministic Go kernel that owns every mutation, permission check, and verification receipt.

- **Usage guide:** [docs/guides/usage.md](docs/guides/usage.md) — step-by-step for every command
- **Spec:** [docs/superpowers/specs/2026-08-28-pika-design.md](docs/superpowers/specs/2026-08-28-pika-design.md)
- **M1 plan:** [docs/superpowers/plans/2026-08-28-pika-m1-kernel.md](docs/superpowers/plans/2026-08-28-pika-m1-kernel.md)

## What it is

Pika combines two halves with a strict authority split:

- **AI agents** decide — research, architecture, task decomposition, implementation strategy, review.
- **The pika kernel** transacts — strict contract parsing, capability enforcement, safe diffs with rollback, deterministic verification, and CI parity.

The repository contract (`.project/contract.yaml`) is the project-level source of truth: selected profile packs, naming rules and exceptions, verification commands, agent role mappings, and GitHub policy.

## Status

> ### Upgrade note — regenerate `profiles.lock`
>
> Milestones 1.5, 2 and 3 each rotated `profiles.PackDigest()`. **Every `.project/profiles.lock` written by an earlier pika build now fails gate 1** with a digest mismatch naming the pack — the lock is doing its job, the packs really did change. M3's rotation came from folding each pack's templates into its digest, which is what finally lets an adopted repository be told its scaffolded CI workflow is out of date. Regenerate:
>
> ```sh
> git status --porcelain   # clean: the diff below is then only the command's work
> pika init --force        # profiles, name and module are read back from the repository
> git diff                 # the contract, the lock, the PR template, the CI workflow
> pika check --all         # gate 1 goes green again
> ```
>
> **`--force` no longer costs you anything you wrote.** It regenerates what the kernel owns — the contract, the lock, the PR template, the CI workflow — and leaves `README.md`, `AGENTS.md`, `CONTRIBUTING.md`, `.gitignore`, the language scaffold and `.project/exceptions.yaml` exactly as they are. Profiles, project name and Go module path are resolved as explicit flag → read back from the repository → refuse, so the bare command above is the whole upgrade. `--reset-docs` is the explicit opt-in for the old behavior; nothing restores a deleted exception, so nothing deletes one. Read [the flag's full behavior](docs/guides/usage.md#--force-regenerates-more-than-the-lock). There is no in-place lock repair: a lock you can hand-edit back to green is a lock that proves nothing.

**Milestone 1 (deterministic kernel) complete.** Shipped:

| Area | What works |
|---|---|
| Contract | Strict YAML parsing (duplicate-key AST walk, unknown-key rejection, JSON Schema validation) |
| Discovery | Stack detection for Go, TypeScript/JavaScript, Python, Swift, Rust; workspace/monorepo split |
| Profiles | Composable packs (core + 5 language packs) with sha256 lock digests, validated at check time |
| Verification | 5-gate ladder (`check`, `check --ci`), process-group kill, injectable timeout, local/CI parity |
| Checks | Naming rules, exception records (`.project/exceptions.yaml`), generated-file ownership |
| Envelope | Capability authorization, deny-by-default (`fs_write`/`exec`/`network`/`credential`/`github`/budget) |
| Adoption | Read-only inventory + draft contract + deterministic preview, then transactional `apply` (drafts promoted, missing core files created, user files preserved); human-readable review at `review/adoption-review.md` |
| Init | Lean scaffolds for 5 stacks with golden-dir tests |
| Transactions | Crash-safe write-ahead journal, idempotent recovery, atomic writes with fsynced parent chains |
| Redaction | Credential/PII scrubbing (RE2, longest-match spans, bounded findings) |
| Evidence | Schema-validated receipts, redact-everything invariant, atomic write |
| MCP | JSON-RPC stdio server: `inspect_repo`, `read_contract`, `preview_plan`, `run_checks`, `acquire_scope`, `release_scope`, `publish_evidence`, and more |

**Milestone 1.5 (ergonomics) complete.** Added:

| Area | What works |
|---|---|
| Help | `pika help` / bare `pika`, generated from the dispatch table so it cannot drift from the registered commands |
| Roots | `--root <dir>` on every command; otherwise discovered by walking up for `.project/contract.yaml`, then the draft, then `.git`. `init` never discovers |
| Doctor | `pika doctor`: root, contract, lock, exceptions, envelope, per-gate command or pack hint, toolchain, git — and it never executes a gate |
| Explain | `pika explain <id>`: naming rules, gate ids, and MCP error codes, with rationale, remediation, and an exception record that actually parses |
| Authorize | `pika authorize [--scope read\|project\|repo]` writes `.project/state/envelope.yaml` at mode 0600 — the hand-authored-YAML barrier to using an agent is gone. `--exec`, `--network`, `--credential` and `--github` are the explicit grants; nothing in them is ever implicit |
| Exec enforcement | MCP `run_checks` authorizes every gate it will spawn, and `preview_plan` every discovered check command its baseline runs; the human CLI deliberately needs no envelope. Exec grants are **whole argv lines** (`--exec "make test"`, not `--exec make`), because the matcher compares element-wise |
| Scoped checks | `check --changed` resolves a real git diff and degrades loudly — it never silently narrows verification |
| Self-governance | pika's own `.project/contract.yaml`, `profiles.lock` and `exceptions.yaml` are committed, and CI runs `pika check --ci` with the binary built from the commit under test |

Envelope enforcement coverage and the M1.5 pack-digest rotation are recorded in [docs/reference/m1-5-delta.md](docs/reference/m1-5-delta.md).

**Milestone 2 (durable work) complete.** Added:

| Area | What works |
|---|---|
| Run records | Every run writes a durable record at `.project/state/work/<work-id>/`, saved atomically at each phase transition, with the handoff bundle inside it. Corruption is reported, never repaired |
| Work ids | Minted from `crypto/rand`. They were derived from the clock and the slug, so two runs of the same kind within one second shared an id — and the second one's receipt overwrote the first's |
| One lifecycle | Repair (`improve`) and feature (`work`) work run the same state machine and differ in exactly one decision: a green baseline means repair is already done, and says nothing about whether a goal has been met |
| Kernel-issued evidence | The kernel writes `.project/evidence/<work-id>.json` from the finished run record — commit and tree read back out of Git. Previously only an agent over MCP could, supplying every field itself |
| Work commands | `pika work` (goal → verified commit), `pika status` (what has run), `pika resume` (finish an interrupted run), `pika recover` (unwedge a crashed transaction) |
| Ladder re-entrancy | `verify.Run` refuses a nested ladder structurally, scoped to the tree under verification — not by convention, and never by silently skipping into a green report |
| Gates that can fail | Packs carry `fail-on-output`, so `gofmt -l .` fails on drift instead of `gofmt -l -w .` rewriting the tree it was verifying. pika's compensating `git diff --exit-code` CI step is gone |
| Scaffolded CI | The generated workflow pins the kernel to the pika release that scaffolded the repository instead of installing `@latest`, drops the `paths:` filter that silently exempted every directory added after scaffold time, and checks out with full history so `check --changed` can resolve a merge base |
| Doctor | `pika doctor` cross-checks the envelope against each gate's whole argv, so a grant that would not cover a gate is a warning now rather than an `envelope_denied` mid-task |
| End-to-end | `internal/e2e` drives the real binary through `work`, `status`, `resume` and `recover` inside temp repositories, with a fake agent binary standing in for `codex` — no model, credential or network anywhere |

What M2 changed underneath existing repositories, and two known gaps it deliberately left open, are recorded in [docs/reference/m2-delta.md](docs/reference/m2-delta.md). Its envelope table — `fs_read`, `network`, `credential`, `github` and `budget` schema-only with no enforcement call site — was true when M2 shipped and is superseded by M3 below.

**Milestone 3 (trust and the upgrade path) complete.** Added:

| Area | What works |
|---|---|
| Safe regeneration | `pika init --force` regenerates only what the kernel owns (contract, lock, PR template, CI workflow) and reads profiles, project name and Go module back out of the repository. Your `README.md`, `AGENTS.md`, `CONTRIBUTING.md`, `.gitignore`, language scaffold and — above all — `.project/exceptions.yaml` are left alone. `--reset-docs` is the explicit opt-in for the old behavior, and even it does not touch the exceptions record |
| Stale scaffolds are detectable | Pack templates are inside `PackDigest`, so a repository scaffolded from a since-corrected template fails gate 1 by name instead of silently keeping an `@latest` CI workflow forever. This closes [M2 gap 1](docs/reference/m2-delta.md#gap-1--a-template-only-pack-change-is-invisible-to-every-adopted-repository) |
| …and fixable | `pika apply` refreshes the two kernel-owned files through the transaction and reports every rewrite as a `write`; operator-owned files stay create-if-missing. In an already-adopted repository, where `apply` refuses, `pika init --force` is the refresh |
| Envelope, fail-closed | `fs_read` is enforced at the MCP read tools, so `inspect_repo` and `read_contract` now need an envelope like every other tool. That is a requirement to *declare* policy, not a narrowing of read scope. `network`, `credential`, `github` and `budget` get no check on purpose — the binary performs no operation of those classes, and the evidence is written down rather than asserted |
| Containment | Every git path listing pika parses is read with `-z`, so a path git would otherwise quote can no longer slip past the private-state filter; verified paths are staged with `--literal-pathspecs`, so a filename containing `*`, `?` or `[` cannot drag untouched files into a commit; and `record.json` and the MCP board are redacted at the point of writing |

The evidence for each — including the one error code that moved on a documented surface, and what the `fs_read` change does *not* mean — is in [docs/reference/m3-delta.md](docs/reference/m3-delta.md).


## Install

Requires Go 1.26+.

```sh
go install github.com/Choaterboater/pika/cmd/pika@latest
```

## Quick start

```sh
# Create a new project (5 stacks supported)
pika init --profile go --name my-service

# Verify it
cd my-service
pika check --all

# Analyze an existing repository without touching it
pika adopt

# Promote the adoption drafts into a live contract (transactional)
pika apply

# Find out what is wrong before running anything
pika doctor

# Understand a rule, a gate, or an error code
pika explain naming-kebab-case

# Authorize an agent to write (local-only, never committed)
pika authorize --scope project

# Hand a goal to the builder agent and get one verified local commit
pika work "add a /healthz endpoint that returns 200"

# Expose the kernel to your AI agent over MCP
pika mcp
```

## Commands

| Command | Purpose |
|---|---|
| `pika init` | Create a lean project contract and scaffold for a new repository |
| `pika adopt` | Inventory an existing repository; produces a draft contract and migration preview without changing working code |
| `pika apply` | Promote the adoption drafts into a live contract transactionally — operator-owned files create-if-missing, the two kernel-owned files refreshed and reported, full rollback on failure, and a rewritten human-readable review bundle |
| `pika recover` | Report a transaction that never finished — holder, liveness, and every file a rollback would touch — and undo it with `--apply` |
| `pika check` | Run the verification ladder locally or in CI (`--all`, `--changed`, `--ci`; `--ci` makes no LLM calls) |
| `pika status` | List the durable work runs this repository has, or report one in full: phases, branch, commit, outcome, and the reason it stopped |
| `pika doctor` | Diagnose contract, lock, exceptions, envelope, per-gate command, toolchain, and git — without executing a single gate |
| `pika explain` | Explain a naming rule, a verification gate, or an MCP error code: rationale, remediation, and a copy-pasteable exception record |
| `pika authorize` | Generate the capability envelope agents need, at `.project/state/envelope.yaml` (mode 0600, local-only, never committed) |
| `pika handoff` | Give actionable failed checks to the configured Codex builder and save a private handoff bundle |
| `pika improve` | Run checks, let Codex repair failed gates, recheck, and make one verified local commit |
| `pika work` | Run a stated goal through the same verified lifecycle: branch, builder agent, recheck, one verified local commit |
| `pika resume` | Continue an interrupted work run from the phase its record proves it reached, or refuse with the specific reason it cannot |
| `pika mcp` | Serve the kernel to agents over MCP (stdio JSON-RPC) |
| `pika help` | Describe pika, or one command's flags — generated from the dispatch table, so help cannot drift from the registered commands |

Running `pika` with no arguments prints the same help.

All commands support `--json` for automation, and every payload is the same envelope — `{"schema":1,"command":…,"ok":…,"result":{…}}` — so a consumer can tell which command answered, and whether it succeeded, before knowing the report's shape. See [docs/guides/usage.md](docs/guides/usage.md#json-output).

### `--root`, and the one command that does not discover

Every command accepts `--root <dir>`. Without it, the repository root is discovered by walking up from the working directory for `.project/contract.yaml`, then `.project/contract.yaml.draft`, then `.git` — so `pika check` from a deep subdirectory reports on the repository, not on the folder you happen to stand in.

`pika init` deliberately does **not** discover. It scaffolds where you stand. Running `init` inside a subdirectory of an existing repository creates a new project *there*, instead of silently re-scaffolding the enclosing repository.

## Design principles

1. **Adaptive, not universal** — one durable repository spine; each ecosystem owns its source layout.
2. **AI decides; the kernel transacts** — agents may decide what should change; only the kernel decides whether and how it is safely applied.
3. **Evidence beats consensus** — source state, executable checks, and recorded decisions determine completion.
4. **Parallelize independent work** — read-only exploration fans out; writers get exclusive scopes or isolated workspaces.
5. **Public-safe history** — the kernel-issued evidence receipt under `.project/evidence/` is redacted, schema-validated and committed; the run record it was issued from, with its prompts, gate output and the agent's own words, stays local under `.project/state/` (gitignored by `init`, and redacted at the point of writing since M3). Operational state and public attestation are separate on purpose: the receipt is validated and meant to be published, the record is neither.

## Development

```sh
go build ./...
go test ./... -count=1
CGO_ENABLED=0 go build ./...   # the shipped binary is CGO-free
```

### pika governs pika

This repository is adopted by its own kernel. `.project/contract.yaml`, `.project/profiles.lock` and `.project/exceptions.yaml` are committed; `.project/state/` is gitignored. `.github/workflows/ci.yml` builds the binary **from the commit under test** — never `go install ...@latest` — and runs `pika check --ci` on this repository, so a change that would break the verifier is caught by the verifier it breaks.

The suite CI runs includes `internal/e2e`, which drives the real binary through the whole durable lifecycle — and through the upgrade path: `--force` over a hand-written README and a recorded exception, a stale lock detected by name, and `apply` refreshing a kernel-owned file — inside temp repositories, with a fake agent binary on `PATH` in place of `codex`. No model, credential or network is involved anywhere, so `pika check --ci` stays provably LLM-free while still covering the path that spawns an agent.

```sh
go build -o /tmp/pika ./cmd/pika
/tmp/pika doctor
/tmp/pika check --all
```

pika's own naming and file-size rules skip dot-prefixed path segments, so `.project/`, `.github/` and `.superpowers/` are exempt from the rules pika applies to itself. See [AGENTS.md](AGENTS.md).

Cross-platform: macOS, Linux, Windows. The txn/verify fsync paths use build-tagged sync implementations (Windows tolerates read-mode `FlushFileBuffers` denials, documented in `internal/fsutil`). Process liveness is build-tagged too, and on Windows it cannot be determined — see the known gaps in [docs/reference/m2-delta.md](docs/reference/m2-delta.md#7-known-gaps-deliberately-not-closed).

## License

MIT — see [LICENSE](LICENSE).
