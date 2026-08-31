# pika M5 Design Specification — The Agent Skill Layer

**Status:** Approved
**Date:** 2026-08-30
**Product:** `pika`
**Builds on:** [2026-08-30-pika-m4-safe-concurrency-design.md](2026-08-30-pika-m4-safe-concurrency-design.md)
**Implements:** [2026-08-28-pika-design.md](2026-08-28-pika-design.md) §5.2, §9.2, and §16's projection-digest gate

## 1. Purpose

Four milestones built a kernel an agent can drive safely. None built the thing that tells an agent *how*.

Connect Codex, omp or Claude Code to `pika mcp` today and it receives capability with no procedure. It can call `run_checks`, `preview_plan` and `acquire_scope`, but nothing tells it that a green ladder is the only evidence that counts, that a `blocked` outcome means diagnose rather than retry, that `scope_conflict` means another writer holds the repository, that `.project/state/` must never be committed, or that the kernel — not the agent — issues the receipt.

All of that knowledge exists. It lives in `docs/guides/usage.md`, which a human reads and an agent does not.

M5 builds the workflow layer the original spec named in §5.2 and §9.2 and every milestone deferred.

## 2. Goals

1. Ship canonical, portable skills under `.agents/skills/` that teach an agent to drive pika correctly.
2. Generate harness-native projections from those canonical skills, so Codex, omp and Claude Code each read a form they understand.
3. Fail `pika check` when a projection drifts from its source, so the copies cannot rot.
4. Consume `agent-guidance`, a pack field that has been declared and empty since M1.
5. Derive every instruction from behavior that actually exists.

## 3. Non-goals

- New MCP tools, roles beyond `builder`, a task graph, multi-agent — all still without a demonstrated consumer (M4 §3).
- A skill registry, marketplace, or remote fetch.
- Teaching an agent general engineering practice. These skills describe **pika**, not software development.
- Harness-specific runtime integration beyond emitting a file each harness already reads.

## 4. Current-state findings

Verified against `main` at `78c12b8`.

| Finding | Evidence |
|---|---|
| `.agents/` does not exist and `init` emits nothing under it | repository root; `internal/initcmd/testdata/golden/*/` |
| Spec §5.2: "The workflow layer consists of portable Agent Skills and role definitions" | `docs/superpowers/specs/2026-08-28-pika-design.md:122` |
| Spec §9.2 names four canonical skills under `.agents/skills/`: `project-work`, `project-research`, `project-review`, `project-maintain` | same file, `:409-414` |
| Spec §9.2 also specifies the mechanism: "Harness-native projections are generated only for clients that cannot consume the canonical location. Projections identify their source and digest; CI rejects drift rather than maintaining parallel handwritten copies" | same file, `:416` |
| Spec §16 already requires CI to validate "generated projection digests" — the gate has a home in gate 1 and needs no new command | same file, `:698-706` |
| `agent-guidance` is declared on every pack and empty in all six | `internal/profiles/packs/*.yaml` |
| `Pack.AgentGuidance []string` is parsed but never surfaced on `Resolved` | `internal/profiles/registry.go` |
| The only agent-facing file `init` emits is a static 22-line `AGENTS.md` | `internal/profiles/packs/core@1/templates/AGENTS.md.tmpl` |
| An agent has no way to learn any of: the ladder is the evidence, `blocked` ≠ retry, `scope_conflict` semantics, receipts are kernel-issued, private state is never committed | absence |

## 5. Design

### 5.1 Canonical skills

Four skills at `.agents/skills/<name>/SKILL.md`, emitted by `init` and by `apply`'s create-if-missing path, owned by `core@1` templates like every other core file.

They are **operator-owned once written** — `--force` regenerates only kernel-owned files (M3 §5.1), and a project that has tuned its skills must not lose them. `--reset-docs` restores the templates.

| Skill | Teaches |
|---|---|
| `project-work` | Route a goal or a failure through the lifecycle: `improve` for repair, `work "<goal>"` for a feature, `resume` after interruption, `recover` first when a lease is stale. What each refusal means. |
| `project-research` | Read the repository through `inspect_repo`, `read_contract`, `explain`; where the contract, lock and exceptions live and what they mean. |
| `project-review` | Evidence-backed review: the ladder is the evidence, the receipt is kernel-issued and must not be written by the reviewer, a warning is not a failure. |
| `project-maintain` | Drift, digests, `doctor`, the upgrade path, and why `--force` is now safe. |

Content is derived from behavior that exists. Every claim in a skill must be true of the binary at the commit that ships it — this project has corrected shipped documentation twice for exactly that reason, and a false instruction to an agent is worse than to a human because the agent cannot notice.

### 5.2 Projections, generated and digest-gated

Harnesses read different files. Rather than maintaining parallel handwritten copies — the failure the spec explicitly names — each projection is **generated from the canonical skill** and carries a header naming its source path and the source's digest.

`pika check` gate 1 verifies every projection's recorded digest against its source and fails on drift. Spec §16 already requires this; gate 1 already validates the profile lock the same way, so the mechanism and its error shape exist.

A projection is kernel-owned: `apply` refreshes it and `--force` regenerates it, because it is a generated artifact and never an authored one.

Which harnesses receive a projection is a contract concern, not a hardcoded list. The contract declares them; the kernel emits only what is declared.

### 5.3 `agent-guidance` finally consumed

`Pack.AgentGuidance` is parsed and dropped. M5 surfaces it on `Resolved` and composes it into the generated skills, so a Go repository's skill can carry Go-specific guidance and a TypeScript one different guidance, without forking the skill.

Editing any pack rotates `PackDigest`, which now covers templates (M3 §5.2) — so a corrected skill template propagates the same way a corrected CI workflow does, and an adopted repository learns its skills are stale rather than silently keeping them.

### 5.4 What the skills must say

The non-obvious rules, each earned by a defect this project actually hit:

- **The ladder is the evidence.** A green `pika check` is the only completion signal. Never claim done from a narrative.
- **`blocked` means diagnose, not retry.** A blocked run recorded a reason; read it.
- **`scope_conflict` and a run-lease refusal mean another writer holds the repository.** Do not wait, do not retry in a loop, do not remove a lock file. `pika recover` clears only a provably dead holder.
- **Never commit `.project/state/`.** It holds prompts, reports and — briefly — the raw agent transcript. The kernel filters it; do not defeat the filter by moving or copying content out.
- **The receipt is issued by the kernel.** Do not write one. `publish_evidence` refuses to overwrite one.
- **A warning is not a failure.** `file-size-review` warnings are visible by design and do not fail a gate.
- **Reads require an envelope on the MCP surface.** `pika authorize --scope read` is the remedy; `envelope_denied` is not a bug in your request.

## 6. Error handling

- A projection whose source is missing fails gate 1 naming both paths.
- A projection whose digest does not match fails gate 1 naming the pack and the remedy.
- An undeclared harness is a contract validation error, not a silent skip.
- Skill emission never overwrites an operator-edited skill; `apply` creates only what is missing.

## 7. Testing

- `init` emits the four canonical skills for every stack; golden trees updated.
- A tampered projection fails gate 1 with a message naming the source.
- A regenerated projection passes.
- `--force` preserves an edited skill; `--reset-docs` restores it.
- `agent-guidance` from a language pack appears in that stack's generated skill and not in another's.
- Every command, flag and error code named in a skill exists — reusing the `TestEveryCommandNamedInAMessageIsRegistered` guard's approach, extended to the skill templates. A skill that names a command pika does not have is the exact failure M3 fixed in `adopt`.
- E2E: a scaffolded repository's skills and projections are present, consistent, and pass `check`.

## 8. Completion definition

M5 is complete when:

1. `init` and `apply` emit four canonical skills under `.agents/skills/`;
2. projections are generated from them, each recording source path and digest;
3. `pika check` gate 1 fails on projection drift and names the remedy;
4. `agent-guidance` is surfaced on `Resolved` and composed into the generated skills;
5. skills are operator-owned: `--force` preserves edits, `--reset-docs` restores templates;
6. every command, flag and error code named in any skill is verified to exist by a test;
7. `go test ./... -count=1` passes, `CGO_ENABLED=0 go build ./...` succeeds, `go.mod` still declares exactly two direct dependencies, and `pika check --ci` passes in GitHub Actions.
