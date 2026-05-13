#!/usr/bin/env bash
set -euo pipefail

bucket="${PUBLISH_BUCKET:-}"
prefix="${PUBLISH_PREFIX:-/}"
dir="${PUBLISH_DIR:-}"
no_delete="${PUBLISH_NO_DELETE:-false}"
region="${PUBLISH_REGION:-}"

if [[ -z "${bucket}" ]]; then
  echo "PUBLISH_BUCKET is required" >&2
  exit 2
fi
if [[ -z "${dir}" ]]; then
  echo "PUBLISH_DIR is required" >&2
  exit 2
fi

workdir="${dir}"
if [[ "${workdir}" != /* ]]; then
  workdir="/workspace/${workdir}"
fi

args=(--bucket "${bucket}" --prefix "${prefix}" --dir "${workdir}")
if [[ "${no_delete}" == "true" ]]; then
  args+=(--no-delete)
fi
if [[ -n "${region}" ]]; then
  args+=(--region "${region}")
fi
if [[ "${PUBLISH_DRY_RUN:-false}" == "true" ]]; then
  args+=(--dry-run)
fi

echo "Publishing website to s3://${bucket}${prefix}" >&2
echo "Local dir: ${workdir}" >&2

cd /workspace/platform/ffreis-website-packer && "/usr/local/go/bin/go" run ./cmd/website-packer "${args[@]}"

