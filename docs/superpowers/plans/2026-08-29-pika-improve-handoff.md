# Pika Improve and Codex Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe `pika improve` loop that gives only failed check gates to Codex, verifies its edits, and commits them on a local branch without creating or merging a PR.

**Architecture:** A new `internal/improve` package owns the deterministic orchestration and exposes injected process execution for tests. `pika handoff` prepares a local run bundle and invokes Codex with the workspace-write sandbox; `pika improve` performs the branch, baseline, handoff, recheck, and verified-commit sequence. Run bundles live below `.project/state/` and remain untracked.

**Tech Stack:** Go 1.26, standard-library `os/exec`, `verify.Report`, Git CLI, Codex CLI.

**Spec:** `docs/superpowers/specs/2026-08-28-pika-design.md`

## Global Constraints

- Never push, create a PR, or merge.
- Refuse dirty trees, missing Codex builder configuration, failed handoffs, and failed rechecks before committing.
- Only failed check gates are repair instructions. Warnings and exception-suppressed findings are context only.
- Invoke Codex with `--sandbox workspace-write --approve-for-me`; never use bypass or danger flags.

---

### Task 1: Handoff bundle and Codex runner

**Files:** Create `internal/improve/handoff.go` and `internal/improve/handoff_test.go`.

**Interface:** `CreateHandoff(root string, report *verify.Report, runner Runner) (Handoff, error)` writes a prompt and result path; `Runner.Run(ctx, root, promptPath, outputPath)` invokes the agent.

- [ ] **Step 1:** Write a failing test that gives `CreateHandoff` a failed lint gate plus a warning and asserts the prompt contains the lint output but excludes the warning.
- [ ] **Step 2:** Run `go test ./internal/improve -run TestCreateHandoffWritesOnlyFailedGatesToPrompt -count=1`; expect an undefined-package or undefined-symbol failure.
- [ ] **Step 3:** Implement the bundle, a `CodexRunner`, and a fakeable runner boundary using `exec.CommandContext` with argument slices only.
- [ ] **Step 4:** Run `go test ./internal/improve -count=1`; expect pass.

### Task 2: Git and verification orchestration

**Files:** Create `internal/improve/improve.go` and `internal/improve/improve_test.go`.

**Interface:** `Run(ctx context.Context, config Config) (Result, error)` records baseline and final reports, creates the configured branch only after a failing baseline, and returns branch, bundle, changed files, and commit.

- [ ] **Step 1:** Write failing tests for dirty-tree refusal, a passing baseline that creates no branch, a failed post-handoff check that commits nothing, and a repaired failure that commits after a green recheck.
- [ ] **Step 2:** Run `go test ./internal/improve -run TestRunCommitsOnlyAfterVerifiedRecheck -count=1`; expect an undefined-symbol failure.
- [ ] **Step 3:** Implement Git status/switch/diff/commit and Pika check calls with explicit argv; on any error preserve the branch and uncommitted agent changes for inspection.
- [ ] **Step 4:** Run `go test ./internal/improve -count=1`; expect pass.

### Task 3: CLI and documentation

**Files:** Create `cmd/pika/handoff.go` and `cmd/pika/improve.go`; modify `cmd/pika/main.go`, `cmd/pika/check.go`, `cmd/pika/check_test.go`, `README.md`, and `docs/guides/usage.md`.

**Interface:** `pika handoff [--agent builder] [--json]` creates and runs a focused handoff. `pika improve [--branch chore/pika-improve] [--agent builder] [--json]` runs the complete verified local workflow.

- [ ] **Step 1:** Write failing command tests for parser errors and JSON output.
- [ ] **Step 2:** Run `go test ./cmd/pika -run TestImprove -count=1`; expect the missing command failure.
- [ ] **Step 3:** Add root dispatch, CLI flags, contract-agent validation, human/JSON output, and usage documentation.
- [ ] **Step 4:** Run `gofmt -w cmd/pika internal/improve`, `go test ./... -count=1`, `go vet ./...`, and `git diff --check`.
- [ ] **Step 5:** Commit all implementation, tests, docs, and this plan with `git commit -m "feat: add verified improve handoff"`.
