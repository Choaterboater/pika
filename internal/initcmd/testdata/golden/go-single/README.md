# go-single

This repository is scaffolded and verified by
[projectctl](https://github.com/Choaterboater/projectctl).

## Layout

- `.project/` — the projectctl contract (`contract.yaml`), the pinned
  profile packs (`profiles.lock`), and the naming exceptions record
  (`exceptions.yaml`).
- `docs/` — the documentation spine: `architecture/`, `decisions/`,
  `guides/`, `reference/`, and `work/`.
- `.github/workflows/ci.yml` — CI running `projectctl check --ci`.

## Verify

Run the verification ladder locally:

    projectctl check
