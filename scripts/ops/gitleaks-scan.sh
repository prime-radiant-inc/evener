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
# If gitleaks is not installed the scan is SKIPPED with a warning and a zero exit
# for local runs. Set EVENER_GITLEAKS_REQUIRED=1 for gates that require the tool.
# Install:
#   https://github.com/gitleaks/gitleaks#installing
set -euo pipefail

mode="${1:-repo}"
root="$(git rev-parse --show-toplevel)"
cfg="${root}/.gitleaks.toml"

if ! command -v gitleaks >/dev/null 2>&1; then
  if [ "${EVENER_GITLEAKS_REQUIRED:-}" = 1 ]; then
    echo "error: gitleaks is required but not installed; cannot run ${mode} secret scan (install: https://github.com/gitleaks/gitleaks)" >&2
    exit 1
  fi
  echo "warning: gitleaks not installed; skipping ${mode} secret scan (install: https://github.com/gitleaks/gitleaks)" >&2
  exit 0
fi

# Every scan runs FROM the repo root over a path RELATIVE to it, because the
# ruleset's path allowlist is matched against the path gitleaks builds from
# --source. An absolute source makes every path start with the checkout's own
# location, and one of those allowlist entries excludes `.claude/worktrees/`
# (the linked worktrees a main checkout would otherwise walk into). Scanning a
# worktree by its absolute path therefore matched that entry for every file in
# it and scanned ~0 bytes — the gate passed by looking at nothing (#1513).
# Relative paths carry no checkout location, so a worktree scans itself and a
# main checkout still skips the worktrees underneath it.
scan_dir() {
  # gitleaks exits non-zero on a finding; --redact keeps any match out of the log.
  (cd "${root}" && gitleaks detect --no-git --redact --config "${cfg}" --source "$1")
}

case "${mode}" in
  repo)
    scan_dir "."
    ;;
  corpus)
    status=0
    while IFS= read -r dir; do
      scan_dir "${dir}" || status=1
    done < <(cd "${root}" && find . -type d \
      \( -path '*/testdata/fuzz' -o -path '*/fuzz/corpus' \) )
    exit "${status}"
    ;;
  *)
    echo "usage: $0 {repo|corpus}" >&2
    exit 2
    ;;
esac
