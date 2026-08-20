.PHONY: e2e-cover coverage-floor coverage-floor-selftest coverage-gaps coverage-gaps-selftest

# e2e-cover measures END-TO-END coverage of the real evener/evener-tui binaries via
# `go build -cover` + GOCOVERDIR — the main()/CLI/dispatch/serve paths unit tests
# structurally can't reach. --merge-unit unions it with the unit profile for a
# combined whole-repo number; EVENER_E2E_LIVE=1 additionally runs the live provider
# scripts (needs real credentials). Local/on-demand, not a gate.
e2e-cover:
	@scripts/coverage/e2e-cover.sh --merge-unit

# coverage-floor is the repo's one coverage ratchet: per module, the union of
# the test track and the deterministic fuzz-replay track, plus the frontend's
# vitest line coverage, against scripts/coverage/coverage-floors.txt. Bare
# invocation reports; CHECK=1 fails on a drop; BLESS=1 raises floors. Heavy +
# local. Its contract is pinned by scripts/coverage/coverage-floor-selftest.sh
# in the dev-tooling wave.
coverage-floor:
	@scripts/coverage/coverage-floor.sh $(if $(CHECK),--check) $(if $(BLESS),--bless) $(COV_ARGS)

coverage-floor-selftest:
	@scripts/coverage/coverage-floor-selftest.sh

# coverage-gaps ranks where a coverage profile's UNCOVERED statements are, by
# count rather than percentage, so coverage work targets the largest real gaps.
# Takes a profile: `make coverage-gaps PROFILE=path/to.cov GAP_ARGS="--by file"`.
coverage-gaps:
	@scripts/coverage/coverage-gaps.sh $(PROFILE) $(GAP_ARGS)

coverage-gaps-selftest:
	@scripts/coverage/coverage-gaps-selftest.sh
