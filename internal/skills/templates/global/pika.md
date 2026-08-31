---
name: pika
description: Use when working in a repository governed by pika (it has .project/contract.yaml), or when putting pika onto a repository that is not governed yet — covers init, adopt and apply, and hands over to that repository's own skills once a contract exists.
---

# First: is this repository governed?

This text is installed in your home directory, so it is read in repositories
pika governs and in repositories pika has never seen. Those two need different
first moves, and everything after this section assumes a contract that an
ungoverned repository does not have.

A repository is governed when `.project/contract.yaml` exists.

**It exists.** Read `.agents/skills/` in that repository before acting. Those
skills are generated from that repository's own contract and profile packs, so
they carry stack guidance this copy cannot, and where the two disagree the
repository's own skills win. Then use the rest of this document.

**It does not exist.** Nothing below works yet: every command there reads a
contract that is not there.

| The directory | The move |
|---|---|
| Empty, a new project | `pika init --profile <lang> --name <name>` |
| An existing repository with real code | `pika adopt`, read the review, then `pika apply` |

`pika adopt` never writes a live contract. It writes
`.project/contract.yaml.draft` and a human-readable `review/adoption-review.md`;
`pika apply` promotes that draft transactionally, and promotion is the step that
makes the repository governed. Read the review before applying — being readable
before it takes effect is the entire reason adoption has two steps.

Neither command is one to run on somebody's repository uninvited. Adoption
writes files at the root and changes how work is done there. Ask first.

## About this file

It is generated. `pika skills install --global` writes it, and everything
between the `pika:skills:begin` and `pika:skills:end` markers is kernel-owned:
pika digests that region, so a hand edit is detectable and a regenerate discards
it. Report it with `pika skills --global`, and change the source rather than this
copy.

No repository can cause this file to be written. Installing it is an explicit
`--global` on a command line, never something a contract, a gate, or a run can
ask for — otherwise cloning a repository would hand it your home directory.
