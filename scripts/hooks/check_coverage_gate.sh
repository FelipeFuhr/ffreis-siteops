#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}/check_required_tools.sh" go >/dev/null

coverage_min="${COVERAGE_MIN:-90}"

# Statements in files matching this ERE are excluded from the coverage
# DENOMINATOR. Default: the process entrypoint, `main.go`.
#
# Why: `func main()` ends in `os.Exit`, which terminates the test binary, so it
# is unreachable from any in-process test. Counting it means a freshly scaffolded
# repo is born below any non-zero threshold and literally cannot be pushed — and
# the only way past the gate then is to bypass it, which is strictly worse than
# having no gate at all. This is the Go analogue of
# `cargo llvm-cov --ignore-filename-regex 'main\.rs$'` (see ml/ffreis-ml-crypto-rl).
#
# This exclusion is honest ONLY because the fleet's Go entrypoints are one-line
# shims (ffreis-siteops/cmd/siteops/main.go is the reference):
#
#     func main() { os.Exit(run(os.Stdout, os.Stderr, os.Args[1:])) }
#
# with every testable statement in run.go, which IS measured. Moving logic into
# main.go to dodge the gate defeats the gate — put it in run.go instead. A repo
# whose main.go carries real logic must either split it out or set
# COVERAGE_IGNORE_REGEX="" so nothing is excluded.
coverage_ignore_regex_default='(^|/)main\.go$'
coverage_ignore_regex="${COVERAGE_IGNORE_REGEX-${coverage_ignore_regex_default}}"

# An empty module (a freshly scaffolded library) has nothing to measure. Say so
# explicitly — "nothing to verify" must never be reported as "verified clean".
# A non-zero `go list` is a real build failure and must fail the gate.
#
# -buildvcs=false: `go list` stamps VCS metadata and dies with "error obtaining
# VCS status: exit status 128" inside a git worktree, which is how every agent
# session in this workspace runs (quality-kit/scripts/session-worktree.sh).
# Coverage does not depend on VCS stamping, so turn it off for this probe.
if ! packages="$(go list -buildvcs=false ./... 2>/dev/null)"; then
  echo "Coverage gate: 'go list ./...' failed — cannot verify coverage." >&2
  go list -buildvcs=false ./... >&2 || true
  exit 1
fi
if [[ -z "${packages}" ]]; then
  echo "Coverage gate: this module has no Go packages — nothing to measure."
  exit 0
fi

profile_file="$(mktemp)"
filtered_file="$(mktemp)"
trap 'rm -f "${profile_file}" "${filtered_file}"' EXIT

go test ./... -coverprofile="${profile_file}"

if [[ ! -s "${profile_file}" ]]; then
  echo "Coverage gate: 'go test' produced an empty profile — cannot verify coverage." >&2
  exit 1
fi

# Profile format: line 1 is `mode: <set|count|atomic>`; every other line is
# `<file>:<startLine>.<col>,<endLine>.<col> <numStatements> <hitCount>`.
if [[ -n "${coverage_ignore_regex}" ]]; then
  # The regex arrives via the environment rather than `awk -v` because awk
  # expands escape sequences in -v assignments (`\.` would decay to `.`).
  COVERAGE_IGNORE_REGEX="${coverage_ignore_regex}" awk '
    BEGIN { ignore_re = ENVIRON["COVERAGE_IGNORE_REGEX"] }
    NR == 1 { print; next }
    { split($0, field, ":"); if (field[1] !~ ignore_re) print }
  ' "${profile_file}" >"${filtered_file}"
else
  cp "${profile_file}" "${filtered_file}"
fi

total_blocks=$(($(wc -l <"${profile_file}") - 1))
kept_blocks=$(($(wc -l <"${filtered_file}") - 1))
excluded_blocks=$((total_blocks - kept_blocks))

# `go tool cover -func` reports a bare `total: 0.0%` for a profile with no
# blocks left, which would read as a 0% FAILURE rather than "nothing measured".
# Detect the empty case here instead of inferring it from that number.
if ((kept_blocks <= 0)); then
  echo "Coverage gate: all ${total_blocks} coverage block(s) excluded by" \
    "COVERAGE_IGNORE_REGEX='${coverage_ignore_regex}' — no measurable code yet."
  echo "This is a vacuous pass, not a verified one: add testable logic outside the excluded files."
  exit 0
fi

total_line="$(go tool cover -func="${filtered_file}" | awk '/^total:/ {print $0}')"
if [[ -z "${total_line}" ]]; then
  echo "Unable to determine total coverage from ${filtered_file}." >&2
  exit 1
fi

total_pct="$(awk '/^total:/ {gsub("%", "", $3); print $3}' <<<"${total_line}")"

scope_note=""
if ((excluded_blocks > 0)); then
  scope_note=" (${excluded_blocks}/${total_blocks} block(s) excluded by COVERAGE_IGNORE_REGEX='${coverage_ignore_regex}')"
fi

if awk -v total="${total_pct}" -v min="${coverage_min}" 'BEGIN { exit !(total+0 >= min+0) }'; then
  echo "Coverage gate passed: ${total_pct}% >= ${coverage_min}%${scope_note}"
  exit 0
fi

echo "Coverage gate failed: ${total_pct}% < ${coverage_min}%${scope_note}." >&2
uncovered="$(go tool cover -func="${filtered_file}" | awk '!/^total:/ && $NF == "0.0%" && ++shown <= 20')"
if [[ -n "${uncovered}" ]]; then
  echo "Fully uncovered functions:" >&2
  echo "${uncovered}" >&2
fi
echo "Add tests for the code above. Do not lower COVERAGE_MIN and do not bypass the hook." >&2
exit 1
