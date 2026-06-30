#!/usr/bin/env bash
# fuzz-mutation-score.sh — Phase 10 W5: measure DETECTION sufficiency with
# gremlins mutation testing. For each curated (module, package) it runs
# `gremlins unleash` and reports the kill score (test efficacy = killed /
# (killed+lived)). A surviving (LIVED) mutant is the payoff: code a mutant
# survives is code our tests + committed fuzz seeds do not actually pin — the
# weak-oracle worklist. This complements the curated oracle-audit
# (fuzz-oracle-audit.sh, which proves specific oracles redden) with a broad,
# per-package kill rate.
#
# Nightly / manual: mutation testing is slow (every mutant re-runs the package's
# tests). gremlins must be installed:
#   go install github.com/go-gremlins/gremlins/cmd/gremlins@latest
#
# serf's tests need a generous per-mutant timeout (gremlins' measured baseline is
# small; a default coefficient makes legitimate mutants spuriously "time out"),
# so the coefficient defaults to 20 — tune via SERF_MUTATION_TIMEOUT_COEFF.
#
# Usage:
#   scripts/fuzz-mutation-score.sh [module:./pkg ...]   # default: the curated set
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
coeff="${SERF_MUTATION_TIMEOUT_COEFF:-20}"

if ! command -v gremlins >/dev/null 2>&1; then
	echo "fuzz-mutation-score: gremlins not installed —" >&2
	echo "  go install github.com/go-gremlins/gremlins/cmd/gremlins@latest" >&2
	exit 2
fi

declare -a targets=("$@")
if [ ${#targets[@]} -eq 0 ]; then
	# Curated fuzz-covered parse/logic packages, one per module. Kept small: this
	# is a slow nightly pass, not the gate. Pass args to score other packages.
	targets=(
		"llm:./providercfg"
		"agent:./internal/frontmatter"
		"agent:./plugin"
		".:./frontmatter"
	)
fi

fail=0
for t in "${targets[@]}"; do
	module="${t%%:*}"
	pkg="${t#*:}"
	echo "=== mutation score: $t (coeff $coeff) ==="
	if ! ( cd "$repo_root/$module" && gremlins unleash --timeout-coefficient "$coeff" "$pkg" 2>&1 |
		grep -iE 'LIVED|NOT COVERED|Killed:|Test efficacy|Mutator coverage' ); then
		echo "fuzz-mutation-score: gremlins failed for $t" >&2
		fail=1
	fi
done
exit "$fail"
