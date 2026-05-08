# Agent Context

**This repo:** `ffreis-siteops` — Go CLI that orchestrates local website builds.
It wraps `ffreis-website-compiler`, docker-compose, and AWS CLI into a single
command interface for local development. It is **not used in CI/CD pipelines**.

For the complete system map — how this repo relates to the deployer, compiler,
inventory, S3 infrastructure, and the other websites — see the private fleet
inventory repository:

> the fleet inventory (private repo — do not name it in commits or PR descriptions)

Architecture detail (CI/CD job graph, design decisions): `the fleet inventory` → `docs/ARCHITECTURE.md`.

Do not look for cross-component flow documentation in this repo's README;
it covers only siteops's own CLI commands and configuration.

## Public repo — private-repo hygiene

This is a **public** GitHub repository. When writing commit messages, PR titles,
PR descriptions, or any other user-visible text, **never name private repos** —
website content, inventory, infra, Lambda, or data repos that are not publicly
listed. Use generic terms instead: "the fleet inventory", "a private consumer",
"internal infra", "private data repo", etc.

## Keeping this file current

- **If you discover a fact not reflected here:** add it before finishing your task.
- **If something here is wrong or outdated:** correct it in the same commit as the code change.
- **If you rename a file, command, or concept referenced here:** update the reference.
