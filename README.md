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

# Expose the kernel to your AI agent over MCP
pika mcp
```

## Commands

| Command | Purpose |
|---|---|
| `pika init` | Create a lean project contract and scaffold for a new repository |
| `pika adopt` | Inventory an existing repository; produces a draft contract and migration preview without changing working code |
| `pika apply` | Promote the adoption drafts into a live contract transactionally — create-if-missing, full rollback on failure, and a rewritten human-readable review bundle |
| `pika check` | Run the verification ladder locally or in CI (`--ci` makes no LLM calls) |
| `pika mcp` | Serve the kernel to agents over MCP (stdio JSON-RPC) |

All commands support `--json` for automation.

## Design principles

1. **Adaptive, not universal** — one durable repository spine; each ecosystem owns its source layout.
2. **AI decides; the kernel transacts** — agents may decide what should change; only the kernel decides whether and how it is safely applied.
3. **Evidence beats consensus** — source state, executable checks, and recorded decisions determine completion.
4. **Parallelize independent work** — read-only exploration fans out; writers get exclusive scopes or isolated workspaces.
5. **Public-safe history** — sanitized evidence is committed; raw transcripts stay local under `.project/state/` (gitignored by `init`).

## Development

```sh
go build ./...
go test ./... -count=1
CGO_ENABLED=0 go build ./...   # the shipped binary is CGO-free
```

Cross-platform: macOS, Linux, Windows. The txn/verify fsync paths use build-tagged sync implementations (Windows tolerates read-mode `FlushFileBuffers` denials, documented in `internal/fsutil`).

## License

MIT — see [LICENSE](LICENSE).
