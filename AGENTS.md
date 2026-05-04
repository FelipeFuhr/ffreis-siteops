# Agent Context

**This repo:** `ffreis-siteops` — Go CLI that orchestrates local website builds.
It wraps `ffreis-website-compiler`, docker-compose, and AWS CLI into a single
command interface for local development. It is **not used in CI/CD pipelines**.

For the complete system map — how this repo relates to the deployer, compiler,
inventory, S3 infrastructure, and the other websites — see the private fleet
inventory repository:

> `FelipeFuhr/ffreis-website-inventory` → `AGENTS.md`

Architecture detail (CI/CD job graph, design decisions): `AGENTS.md` links to
`docs/ARCHITECTURE.md` in the same repo.

Do not look for cross-component flow documentation in this repo's README;
it covers only siteops's own CLI commands and configuration.

## Keeping this file current

- **If you discover a fact not reflected here:** add it before finishing your task.
- **If something here is wrong or outdated:** correct it in the same commit as the code change.
- **If you rename a file, command, or concept referenced here:** update the reference.
