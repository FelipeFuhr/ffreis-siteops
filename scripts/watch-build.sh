#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="${ROOT_DIR:-/workspace}"
WEBSITE_ROOT="${WEBSITE_ROOT:-}"
OUT_DIR="${OUT_DIR:-}"
INLINE_ASSETS="${INLINE_ASSETS:-false}"
COPY_ASSETS="${COPY_ASSETS:-true}"
WEBSITE_COMPILER_BIN="${WEBSITE_COMPILER_BIN:-/usr/local/bin/website-compiler}"
STITCHER_WATCH_PATH="${STITCHER_WATCH_PATH:-.}"

[ -n "${WEBSITE_ROOT}" ] || { echo "WEBSITE_ROOT is required"; exit 1; }
[ -n "${OUT_DIR}" ] || { echo "OUT_DIR is required"; exit 1; }
[ -x "${WEBSITE_COMPILER_BIN}" ] || { echo "WEBSITE_COMPILER_BIN is not executable: ${WEBSITE_COMPILER_BIN}"; exit 1; }

cd "${ROOT_DIR}"
[ -d "${STITCHER_WATCH_PATH}" ] || { echo "STITCHER_WATCH_PATH is not a directory: ${STITCHER_WATCH_PATH}"; exit 1; }

build_site() {
  echo "[$(date -Iseconds)] Building site..."
  "${WEBSITE_COMPILER_BIN}" build \
    -website-root "${WEBSITE_ROOT}" \
    -out "${OUT_DIR}" \
    -inline-assets="${INLINE_ASSETS}" \
    -copy-assets="${COPY_ASSETS}"
  echo "[$(date -Iseconds)] Build complete."
}

watch_loop() {
  inotifywait -r -e modify,create,delete,move \
    --exclude '(^|/)(\.git|dist|node_modules|\.venv)(/|$)' \
    "${WEBSITE_ROOT}" "${STITCHER_WATCH_PATH}"
}

build_site

echo "Watching for changes in ${WEBSITE_ROOT} and ${STITCHER_WATCH_PATH} ..."
while watch_loop; do
  build_site || echo "[$(date -Iseconds)] Build failed; waiting for next change..."
done
