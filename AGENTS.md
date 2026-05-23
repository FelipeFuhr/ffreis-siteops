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

## `dev` command — run the develop env locally

The `dev` subcommand (registered in `internal/cli/dev.go`, orchestrated in
`internal/runner/dev.go`) spawns `website-compiler serve` and fronts it with a
reverse proxy (`internal/runner/proxy.go`) that forwards configured paths to a
remote API Gateway, rewriting the `Origin` header so the dev Lambdas'
`CORS_ALLOW_ORIGIN` check passes for browsers on localhost.

Required config fields:

| Field | Description |
|---|---|
| `data_root` | Root of the data repo; must contain `<lang>/site.yaml`, `<lang>/site.d/*.yaml`, and `shared/site.d/*.yaml`. The dev command stages a merged copy into `<website_root>/src/data/` before serving. |
| `default_lang` | Language injected when `--lang` is not passed (e.g. `pt`, `en`, `jp`). |
| `preview_port` | Optional. User-facing port; default 8088. If taken, the next free port in [start, start+50) is used. |
| `api.gateway_url` | Optional. Dev API Gateway invoke URL. When unset, dev mode is frontend-only. |
| `api.dev_origin` | Origin value to inject on proxied API requests (e.g. `https://flemming.ffreis.com`). |
| `api.proxy_paths` | Path patterns routed to the API Gateway. Trailing `/*` is supported (e.g. `/api/*`). |

Data injection mirrors the manual local-deploy procedure: per-language
`site.yaml` and `site.d/*.yaml` are copied, then `shared/site.d/*.yaml` is
merged on top. The compiler does not recurse into `site.d/` when pointed at a
top-level data dir, so we materialize the merge ahead of serving.

Discovering API Gateway URLs:

```bash
AWS_PROFILE=ffreis-platform aws apigatewayv2 get-apis --region us-east-1 \
  --query 'Items[?contains(Name, `-api-dev`)].{Name:Name,Endpoint:ApiEndpoint}' --output table
```

The dev Lambdas already accept requests with `Origin: https://<dev-domain>`
(set by terraform as `CORS_ALLOW_ORIGIN`), so the proxy's Origin rewrite means
no infra change is needed for local dev to call real dev Bedrock / KB.

Caveats:
- `website-compiler serve` renders templates without the compiler's build-time
  transforms (CSS inlining, fingerprinting, image processing, LQIP). Use for
  iteration; deploy to dev for prod-fidelity verification.
- `serve` only registers `/` and `/<page>.html` page routes. Clean-URL links
  (`/courses` rather than `/courses.html`) emitted by some templates may 404 in
  dev mode but work correctly once deployed.

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
