# Agent Context

**This repo:** `ffreis-siteops` — Go CLI that orchestrates local website builds.
It wraps `ffreis-website-compiler`, docker-compose, and AWS CLI into a single
command interface for local development. It is **not used in CI/CD pipelines**.

For the complete system map — how this repo relates to the deployer, compiler,
inventory, S3 infrastructure, and the other websites — see the private fleet
inventory repository:

> the fleet inventory (private repo — do not name it in commits or PR descriptions)

Architecture detail (CI/CD job graph, design decisions): `FelipeFuhr/ffreis-website-inventory` → `docs/ARCHITECTURE.md`.

Do not look for cross-component flow documentation in this repo's README;
it covers only siteops's own CLI commands and configuration.

## Public repo — private-repo hygiene

This is a **public** GitHub repository. When writing commit messages, PR titles,
PR descriptions, or any other user-visible text, **never name private repos** —
website content, inventory, infra, Lambda, or data repos that are not publicly
listed. Use generic terms instead: "the fleet inventory", "a private consumer",
"internal infra", "private data repo", etc.

## Local config optional embedding fields

The following fields can be added to any `<site>.local.yaml` to control how the
compiler embeds resources into HTML:

| Field | Type | Description |
|---|---|---|
| `js_inline_threshold` | int | Override compiler's `-js-inline-threshold` (default 8192 = 8 KB). 0 = disable. |
| `js_shared_inline_threshold` | int | Override compiler's `-js-shared-inline-threshold`. Scripts on >1 page use this lower limit instead; set to 0 to always cache shared scripts. nil = disabled (all JS uses `js_inline_threshold`). |
| `raster_inline_threshold` | int | Override compiler's `-raster-inline-threshold` (default 0 = disabled). Use `2147483647` to embed all raster images. |
| `embed_fonts` | bool | Pass `-embed-fonts` to embed font files as base64 data URIs in inlined CSS. |
| `inline_body_css` | bool | Pass `-inline-body-css` to inline deferred body CSS instead of the deferred external pattern. |

All new fields are optional; omitting them uses the compiler's built-in defaults.
Flags are passed for both `build` and `build-inline` commands, but `-inline-assets`
already handles all assets as data URIs so the threshold flags are redundant there.
## Keeping this file current

- **If you discover a fact not reflected here:** add it before finishing your task.
- **If something here is wrong or outdated:** correct it in the same commit as the code change.
- **If you rename a file, command, or concept referenced here:** update the reference.
