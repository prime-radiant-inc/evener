#!/usr/bin/env bash
#
# gitleaks-scan.sh — repo-wide or corpus-scoped secret scan with gitleaks.
#
# Usage:
#   scripts/gitleaks-scan.sh repo      # whole working tree (make secret-scan)
#   scripts/gitleaks-scan.sh corpus    # only the fuzz seed corpora (make fuzz-corpus-scan)
#
# Both use the committed .gitleaks.toml ruleset — the same engine the harvester's
# write-time barrier shells out to, so the writer and the repo gate cannot drift.
#
# If gitleaks is not installed the scan is SKIPPED with a warning and a zero exit,
# so the gate stays green where the tool is absent (it is required in CI). Install:
#   https://github.com/gitleaks/gitleaks#installing
set -euo pipefail

mode="${1:-repo}"
root="$(git rev-parse --show-toplevel)"
cfg="${root}/.gitleaks.toml"

if ! command -v gitleaks >/dev/null 2>&1; then
  echo "warning: gitleaks not installed; skipping ${mode} secret scan (install: https://github.com/gitleaks/gitleaks)" >&2
  exit 0
fi

scan_dir() {
  # gitleaks exits non-zero on a finding; --redact keeps any match out of the log.
  gitleaks detect --no-git --redact --config "${cfg}" --source "$1"
}

case "${mode}" in
  repo)
    scan_dir "${root}"
    ;;
  corpus)
    status=0
    while IFS= read -r dir; do
      scan_dir "${dir}" || status=1
    done < <(find "${root}" -type d \
      \( -path '*/testdata/fuzz' -o -name 'fuzz-jobs-staging' -o -path '*/fuzz/corpus' \) )
    exit "${status}"
    ;;
  *)
    echo "usage: $0 {repo|corpus}" >&2
    exit 2
    ;;
esac
