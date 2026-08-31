# Milestone 5 delta — the agent skill layer

A record of what M5 changed underneath users, written a milestone late and
published with M6. It sits alongside [M1.5](m1-5-delta.md), [M2](m2-delta.md),
[M3](m3-delta.md) and [M4](m4-delta.md) and edits none of them.

M5 shipped on `main` without one. That is the debt this file pays, and the
reason it is written from the code as it stands rather than from the commit
that landed it: a delta written late is only useful if it is true of the
binary you are running.

Four milestones built a kernel an agent could drive safely. None built the
thing that tells an agent *how*. M5 built it, and it is the first milestone
whose output is mostly files an operator is expected to read and edit.

---

## 1. `.agents/skills/` is now part of a scaffolded repository

`pika init` writes four canonical skills:

```
.agents/skills/project-work/SKILL.md       running, repairing and resuming work
.agents/skills/project-research/SKILL.md   reading the contract, the lock, the exceptions record
.agents/skills/project-review/SKILL.md     what counts as evidence, and what a reviewer must not do
.agents/skills/project-maintain/SKILL.md   drift, digests, doctor, and the upgrade path
```

`pika apply` writes them through its create-if-missing path, so an adopted
repository gets them without asking. They are **operator-owned once written**:
`pika skills install` and `pika init --force` never overwrite one you have
edited. `--reset-docs` is the opt-in that restores the shipped templates, and
it is the same flag that resets `README.md` and the rest.

The content is derived from behaviour that exists. Every claim in a skill is
meant to be true of the binary at the commit that ships it. A false
instruction to an agent is worse than a false instruction to a human, because
the agent cannot notice.

The one mechanical guard is narrow and real: a test walks the command registry
against every `pika <command>` mentioned in the canonical skill templates, so
a skill cannot tell an agent to run a command pika does not have. That pass
shipped with M6, not M5 — until then the template text was outside the
guard's reach, because the guard walks `.go` files. Flags and error codes
named in a skill are still verified by reading, not by test.

## 2. Projections, generated and digest-gated

A harness that will not read `.agents/skills/` gets a **projection**: the same
guidance rendered where it does read. Which harnesses get one is declared in
the contract, not compiled into the kernel.

```yaml
skills:
  projections:
  - harness: codex
    path: AGENTS.md
  - harness: claude
    path: CLAUDE.md
```

A projection is a **region** between `pika:skills:begin` and `pika:skills:end`
markers, not a whole file. Everything outside the markers is yours and
survives regeneration; everything inside is the kernel's.

Gate 1 recomputes two digests and the two failures have **opposite remedies**,
which is the part worth reading before you act on one:

- **Stale.** The `pika:source` lines name a source whose bytes have moved on.
  Regenerating is the whole fix and nothing you wrote is at risk:
  `pika skills install`.
- **Tampered.** The region's own recorded digest no longer matches its bytes —
  somebody edited inside the markers. Regenerating **discards that edit**. Move
  the text outside the markers first, then regenerate.

```
skills projection: stale AGENTS.md (harness codex) cites skill
.agents/skills/project-work/SKILL.md at sha256:4018…, which is now
sha256:9c2f…; regenerate it with `pika skills install`
```

A gate that refused to tell these two apart would invite an operator to
destroy their own edit with the remedy for a different problem.

## 3. `pika skills`, and the boundary `--global` crosses

```sh
pika skills             # report: what is installed, what is projected, what is stale or tampered
pika skills install     # write the canonical skills, regenerate every declared projection
pika skills check       # exit 1 if any projection is stale, tampered or unreadable

pika skills --global            # the same three modes, against the agent files in
pika skills install --global    # your home directory instead of this repository's
pika skills check --global      # projections
```

The `--global` surface is deliberately separate, and the separation is the
point. A repository's contract cannot ask for a global install at any
spelling: cloning a repository must never grant it a capability over the
machine that cloned it. The files in your home directory are reachable only
through an explicit `pika skills install --global` on a command line.

`pika doctor` reports the global files, and gate 1 deliberately does not:
those files are absent from a fresh checkout by definition, so a gate that
digested them would fail on every clone of every repository. A stale or
tampered global file is a warning, never an error — the repository is not
broken, and failing the ladder over a file outside it would make the exit code
answer a question nobody asked.

## 4. `agent-guidance` is finally consumed

`Pack.AgentGuidance` was declared on every pack and empty in all six since
M1. M5 surfaces it on `profiles.Resolved` and composes it into the generated
skills, so a Go repository's skill can carry Go-specific guidance and a
TypeScript one different guidance, without forking the skill.

The consequence is the one to know about: **editing any pack rotates its
digest**, and a corrected skill template propagates the same way a corrected
CI workflow does. An adopted repository learns its skills are stale rather
than silently keeping old ones.

## 5. What an existing repository notices

- **A repository scaffolded before M5 has no `.agents/skills/`.** Nothing
  fails: the skills are created-if-missing content, not gate-enforced
  content, so `pika check` stays green. Run `pika skills install` to get them.
- **An adopted repository gains four files** on its next `pika apply`, and
  whatever projection its contract declares is regenerated to cite them.
- **A repository that already had an `AGENTS.md` keeps everything outside the
  markers.** The kernel-owned region is added, not the file replaced.
- **`pika skills check` is a new thing to run in CI.** It exits nonzero on a
  stale or tampered projection, which is how a projection stops rotting.
- **No `.project/profiles.lock` was rotated by M5 itself**, but a pack's
  `agent-guidance` is now load-bearing: adding guidance to a pack changes its
  bytes, and therefore its digest, and therefore the lock.

## 6. Known gaps, deliberately left open

- **The skills are English prose read by a model.** Nothing verifies that an
  agent understood one, and nothing can. What is verified is narrower and
  real: that every command, flag and error code named in a skill exists in
  this binary.
- **A projection is verified by digest, not by content.** Gate 1 can tell you
  the copy moved from its source; it cannot tell you the source is wrong.
  That judgement stays with whoever edits the canonical skill.
- **A global install is still invisible to `pika check`.** This is deliberate
  (§3), and it means a machine whose home-directory instructions have drifted
  shows nothing at all until `pika doctor` is run.
- **Four skills, five roles.** Design §9.1 names `lead`, `explorer`,
  `researcher`, `builder` and `reviewer`. M5 wrote skills for the work,
  research, review and maintenance activities, not one per role, because a
  skill per role would have been four files saying the same three things.
  M6 makes `explorer` and `reviewer` real roles in the lifecycle; `lead` and
  `researcher` are still not.
- **No registry, marketplace or remote fetch.** Skills ship with the packs or
  they do not exist.
