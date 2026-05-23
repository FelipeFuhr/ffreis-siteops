# siteops (ffreis-siteops)

Go CLI that orchestrates website builds through a configurable compiler CLI.

It uses YAML config and is designed so project-specific data can stay local and ignored.

## Config

Committed template:
- `config/config.example.yaml`

Create local config (ignored by git):

```bash
cp config/config.example.yaml config/site.local.yaml
```

Then edit `config/site.local.yaml` with your real paths, for example:

```yaml
compiler_command: "website-compiler"
website_root: "../my-website"
out_dir: "../dist/my-website"
site_data_source: ""
sitemap_base_url: "https://www.example.com"
mirror_external_assets: true
compose_env:
  WORKSPACE_ROOT: "."
  WEBSITE_ROOT: "my-website"
  OUT_DIR: "dist/my-website"
  COMPILER_WATCH_PATH: "."
  PREFIX: "myorg"
  IMAGE_PROVIDER: ""
  IMAGE_TAG: "local"
  COMPILER_IMAGE_NAME: "website-compiler-cli"
  COMPILER_WATCH_IMAGE_NAME: "website-compiler-watch"
  COMPILER_WATCH_IMAGE: "myorg/website-compiler-watch:local"
  WEBSITE_COMPILER_IMAGE: "myorg/website-compiler-cli:local"
```

`config/*.local.yaml` is ignored by git, so project-specific paths are not committed by accident.

## Usage

```bash
make build
make serve
make validate-site-data
make validate-assets
make compose-up
make publish
make dev CONFIG=config/flemming-dev.local.yaml LANG=pt
```

### `dev` — run the develop env locally end-to-end

`make dev CONFIG=<site>-dev.local.yaml [LANG=pt]` (or `siteops dev`) does in one
command what previously took six manual steps:

1. Stages site data: copies `<data_root>/<lang>/site.yaml` and merges
   `<data_root>/<lang>/site.d/` + `<data_root>/shared/site.d/` into
   `<website_root>/src/data/`.
2. Spawns `website-compiler serve` on an internal port.
3. Listens on `preview_port` (default 8088) and reverse-proxies:
   - `/ask` and any `api.proxy_paths` patterns → `api.gateway_url` (the dev API
     Gateway), with the request `Origin` rewritten to `api.dev_origin` so the
     dev Lambda's `CORS_ALLOW_ORIGIN` check passes;
   - everything else → the compiler.

Use `AWS_PROFILE=ffreis-platform` if you need AWS CLI access alongside; the
proxy itself only needs HTTPS reachability to the API Gateway, since the dev
Lambdas hold their own dev Bedrock + KB bucket permissions server-side.

When the `api` block is omitted, dev mode runs frontend-only with no API
proxying — useful for content-only iteration.

CLI:
- `siteops` is the command.

Specify another config:

```bash
make build CONFIG=config/another-site.local.yaml
```

## Logging

Siteops uses structured logging via Go `slog`.

Environment variables:
- `LOG_LEVEL`: `debug|info|warn|error` (default: `info`)
- `LOG_FORMAT`: `text|json` (default: `text`)
- `LOG_SOURCE`: `true|false` (default: `false`)
- `SITEOPS_COMMAND_TIMEOUT`: duration timeout per compiler/compose command (default: `15m`, `0` disables timeout)
- `SITEOPS_SHUTDOWN_GRACE`: grace period before force kill on cancel/timeout (default: `10s`)

Examples:

```bash
LOG_LEVEL=debug LOG_FORMAT=text make build
```

```bash
LOG_LEVEL=info LOG_FORMAT=json make compose-up
```

## Notes

- `container_command` in YAML is forwarded as `CONTAINER_COMMAND` to compiler wrappers.
- `compose_command`, `compose_file`, and `compose_env` in YAML define how compose is called (podman-first, docker fallback).
- `publish` uses docker compose services `publisher` and `invalidator`:
  - `publisher` runs `platform/ffreis-website-packer` to sync `out_dir` to the configured S3 bucket (bucket-per-domain recommended).
  - `invalidator` optionally runs CloudFront invalidation when `cloudfront_distribution_id` is set.
- `site_data_source` optionally overrides `src/data/site.yaml`. When both are present, the override wins and the compiler logs a warning.
- Every website must keep its own `src/data/site.contract.yaml` committed locally. The contract is authoritative for that site and is not overrideable through siteops config.
- `validate-site-data` lets you validate a local or external site-data payload against the site contract without doing a full build. This is useful for a separate repo that only manages updateable data.
- `validate-assets` lets you validate that local `.css` and `.js` files are actually reachable from the rendered pages through HTML references, CSS `@import`, and JS module imports.
- Contract usage validation follows `.SiteData` access through the compiler's generic `dig` helper, so websites should read site data with `dig` instead of direct `index` lookups when they want stale-contract detection.
- `sitemap_base_url` lets the compiler auto-generate `sitemap.xml` from template pages when no `sitemap.yaml` exists in the website root.
- `mirror_external_assets: true` tells the compiler to vendor external stylesheet/script/image/font URLs into `dist/` so the built site remains self-contained even if source templates or CSS reference third-party assets.
- Image variables follow the same model as `ffreis-python-onnx-model-converter`:
  - `PREFIX`
  - `IMAGE_PROVIDER`
  - `IMAGE_TAG`
  - `COMPILER_IMAGE_NAME`
  - `COMPILER_WATCH_IMAGE_NAME`
  - Derived `IMAGE_ROOT = (IMAGE_PROVIDER ? IMAGE_PROVIDER + "/" : "") + PREFIX`
- Compiler image for the compiler-watch build is resolved as:
  - `${WEBSITE_COMPILER_IMAGE}` if set
  - otherwise `${IMAGE_ROOT}/website-compiler-cli:${IMAGE_TAG}`
- `docker-compose.yml` uses:
  - `${COMPILER_WATCH_IMAGE}` if set
  - otherwise `${IMAGE_ROOT}/website-compiler-watch:${IMAGE_TAG}`
  - `${PREVIEW_IMAGE}` (default `nginx:alpine`)
