# Milestone 1.5 delta

A short record of two things that changed underneath users in M1.5. It is
deliberately separate from the design spec's current-state audit table: that
table is the historical evidence this milestone was built from and is not
edited after the fact.

---

## 1. `profiles.lock` files written by an earlier build are now stale

M1.5 edited the embedded profile packs (the `go@1` pack gained `autofill`
markers and a `test` command; the `core@1` pack's rules and templates were
revised). The pack bytes are hashed, so this **rotated
`profiles.PackDigest()`** — both the per-pack digests and the lock's
top-level registry digest.

Consequence: **every `.project/profiles.lock` written by a pre-M1.5 pika
fails gate 1** with a digest mismatch. That is the lock working, not the lock
breaking — the packs genuinely changed.

Remedy:

```sh
pika init --force     # rewrites the managed files, including profiles.lock
pika check --all      # gate 1 goes green again
```

`--force` regenerates the managed files under `.project/` and never touches
your own files outside it. There is deliberately no in-place "repair the
lock" command: a lock a user can edit back to green is a lock that proves
nothing.

> **Superseded by M3.** `--force` never regenerated only what lives under
> `.project/` — it rewrote `README.md`, the language scaffold and the GitHub
> files too, and reset `.project/exceptions.yaml`. Since M3 it regenerates the
> contract, the lock, the PR template and the CI workflow, reads its inputs
> back out of the repository, and leaves everything else alone. See
> [M3 §5](m3-delta.md#5-pika-init---force-is-safe-to-run).

---

## 2. Envelope enforcement now covers `fs_write` **and** `exec`

The capability envelope schema has always described six classes. Only two of
them have an enforcement call site in the binary today.

| Envelope class | Enforced? | Where |
|---|---|---|
| `fs_write` | **Yes** | `internal/mcp/server.go` — `preview_plan`, `acquire_scope`, `release_scope`, `publish_evidence`, `propose_decision`, `record_sources` |
| `exec` | **Yes (new in M1.5)** | `internal/mcp/server.go` — `run_checks` authorizes each gate's full argv before spawning it, and `preview_plan` authorizes every discovered check command its baseline would run |
| `fs_read` | No | schema and matcher exist (`Envelope.allowsRead`); no call site asks |
| `network` | No | schema and matcher exist; no call site asks |
| `credential` | No | schema and matcher exist; no call site asks |
| `github` | No | schema and matcher exist; no call site asks |
| `budget` | No | `pika authorize` never writes a budget at all, because no code compares spend against a ceiling and an unenforced ceiling is a lie in a file whose whole job is to be true |

> **Superseded by M3 for `fs_read`**, which is now enforced at the MCP read
> tools — a requirement to hold an envelope, not a narrowing of read scope.
> `network`, `credential`, `github` and `budget` are still unenforced, and M3
> records the evidence that they cannot be enforced because the binary
> performs no operation of those classes. See
> [M3 §1 and §2](m3-delta.md#1-fs_read-is-enforced).

Two consequences worth stating plainly:

- An envelope that grants `network` or `credential` grants nothing in
  practice. Do not read those entries as protection.
- The **human CLI needs no envelope**. `pika check` runs its gates directly;
  only the MCP surface authorizes. This is intentional: the envelope exists to
  bound what an *agent* may do on your behalf, not to make a developer ask
  permission to run their own tests.
