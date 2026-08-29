# swift-single

This repository is scaffolded and verified by
[pika](https://github.com/Choaterboater/pika).

## Layout

- `.project/` — the pika contract (`contract.yaml`), the pinned
  profile packs (`profiles.lock`), and the naming exceptions record
  (`exceptions.yaml`).
- `docs/` — the documentation spine: `architecture/`, `decisions/`,
  `guides/`, `reference/`, and `work/`.
- `.github/workflows/ci.yml` — CI running `pika check --ci`.

## Verify

Run the verification ladder locally:

    pika check
