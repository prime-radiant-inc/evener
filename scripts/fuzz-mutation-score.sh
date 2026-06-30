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
# READING THE NUMBERS — two gremlins artifacts make "mutator coverage" understate
# real test coverage, especially for switch/codec-heavy packages (e.g. appwire):
#   1. SWITCH-CASE CONDITIONS. Go's coverage blocks for `switch { case C: }` start
#      at the case BODY, so the case-expression position C has no counter. gremlins
#      mutates C and, finding no covering block, reports NOT COVERED even when the
#      branch is fully exercised (confirm with `go test -cover`: the func reads
#      100%). Same for tagless-switch dispatch (Kind/IDString-style methods).
#   2. CONST DECLARATIONS. `const X = -32602` is not an executable statement, so a
#      mutated constant is always NOT COVERED — assert against the literal value in
#      a test to pin it, but gremlins still can't credit coverage.
# So judge a package by its LIVED (covered-but-survived = the weak-oracle worklist)
# and by `go test -cover`, NOT by mutator-coverage alone. Equivalent mutants
# (clamp-to-boundary no-ops, always-true invariant.Hold guards, cosmetic 0-basing)
# legitimately LIVE and are not gaps.
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
		"agent:./task"
		"agent:./internal/contextmgr"
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
