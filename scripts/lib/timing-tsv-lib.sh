# timing-tsv-lib.sh — the test-timing-budget ratchet's row contract, in one
# place. Sourced, never executed.
#
# The ratchet's comparison (scripts/gate/test-timing-budget.sh, compare.py)
# reads three tab-separated row kinds:
#
#   PKG  <package>                    one per package-level terminal event
#   TEST <package> <name> <seconds>   one per test AND subtest result
#   SUM  <package> <seconds>          the package's own wall time
#
# Two producers emit them today and must agree on that shape, so it lives here
# rather than copied into each: the ratchet's own measurement pass (which runs
# `go test -json` / vitest itself), and the gate's single measured run
# (run-module-tests.sh, which sources the same `go test -json` / vitest output
# from the tests the gate already runs — issue #144).
#
# Sourcing defines functions only; it touches no file and launches nothing.
# Callers must have set $repo_root (both scripts do).

# timing_parse ARGS... — `evener-dev timing-parse`, the one parser for both
# producers' output, unit-tested in Go against JSON strings rather than through
# a faked toolchain (issue #172 review; docs/developing-evener/testing.md's
# port-on-touch rule). GOFLAGS= keeps an inherited GOFLAGS from changing how the
# helper itself builds.
#
# EVENER_DEV_BIN, when set, is a prebuilt evener-dev the caller already has:
# run-module-tests.sh builds it once instead of paying a `go run` compile per
# module. Unset falls back to `go run`, the ratchet's own idiom.
timing_parse() {
	if [ -n "${EVENER_DEV_BIN:-}" ]; then
		( cd "$repo_root" && GOFLAGS= "$EVENER_DEV_BIN" dev timing-parse "$@" )
	else
		( cd "$repo_root" && GOFLAGS= go run ./cmd/evener-dev/bin dev timing-parse "$@" )
	fi
}

# go_test_json_to_tsv PACKAGE_JSON_LOG EXPECTED_PACKAGES_FILE
# — the rows for one `go test -json` stream; a package in the inventory that the
# stream never ends fails the parse.
go_test_json_to_tsv() {
	timing_parse --json "$1" --packages "$2"
}

# vitest_json_to_tsv REPORT_JSON — rolls the whole frontend suite up under one
# package key, "web": vitest has no per-package shape the way a Go module does,
# and the budget's own doc comment names this as the design.
vitest_json_to_tsv() {
	timing_parse --vitest "$1"
}
