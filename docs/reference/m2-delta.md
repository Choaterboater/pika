# Milestone 2 delta

A record of what changed underneath users in M2, what is still true from
[M1.5](m1-5-delta.md), and the two gaps M2 deliberately did not close. It is
separate from the design spec's current-state audit table for the same reason
the M1.5 delta is: that table is the historical evidence the milestone was
built from and is not edited after the fact.

---

## 1. `profiles.lock` files written by a pre-M2 build are stale again

M2 edited two of the embedded profile packs. `go@1`'s format hint became
`gofmt -l .` carrying the new `fail-on-output` slot flag, and `python@1`'s
format hint became `ruff format --check .` while its test slot was corrected
from `python -m pytest` to `pytest` (Debian and Ubuntu ship `python3` only, so
every scaffold there failed its own test gate). The pack bytes are hashed, so
this **rotated `profiles.PackDigest()` again** — the digests of those two
packs and, because the top-level digest covers every registered pack, the
lock's registry digest along with them.

Consequence, identical in shape to M1.5: **every `.project/profiles.lock`
written by a pre-M2 pika fails gate 1** with a digest mismatch. The lock is
working; the packs really did change.

The remedy is `pika init --force`, and it is sharper than it looks. Read
[§1 of the usage guide](../guides/usage.md#--force-regenerates-more-than-the-lock)
before running it: `--force` rebuilds the contract from the profiles given on
*that* invocation, takes the project name from `--name` or the directory's
basename rather than reading it back, rewrites every other file init
manages — `README.md`, `AGENTS.md`, `CONTRIBUTING.md`, `.gitignore`, the PR
template, the CI workflow, the language scaffold — and resets
`.project/exceptions.yaml` to `{}`. Pass the same `--profile` and `--name` the
repository was scaffolded with, on a clean tree, and diff the result before
committing it.

There is still deliberately no in-place "repair the lock" command: a lock a
user can edit back to green is a lock that proves nothing.

> **Superseded by M3 — this paragraph describes `--force` as it behaved at M2.**
> Since M3 it regenerates only the kernel-owned files (contract, lock, PR
> template, CI workflow), reads profiles, project name and Go module back out
> of the repository, and never touches `.project/exceptions.yaml`. The
> destructive behavior described above is now the explicit `--reset-docs`
> opt-in. See [M3 §5](m3-delta.md#5-pika-init---force-is-safe-to-run). The
> text is kept as written because it is the record M3 was built from.

---

## 2. The format gate can now fail — and no longer rewrites your files

Before M2, `runGate` decided pass and fail purely on exit status, and `go@1`'s
format hint was `gofmt -l -w .`. That gate was **structurally incapable of
failing**: `-w` rewrote the very tree it was verifying and then exited 0.
`python@1`'s `ruff format .` had the same shape.

Packs now carry a `fail-on-output` flag, honored by `verify.runGate` after the
exit-status branch: a gate that exits 0 having printed anything fails anyway.
`go@1`'s hint is `gofmt -l .` with the flag; `python@1`'s is
`ruff format --check .`, which already exits nonzero and needs no flag.

> **Narrowed after 0.5.0 — this paragraph describes the flag as it behaved at
> M2.** At M2 the flag was read as a property of the *slot*, so it rode onto
> whatever command filled that slot, including one a repository already had.
> The first foreign repository pika adopted (`spf13/cobra`) got
> `format: make fmt` from its own Makefile and was reported
> `FAIL format exit=0` for a command that had succeeded: `make fmt` prints
> while exiting 0, as do `prettier --write`, `black .` and `cargo fmt`. The
> flag now reaches a gate only when the gate's argv is the argv the pack
> declared — `verify.FromProfiles` compares them — so it means what it always
> read as: "this is how to judge *this* command." `pika init` and `pika apply`
> write the pack's hint verbatim, so a scaffolded repository is unaffected;
> `pika adopt` writes the repository's own command, and that command is judged
> on its exit status. The text is kept as written because it is the record the
> narrowing was built from. See
> [../guides/usage.md](../guides/usage.md#the-gates-report-they-do-not-fix).

Two things change for a user:

- **Verification no longer edits your working tree.** Formatting drift is
  reported, not silently repaired. Run your formatter yourself.
- **A repository whose format gate used to pass may now fail.** That is the
  gate telling the truth for the first time, not a regression.

pika's own CI had a compensating `git diff --exit-code` step that existed only
because the format gate could not fail — and only worked because the gate
rewrote the tree. It is removed. The gate is what turns formatting drift red.

---

## 3. The kernel issues the evidence receipt

Before M2, a receipt under `.project/evidence/` could only be produced by an
agent calling MCP `publish_evidence` and supplying every field itself: the
commit, the changed files, the commands that ran, the completion verdict. The
attestation was written by the party it attested.

Now every run of `pika work`, `pika improve` and `pika resume` that reached an
agent ends with the kernel writing `.project/evidence/<work-id>.json` from the
finished run record — the contract and lock as they are on disk, the ladder
reports the run actually produced, and the commit read back out of Git rather
than taken from the run's own bookkeeping. Blocked runs get a receipt too: a
document that only ever describes successes attests the wrong half of what
pika does.

`publish_evidence` still exists for agents that have something else to attest.
It is no longer the only writer, and it is no longer the writer for a run pika
itself drove. It also cannot become that writer after the fact: publishing to
a path that already holds a receipt is refused with `invalid_params` and the
existing file is left untouched. A receipt issued by the component that ran
the gates is evidence; one supplied by the agent whose work it attests is a
claim, and a claim must not be able to overwrite the evidence.

---

## 4. A nested ladder is refused structurally, not by convention

`pika check`'s test gate runs the repository's own suite, and that suite can
invoke pika. In M1.5 that loop re-entered roughly every 13 seconds until the
machine held about twenty orphaned drivers.

`verify.Run` now refuses. Before any gate runs it compares the tree it was
asked to verify against the chain of enclosing ladders carried in
`PIKA_CHECK_LADDER`, and returns `ErrNestedRun` on a match:

```
pika check: verify: refusing to re-enter a running ladder: /path/to/repo
(enclosing ladders: /path/to/repo); a gate re-entered the ladder that
spawned it — pin the inner command to a different root
```

Exit 2, `code: "config"` under `--json`. Refusing rather than skipping is
deliberate: a skipped gate is `StatusSkip`, and `Pass` is `Summary.Fail == 0`,
so a silent skip would return a **green** report for a ladder that never ran.

The guard is scoped to the tree under verification, not to the process. A
ladder verifying a fixture in a temp directory is not the loop — it
terminates — so pika's own end-to-end suite can run `pika` inside temp
repositories while itself running under pika's ladder.

---

## 5. Runs are durable, and there are four commands for them

`pika work`, `pika status`, `pika resume` and `pika recover` are new; see
[§12–§15 of the usage guide](../guides/usage.md#12-hand-a-goal-to-the-agent-pika-work).
What underpins them:

- Every run writes a durable record at `.project/state/work/<work-id>/`,
  saved atomically at every phase transition, with the handoff bundle inside
  it. Corruption is reported, never repaired.
- Work ids come from `crypto/rand`. They were derived from the clock and the
  slug, so two runs of the same kind within one second produced the same id —
  and the second one's evidence receipt overwrote the first's.
- One lifecycle serves repair (`improve`) and feature (`work`) work. They
  differ in exactly one decision: a green baseline means repair work is
  already done, while it says nothing about whether a goal has been met.
- `.project/state/work/` is **local and gitignored**; `.project/evidence/` is
  **committed**. Operational state versus public attestation — the record
  holds unredacted agent transcripts and exists so a run can be resumed and
  diagnosed on the machine that ran it.

Two things outside the lifecycle moved with it.

**The scaffolded CI template was corrected.** It now pins the kernel to the
pika release that scaffolded the repository (`PIKA_REF`) instead of installing
`@latest`, so a green pull request does not go red on merge with nothing in
the diff to explain it. It also drops the `paths:` filter — a filter can only
name the directories that existed at scaffold time, so every directory the
repository grew afterwards was silently exempt from verification while CI
still reported success — checks out with `fetch-depth: 0` so `check --changed`
can resolve a merge base, and declares `permissions: contents: read`. See
gap 1 below for who does *not* get this fix.

**`pika doctor` cross-checks the envelope against the gates.** It loaded the
envelope and resolved gate argv in two functions that never spoke, so the
first notice that an envelope did not cover a gate was an `envelope_denied`
from MCP `run_checks`, mid-task. `doctor` now asks the envelope the same
question the enforcement path asks — the whole argv line — and warns per gate.
A warning, not an error: `pika check` consults no envelope, so no human path
is broken by an envelope that covers nothing.

---

## 6. Still true from M1.5: envelope enforcement did not move

M2 closed none of this. The table below is unchanged from
[the M1.5 delta](m1-5-delta.md#2-envelope-enforcement-now-covers-fs_write-and-exec),
restated here so nothing above is read as having widened it.

| Envelope class | Enforced? | Where |
|---|---|---|
| `fs_write` | **Yes** | `internal/mcp/server.go` — `preview_plan`, `acquire_scope`, `release_scope`, `publish_evidence`, `propose_decision`, `record_sources` |
| `exec` | **Yes** | `internal/mcp/server.go` — `run_checks` authorizes each gate's full argv before spawning it; `preview_plan` authorizes every discovered check command its baseline would run |
| `fs_read` | **No** | schema and matcher exist (`Envelope.allowsRead`); **no call site asks** |
| `network` | **No** | schema and matcher exist; **no call site asks** |
| `credential` | **No** | schema and matcher exist; **no call site asks** |
| `github` | **No** | schema and matcher exist; **no call site asks** |
| `budget` | **No** | `pika authorize` never writes a budget at all, because no code compares spend against a ceiling |

An envelope that grants `network` or `credential` still grants nothing in
practice. Do not read those entries as protection. The human CLI still needs
no envelope: only the MCP surface authorizes.

---

## 7. Known gaps, deliberately not closed

Both are real, both were understood when M2 shipped, and neither is scheduled
here. They are written down so nobody rediscovers them as a surprise.

### Gap 1 — a template-only pack change is invisible to every adopted repository

`profiles.PackDigest()` hashes the pack **YAML** — `packs/core@1.yaml`,
`packs/go@1.yaml` and the rest. `core@1`'s templates live in a *separate*
`embed.FS` (`internal/profiles/templates.go`, `//go:embed
packs/core@1/templates`) and are **not covered by any digest**.

The consequence chain, for the M2 CI-template correction specifically:

1. Correcting `ci.yml.tmpl` rotates no digest of its own.
2. So `CheckLock` gives an already-adopted repository **no signal**: gate 1
   passes with the old workflow on disk. (M2's lock rotation came from the
   pack YAML edits in §1, and it says nothing about the template.)
3. And `pika apply` only creates core files a repository is **missing** —
   existing files are skipped with `already exists; kept the existing file`,
   never overwritten.

So every repository scaffolded or adopted before that commit keeps its old
`.github/workflows/ci.yml`, which installs the kernel with `@latest`, and
nothing in pika will ever tell it. The only path that rewrites the file is
`pika init --force`, with all the caveats in §1 above; otherwise copy the
current template by hand.

Closing this properly means extending the digest to cover the template FS —
which rotates the digest once more, for everyone.

> **Closed in M3, both halves.** Pack templates are now inside `PackDigest()`,
> so a corrected template rotates the pack digest and gate 1 names the pack;
> and `pika apply` compares the two kernel-owned files against the rendered
> template and refreshes a stale one, reporting each rewrite. Point 3 above is
> therefore no longer true of the current binary. It cost the digest rotation
> this section predicted. See
> [M3 §6](m3-delta.md#6-the-template-blind-spot-is-closed-and-what-it-cost).

### Gap 2 — `pika recover --apply` cannot prove a holder dead on Windows

`txn.processAlive` is build-tagged. The Unix implementation signals the pid and
reads the result. The Windows implementation
(`internal/txn/process_alive_windows.go`) is:

```go
func processAlive(pid int) bool {
	return pid > 0
}
```

There is no portable stdlib-only liveness check on Windows, and the
conservative answer was chosen over a wrong one. The consequence is concrete:
on Windows **every** recovery lock reads as live, so `pika recover --apply`
refuses every wedged repository with `the holder process is still running`,
and the operator's only way out is to delete
`.project/state/recovery/lock` by hand and re-run.

`pika recover` without `--apply` still reports correctly on Windows — the
journal walk, the operation classification and the file listing are all
platform-independent. It is only the liveness verdict, and therefore the
authorization to roll back, that cannot be trusted there.
