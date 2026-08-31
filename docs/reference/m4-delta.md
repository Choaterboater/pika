# Milestone 4 delta — safe concurrency

A record of what M4 changed, and — at greater length, because it is the part
that will otherwise read as an omission — what it deliberately did **not**
build and the evidence for that decision. It sits alongside
[M1.5](m1-5-delta.md), [M2](m2-delta.md) and [M3](m3-delta.md) and does not
edit them.

M4 closes one hazard, and it is not exotic. One user with two terminals, in
one repository:

```sh
# terminal 1
pika work "add a /healthz endpoint"

# terminal 2, ninety seconds later
pika work "rename the config loader"
```

Before M4 both runs started. They share one working tree and one HEAD, so the
second run's agent edits land in the first run's `git add`, the second run's
branch checkout moves the tree the first one is verifying, and each one's
receipt attests a commit containing the other's work. Neither run knew the
other was there, and nothing in the product would have told either of them.

There are three ways into that hazard, not one. The second terminal may run
`pika work`; it may run `pika resume` or `pika handoff`, which drive the
lifecycle without going through `improve.Run`; or it may run `pika mcp`,
serving an agent harness that writes files directly and coordinates through
`acquire_scope`. All three end up in the same working tree, so all three are
held by the same exclusion — see [§5](#the-run-lease-is-the-scope-lease-on-).

---

## 1. One holder lock, and it is the one that already worked

`internal/lease` is the only `O_EXCL` exclusion in the binary. It was not
written for M4 — it was extracted from `internal/txn`, whose transaction lock
is the one exclusion in pika that has never corrupted a repository, and
`internal/txn` now uses the extracted package rather than its own copy.

The mechanism is a file created with `O_CREATE|O_EXCL`: the filesystem picks
the winner, so two processes racing for one name cannot both succeed, and the
loser learns who won by reading the file. The holder record is written and
fsynced — the file *and* its directory entry — before `Acquire` returns, and
the file is removed again if any of that fails, so the only lock that outlives
a crash is one that names a holder.

Two properties carried across unchanged, and they are the whole design:

- **A lease is never stolen automatically, on any path.** Not by a retry, not
  by a resume, not by a second run that has been waiting a long time. `Inspect`
  reports what can be proved and nothing more.
- **`StartedAt` and `Host` are recorded**, because a person deciding whether to
  clear a lock needs to know how long it has been there and on which machine.

One property is new: `Release` refuses to remove a file that no longer names
the acquisition it was given. A lease that has stopped being yours is not
yours to delete.

## 2. The run lease is whole-repository, and that is not caution

`pika work`, `pika improve`, `pika resume` and `pika handoff` hold an exclusive
lease at `.project/state/run.lock` for the whole run, released only once a
terminal outcome is recorded. A second run refuses:

```
pika work: improve: another run holds this repository: run 20260830-feature-b2792c9f
(pid 8900 on build-01, started 2026-08-30T23:08:14Z) is in progress; one repository runs
one run at a time, because both would commit through the same working tree
```

Path-scoped run leases would be the more sophisticated answer, and they would
be answering a question nobody asked. The hazard is whole-repository because
the shared resources are: one working tree, one HEAD, one index. Scoping the
lease to `src/api` would not stop the other run's `git checkout` from moving
the tree underneath it. Path scoping serves parallel writers, and pika has
none — see §6.

The refusal names the holder, in three different ways for three different next
moves, because a single "already running" would tell an operator none of it:

| State | What the refusal says | What the operator does |
|---|---|---|
| held | the run, its pid, its host and its start time | wait for it, or go and stop it |
| stale | the holder's process is gone and its host is this one | `pika recover` |
| unverifiable | the holder is on another host, so this machine cannot judge it | check that machine first |

The lease is never waited on. A run that blocked silently behind another one
would be indistinguishable, from the operator's terminal, from a run that
hung.

## 3. `StateUnverifiable` is never swept, and never called stale

A pid only means something on the machine that recorded it. On a shared or
network filesystem, a pid that is dead here can be very much alive on the host
that took the lease. So a holder recorded on another host is reported
`unverifiable` — not `stale` — everywhere: in `lease.Inspect`, in the run
refusal, in `pika recover`'s report, and in `lease.Clear`, which refuses it.

This is the single most load-bearing decision in the milestone, and it is
load-bearing precisely because getting it wrong is silent. "Stale" is the word
that makes an operator clear a lock. Applying it to a holder this machine
cannot judge is how two writers end up in one tree — with a recovery command's
blessing, which is worse than no recovery command at all.

## 4. `pika recover` covers all three locks

`pika recover` already reported and rolled back a transaction that never
finished. It now also reports the run lease and every MCP scope lease under
`.project/state/locks/`, and `--apply` clears the ones whose holders are
provably gone.

Without this, M4 would have shipped a new dead end. A killed `pika work` leaves
a lease `pika resume` refuses — correctly, since a record saying "interrupted"
is bit for bit what a run still working leaves behind — and the only way out
would have been `rm .project/state/run.lock`. That is exactly the state M2
found `pika apply` in, when `txn.Recover` had no production caller at all and a
crashed apply could only be cleared by hand. Reintroducing it one layer up
would have been a regression dressed as a feature.

Three rules hold on the `--apply` path:

- Only `StateStale` is swept.
- `StateHeld` is refused, exit 2, naming the holder. Nothing is attempted: a
  live run in the tree is a reason not to start rolling anything back either.
- `StateUnverifiable` is refused, exit 2, and is never described as stale.

`cmd/pika` does not spell `.project/state/run.lock` or `.project/state/locks`.
`internal/repolease` owns both, and the reason is that a second spelling drifts
*silently*: a recover looking in the wrong place reports a repository that is
already clean.

Clearing a lease does not discard the run. The record is untouched,
`pika status` still lists it, and `pika resume <work-id>` finishes it.

## 5. Scope leases became real, and they are the same exclusion as the run

`acquire_scope`'s description has always promised exclusivity. Until M4 it
recorded a row on the board and returned success, so two MCP sessions could
both be granted the same path. It is now backed by the same lease, under
`.project/state/locks/<percent-encoded-path>.lock`, with the stable
`scope_conflict` code — which `README.md` and the usage guide now list.

Exclusive over a path means exclusive over its whole subtree: a lease on `src`
conflicts with one on `src/pkg` in both directions. An exclusion an agent could
sidestep by naming a subdirectory would not be one.

### The run lease is the scope lease on `.`

The first version of this shipped two exclusions that never looked at each
other. `improve.TakeRunLease` did not read `.project/state/locks/`;
`acquire_scope` did not read `.project/state/run.lock`. So:

```sh
# terminal 1
pika work "add a /healthz endpoint"

# terminal 2 — an agent harness that writes files directly
pika mcp        # → acquire_scope src  →  granted:true
```

Both proceeded. Two writers, one working tree — the hazard this milestone
exists to close, reached through a third door.

`internal/repolease` closes it, and it does so with **one rule rather than a
special case**. A run lease is a claim over the whole repository; a scope lease
is a claim over a subtree. They are the same kind of claim at different radii,
so the run lease's scope is spelled `.` — the repository root — and the overlap
rule that already made `src` conflict with `src/pkg` decides every pair. `.`
contains everything, therefore:

| Held | Asked for | Answer |
|---|---|---|
| run lease | any `acquire_scope` | `scope_conflict`, naming the run, its pid, its host and its start time |
| any scope lease | a run (`work`, `improve`, `resume`, `handoff`) | refused, naming the scope and the session holding it |
| `src` | `src`, `src/pkg`, `.` | `scope_conflict`, as before |
| `src` | `docs` | granted: they cannot collide |

The two keep separate files because a refusal has to name the holder in the
operator's own terms — "the run holding this repository" and "the scope lease
on `docs/guides`" are different sentences with different next moves, and
`pika recover` reports them as different things.

**Why not subsumption.** The tempting alternative is to let a run lease subsume
scopes: the run already holds the tree, so let its agent take scopes inside it
freely. The mechanism cannot honestly implement that. Nothing links the process
asking for a scope to the process holding the run — they are different
commands, usually different processes, and a lease records a pid and a host,
not a lineage. "The run holder may have this scope" would in practice read
"anybody may have this scope while a run is in the tree", which is the hazard
with a permission slip stapled to it. There is no re-entrancy anywhere in this
design: `acquire_scope` already refuses the holding session a lease it is
already holding.

**Nothing waits and nothing is stolen**, on this path either. Every acquisition
claims its own file with `O_EXCL` first and only then looks for conflicts, so
two acquisitions racing for overlapping ground both see the other's file, both
refuse, and both give their claim back. There is no retry loop in any caller,
so no deadlock is structurally possible. A refusal that could have been a grant
is fixed by asking again; two writers in one tree is fixed by nothing.

A run refused by a scope lease wraps `improve.ErrScopeLeaseHeld`, not
`ErrRunInProgress`. "Another run holds this repository" would send an operator
to `pika status`, where they would find no run and conclude the message was
lying — what is actually in the tree is an agent harness driving `pika mcp`.

---

## 6. What was deliberately not built, and why

Spec §9.3 specifies a SQLite coordination board in WAL mode, and §§9.1–9.4
specify the multi-agent machinery it would coordinate: named agents addressing
one another, typed messages and questions, a task graph, parallel writers with
declared disjoint scopes. M4 built none of it. That is a decision, not an
oversight, and here is the evidence it was made from.

### 6.1 The board has nine record types and no reader at all

§9.3 lists nine things the board stores: work runs and lifecycle stage; task
graph and dependencies; agent identity, role, runtime, model and status;
write-scope leases; typed messages and questions; decisions and acceptance
status; artifact references; verification gates and receipts; recovery
checkpoints.

What exists is `.project/state/board.jsonl`, appended by three MCP tools:
`propose_decision` writes `decision`, `record_sources` writes `sources`, and
`acquire_scope`/`release_scope` write `scope_lease`. **Nothing in the binary
reads it.** There is no reader for `board.jsonl` anywhere in the repository —
it is a write-only log, and has been since M1.

Of the nine record types, exactly one has a real consumer in the product:
write-scope leases. M4 gave that one the consumer it needed, and it is a lock
file that actually excludes, not a row in a table. The other eight are served
by things that already exist and are read:

| §9.3 record type | Where it actually lives | Read by |
|---|---|---|
| work runs and lifecycle stage | `.project/state/work/<id>/record.json` | `pika status`, `pika resume` |
| write-scope leases | `.project/state/locks/`, `.project/state/run.lock` | `acquire_scope`, every run, `pika recover` |
| verification gates and receipts | `verify.Report`, `.project/evidence/<id>.json` | `pika check`, the run lifecycle |
| recovery checkpoints | `.project/state/recovery/` journals | `pika recover` |
| decisions, artifact references | `board.jsonl` | nothing |
| task graph, agent identity, messages | nowhere | nothing |

Migrating a write-only log to SQLite does not make it read. It makes it a
write-only log with a schema, a driver, and a WAL file.

### 6.2 Nothing in the repository asks for a second agent

The three record types with no storage at all — task graph, agent identity,
typed messages — exist to coordinate concurrent agents. pika spawns exactly
one: `improve.Run` calls its `Runner` once per run, `configuredCodexRunner`
resolves one contract agent (`builder` by default), and `codexRuntime` is the
only runtime any command will spawn. The run lease this milestone added
guarantees there is at most one of those in a repository at a time.

A messaging table for agents that cannot exist, keyed by an agent identity
nothing mints, is not infrastructure for a future feature. It is a schema
somebody will later have to reverse-engineer intent from.

### 6.3 The dependency floor is a real constraint, not a preference

`go.mod` declares two direct dependencies. `CGO_ENABLED=0 go build ./...` must
succeed, and the binary ships CGO-free for macOS, Linux and Windows. The only
pure-Go SQLite driver that satisfies that is `modernc.org/sqlite`, which brings
a transitive tree of its own — for a database with no reader (§6.1),
coordinating agents that do not exist (§6.2).

The exclusion M4 actually needed is one file created with `O_EXCL`. It is
already in the binary, it is the one mechanism here that has never corrupted a
repository, and it needs no driver, no schema migration and no WAL.

### 6.4 What would change this

This is a reasoned deferral with a stated trigger, not a permanent no. Build
the board when a second agent exists to coordinate — when something in the
product spawns two runners, mints agent identities, or reads a decision back
out of `board.jsonl`. At that point the board has a consumer and the argument
above stops holding. Until then, the honest shape of pika's coordination is:
one run, one agent, one lock file.

---

## 7. What an existing repository notices

- **A second concurrent run now refuses** instead of silently corrupting the
  first. If any automation ran two `pika work` invocations in one checkout, it
  will now see exit 1 and a message naming the holder.
- **A killed run leaves `.project/state/run.lock` behind**, and `pika resume`
  refuses until `pika recover --apply` clears it. This is one extra deliberate
  command after a crash, and it is the cost of never stealing a lease.
- **`acquire_scope` can now be refused** with `scope_conflict` where it
  previously always succeeded. That refusal was always in its description.
- **`acquire_scope` is refused for every path while a run is in progress**, and
  a run is refused while any scope lease is held. An agent harness driving
  `pika mcp` beside a `pika work` used to be granted both; it now gets a
  `scope_conflict` naming the run. Automation that interleaved the two in one
  checkout has to serialize them — which is the point: they were sharing a
  working tree.
- **`pika doctor` reports the leases.** A repository locked out by a crashed
  run or a crashed MCP session used to read clean; it now carries a
  `lease.run` or `lease.scope.<path>` finding. A stale lease is an error and
  fails `doctor`; a live holder is a warning and does not.
- **No pack digest rotated.** M4 changed no pack YAML and no pack template, so
  no `.project/profiles.lock` needs regenerating for this milestone.
- **No new dependency.** `go.mod` still declares the same two direct
  dependencies.

## 8. Known gaps, deliberately left open

- **Windows cannot prove a holder dead.** `processAlive` reports every positive
  pid as alive, so no lease is ever `StateStale` there and `pika recover
  --apply` refuses every one of them. The report is unaffected — it names the
  holder, its host and its start time — but the operator removes the file by
  hand. This is the same gap M2 recorded for the transaction lock, now applying
  to the run and scope leases as well:
  [m2-delta.md](m2-delta.md#gap-2--pika-recover---apply-cannot-prove-a-holder-dead-on-windows).
- **A run refused for a held lease exits 1, not 2.** The command layer
  documents exit 2 for "a repository state the run refuses to start in", and
  nothing was attempted when a lease refusal fires, so 2 is the right code.
  `ErrDirtyTree` has had the same mismatch since M2 and both should move
  together, with `pika work`, `pika improve` and `pika resume` mapping them
  alongside the three refusals `resume` already maps. The refusal message is
  unaffected either way.
- **`pika doctor` never says which run is which.** The `lease.run` finding
  names the work id, pid, host and start time of the holder, but not what that
  run is doing — for that the operator runs `pika status`. Teaching `doctor` to
  read run records would make it a second, partial `status`.
- **Pid reuse is not defended against.** A stale lease whose pid has been
  recycled by an unrelated process reads as held, so recovery refuses it and
  the operator waits or removes it by hand. The failure is a false refusal,
  never a false sweep, which is the direction this mechanism errs in
  everywhere.
