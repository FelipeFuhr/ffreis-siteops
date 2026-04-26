#!/usr/bin/env bash
set -euo pipefail

dist_id="${CLOUDFRONT_DISTRIBUTION_ID:-}"
paths="${CLOUDFRONT_PATHS:-/*}"

if [[ -z "${dist_id}" ]]; then
  echo "No CLOUDFRONT_DISTRIBUTION_ID set; skipping invalidation." >&2
  exit 0
fi

echo "Creating CloudFront invalidation: distribution=${dist_id} paths=${paths}" >&2
aws cloudfront create-invalidation --distribution-id "${dist_id}" --paths ${paths}

