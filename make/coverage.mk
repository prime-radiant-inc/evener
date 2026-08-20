.PHONY: e2e-cover coverage-floor coverage-floor-selftest coverage-gaps coverage-gaps-selftest

# e2e-cover measures END-TO-END coverage of the real evener/evener-tui binaries via
# `go build -cover` + GOCOVERDIR — the main()/CLI/dispatch/serve paths unit tests
# structurally can't reach. --merge-unit unions it with the unit profile for a
# combined whole-repo number; EVENER_E2E_LIVE=1 additionally runs the live provider
# scripts (needs real credentials). Local/on-demand, not a gate.
## Measure end-to-end coverage of the real evener/evener-tui binaries via
## `go build -cover` + GOCOVERDIR, unioned with the unit profile.
## EVENER_E2E_LIVE=1 additionally runs the live provider scripts. Local and
## on-demand, not a gate.
e2e-cover:
	@scripts/coverage/e2e-cover.sh --merge-unit

# coverage-floor is the repo's one coverage ratchet: per module, the union of
# the test track and the deterministic fuzz-replay track, plus the frontend's
# vitest line coverage, against scripts/coverage/coverage-floors.txt. Bare
# invocation reports; CHECK=1 fails on a drop; BLESS=1 raises floors. Heavy +
# local. Its contract is pinned by scripts/coverage/coverage-floor-selftest.sh
# in the dev-tooling wave.
## The repo's one coverage ratchet: per module, the union of the test track
## and the deterministic fuzz-replay track, plus the frontend's vitest line
## coverage.
## proves: Per-module statement coverage reached by any deterministic test
##   (the test track unioned with the deterministic fuzz-seed replay), plus
##   the frontend's vitest line coverage, against
##   scripts/coverage/coverage-floors.txt.
## trigger: Local/on-demand; not required CI (heavier than make test). Bare
##   invocation reports; CHECK=1 to gate; BLESS=1 to raise floors.
## requires: Deterministic; no provider calls. The fuzz half replays
##   committed seed corpora only (go test without -fuzz), never a search.
## fails-when: Under CHECK=1, any row falls below its floor beyond the
##   tolerance band; a floored row that cannot be measured fails loudly
##   rather than skipping.
coverage-floor:
	@scripts/coverage/coverage-floor.sh $(if $(CHECK),--check) $(if $(BLESS),--bless) $(COV_ARGS)

## Exercise coverage-floor.sh against a throwaway repo and a fake `go` that
## emits two different profiles depending on the evenerfuzz tag.
## proves: The union arithmetic is correct — a fixture where the test track
##   covers only block A and the fuzz track covers only block B, so a
##   correct union reports 100% where each track alone reports 40% and 60%.
## trigger: make test-dev-tooling wave; on demand.
## requires: Offline and deterministic; no real go build or suite runs, only
##   a faked go binary and a throwaway repo.
## fails-when: The computed union coverage diverges from the fixture's
##   hand-computed answer, or the suite leaves files behind.
coverage-floor-selftest:
	@scripts/coverage/coverage-floor-selftest.sh

# coverage-gaps ranks where a coverage profile's UNCOVERED statements are, by
# count rather than percentage, so coverage work targets the largest real gaps.
# Takes a profile: `make coverage-gaps PROFILE=path/to.cov GAP_ARGS="--by file"`.
## Rank a coverage profile's uncovered statements by count, not percentage,
## so coverage work targets the largest real gaps.
## Takes a profile: `make coverage-gaps PROFILE=path/to.cov GAP_ARGS="--by file"`.
coverage-gaps:
	@scripts/coverage/coverage-gaps.sh $(PROFILE) $(GAP_ARGS)

## Exercise coverage-gaps.sh against a synthetic coverage profile with
## hand-computed answers.
## proves: The ranking arithmetic — dedup, ranking, and totals — including
##   that the same block position hit twice is counted once.
## trigger: make test-dev-tooling wave; on demand.
## requires: Offline and deterministic; no go test or compilation, arithmetic
##   only.
## fails-when: The ranked output diverges from the fixture's hand-computed
##   answer, or the suite leaves files behind.
coverage-gaps-selftest:
	@scripts/coverage/coverage-gaps-selftest.sh
