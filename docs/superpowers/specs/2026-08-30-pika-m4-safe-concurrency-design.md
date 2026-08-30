# pika M4 Design Specification — Safe Concurrency, Honestly Described

**Status:** Approved
**Date:** 2026-08-30
**Product:** `pika`
**Builds on:** [2026-08-30-pika-m3-trust-and-upgrade-design.md](2026-08-30-pika-m3-trust-and-upgrade-design.md)

## 1. Purpose

The original design spec's next milestone is parallel writers, a task graph, multi-agent collaboration and a SQLite coordination board. This milestone does not build that, and the reason is evidence rather than caution.

A read-only audit found **no demonstrated consumer** for any of it. `pika work` runs exactly one agent; two prior milestones named multi-agent an explicit non-goal; no test, TODO, issue or document in the repository asks for a second concurrent agent. Of the nine record types §9.3 promises, exactly one has a real consumer today, two have write paths with zero readers, and six are speculative absent parallel agents. Building that store now would be constructing storage for consumers that do not exist — the mistake M2 explicitly refused and M3 refused again.

What the audit did find is a hazard that needs no new machinery to reach: **two concurrent `pika work` runs in one repository corrupt each other.** And an MCP tool that tells agents it grants an exclusive lease while storing nothing at all.

M4 closes those two.

## 2. Goals

1. Make a second concurrent mutating run refuse rather than corrupt.
2. Stop advertising a lease that does not exist.
3. Make lock release safe against a process that does not hold it.
4. Close the remaining option-injection surface in git invocations.

## 3. Non-goals

Deferred, with the reason recorded so the decision can be revisited when evidence changes:

- **A SQLite coordination board.** §9.3 mandates it; §18 only constrains *how* SQLite is integrated if it is. One of nine record types has a consumer. `modernc.org/sqlite` brings a build closure on the order of 20–25 modules against pika's current two direct dependencies. Revisit when a second concurrent writer genuinely exists.
- **Multi-agent, roles beyond `builder`, a task graph, typed messages, artifacts.** No demonstrated need.
- **Path-scoped leases with disjointness analysis.** Whole-repository exclusion is what the actual hazard requires; per-path leases serve parallel writers, which do not exist.
- **`apply_plan`.** Still no consumer contract.
- Content-based copy detection (M3 §7 reasoning stands).

## 4. Current-state findings

Verified read-only against `main` at `9fc249a`.

| Finding | Evidence |
|---|---|
| **Two concurrent `pika work` runs are unprotected.** With the default branch they collide non-deterministically at `git switch -c`; with distinct `--branch` flags they proceed and corrupt each other's commits through the shared working tree and shared HEAD | `internal/improve/improve.go` lifecycle; no lock acquired anywhere in it |
| `internal/workrec` has no locking beyond `Create`'s single `os.Mkdir`, which excludes only id collisions, never concurrent runs | `internal/workrec/workrec.go` |
| `acquire_scope` is described to agents as an **"exclusive write lease"** and does an envelope check plus one append-only board line, then returns `granted:true`. No holder identity, no conflict detection, no expiry, no readback | `internal/mcp/server.go:176-183, 570-588` |
| Two sequential `acquire_scope` calls on the same path both return `granted:true` | `internal/mcp/server.go:570-588` |
| `release_scope` never checks that an acquire happened | `internal/mcp/server.go:591-608` |
| The board has four writers and zero non-test readers | `internal/mcp/server.go:584, 810, 827` |
| `internal/txn`'s O_EXCL lock is the only real mutual exclusion in the binary, and it is scoped to `pika apply` alone — it covers nothing in the `work`/`improve` lifecycle | `internal/txn/apply.go:132-176`; single `txn.Begin` call site |
| `releaseLock` is an unconditional `os.Remove` with **no ownership check**, so any process able to write the directory can release another's lock | `internal/txn/journal.go:134-140` |
| `lockInfo` stores `TxID`, `PID`, `StartedAt` — no hostname, so it cannot be validated across machines or on NFS | `internal/txn/journal.go:57-61` |
| Several git invocations lack a `--` separator before a `<branch>`/`<commit>` argument, so a value beginning with `-` is read as an option | `internal/improve/improve.go`, `internal/improve/receipt.go` |

## 5. Design

### 5.1 A real run lease

Mutating lifecycle commands — `work`, `improve`, `handoff`, `resume` — acquire an exclusive per-repository lease before they touch the working tree, and hold it until the run reaches a terminal outcome.

The mechanism is the one already proven in `internal/txn`: an `O_EXCL` lock file whose contents name the holder. It is not reimplemented; the shared primitive is lifted into a package both callers use, so a second exclusion implementation cannot drift from the first.

Held-lock behavior is a **refusal naming the holder** — its run id, pid, start time and host — never a wait and never a steal. A command that blocks silently is indistinguishable from one that hung, and stealing is how two runs corrupt one tree.

`lockInfo` gains a hostname. Without it a lock cannot be validated on a shared or network filesystem, and PID liveness on a different machine is meaningless.

The lease covers the whole repository, matching the hazard: both runs share one working tree and one HEAD. Path-scoped leases would serve parallel writers, which do not exist.

A crash leaves the lock held; `pika recover` already exists for exactly this and is extended to cover the run lease alongside the transaction lock.

### 5.2 Release requires ownership

`releaseLock` verifies the lock it is about to remove is the one the caller holds — comparing the recorded id — and refuses otherwise. Today any process able to write the directory can release another's lock, which silently converts a correct exclusion into no exclusion at all.

### 5.3 Stop advertising a lease that does not exist

`acquire_scope`'s tool description claims an "exclusive write lease". An agent reading `tools/list` is entitled to believe that.

Two honest options; this milestone takes the first:

- **Back it with the real lease.** `acquire_scope` takes the run lease, records the holder, detects a conflicting acquire, and `release_scope` validates ownership. The description becomes true.
- Remove the claim and describe them as advisory board annotations.

The first is chosen because the mechanism now exists and the alternative leaves an MCP surface whose semantics an agent cannot rely on. A second `acquire_scope` for a conflicting scope returns `envelope_denied`'s sibling — a distinct, documented refusal code — rather than `granted:true`.

### 5.4 Close the option-injection surface

Git invocations that pass a `<branch>` or `<commit>` argument gain a `--` separator, so a value beginning with `-` is a value and not an option. No such value is agent-controlled today; this is closing a door before someone walks through it, and it is a one-token change per call site.

## 6. Error handling

- A held lease refuses with the holder's identity. No waiting, no timeout, no stealing.
- A stale lease — holder process dead, same host — is reported as stale and pointed at `pika recover`; it is still never stolen automatically.
- A lease whose recorded host differs from the current host is reported as unverifiable rather than assumed stale: PID liveness on another machine proves nothing.
- Release by a non-holder refuses and leaves the lock intact.
- `acquire_scope` on a conflicting scope refuses with a distinct code, never `granted:true`.

## 7. Testing

- Two concurrent runs: the second refuses, names the holder, and leaves the first's tree and record untouched. This is the milestone's headline and must be a real concurrency test, not a simulated one.
- The refusal must fire for both the default-branch collision and the distinct-`--branch` case, because today the second is the one that silently corrupts.
- Release by a non-holder refuses; release by the holder succeeds.
- A dead holder on the same host reports stale; a foreign host reports unverifiable.
- `acquire_scope` twice on the same path: the second refuses.
- `release_scope` without a prior acquire refuses.
- A branch or commit argument beginning with `-` is treated as a value.
- `pika recover` releases a crashed run lease.

## 8. Completion definition

M4 is complete when:

1. a second concurrent mutating run refuses, naming the holder, for both branch cases;
2. the run lease and the transaction lock share one implementation;
3. `lockInfo` records a hostname and a foreign host is reported as unverifiable rather than stale;
4. release verifies ownership and refuses otherwise;
5. `acquire_scope` detects a conflicting acquire and `release_scope` validates ownership, so their tool descriptions are true;
6. every git invocation passing a branch or commit argument uses `--`;
7. `pika recover` releases a crashed run lease;
8. `go test ./... -count=1` passes, `CGO_ENABLED=0 go build ./...` succeeds, `go.mod` still declares exactly two direct dependencies, and `pika check --ci` passes on this repository in GitHub Actions.
