# timing-tsv-lib.sh — the test-timing-budget ratchet's row contract, in one
# place. Sourced, never executed.
#
# The ratchet's comparison (scripts/gate/test-timing-budget.sh, compare.py)
# reads four tab-separated row kinds:
#
#   PKG  <package>                    one per package-level terminal event
#   TEST <package> <name> <seconds>   one per test AND subtest result
#   SUM  <package> <seconds>          the package's own wall time
#   CPU  <package> <seconds>          its test binary's (or shards') CPU time
#
# Two producers emit them today and must agree on that shape, so it lives here
# rather than copied into each: the ratchet's own measurement pass (which runs
# `go test -json` / vitest itself), and the gate's single measured run
# (run-module-tests.sh, which sources the same `go test -json` / vitest output
# from the tests the gate already runs — issue #144).
#
# Sourcing defines functions and resolves the checkout root; it touches no file.
# $repo_root, when a caller has set it, names the checkout; otherwise the lib
# resolves it from its own location, so a CI step can source it directly.

# timing_tsv_lib_root is the checkout this lib lives in, resolved when the lib
# is sourced: BASH_SOURCE may be relative to a directory the caller later leaves.
timing_tsv_lib_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# timing_abs PATH — PATH made absolute against the caller's directory, since
# the parser runs from the checkout root.
timing_abs() {
	case "$1" in
	/*) printf '%s\n' "$1" ;;
	*) printf '%s/%s\n' "$PWD" "$1" ;;
	esac
}

# timing_parse ARGS... — `evener-dev timing-parse`, the one parser for both
# producers' output, unit-tested in Go against JSON strings rather than through
# a faked toolchain (issue #172 review; docs/developing-evener/testing.md's
# port-on-touch rule). GOFLAGS= keeps an inherited GOFLAGS from changing how the
# helper itself builds. File arguments must already be absolute.
#
# EVENER_DEV_BIN, when set, is a prebuilt evener-dev the caller already has:
# run-module-tests.sh builds it once instead of paying a `go run` compile per
# module. Unset falls back to `go run`, the ratchet's own idiom.
timing_parse() {
	local root="${repo_root:-$timing_tsv_lib_root}"
	if [ -n "${EVENER_DEV_BIN:-}" ]; then
		( cd "$root" && GOFLAGS= "$EVENER_DEV_BIN" dev timing-parse "$@" )
	else
		( cd "$root" && GOFLAGS= go run ./cmd/evener-dev/bin dev timing-parse "$@" )
	fi
}

# timing_go_test_exec — the `go test -exec` value both producers pass, so each
# test binary runs under test-cpu-exec and its stream carries the package's CPU
# row. It needs EVENER_DEV_BIN, a built evener-dev. go splits the value into
# words with its own quoting rules, so the path is single-quoted: a scratch
# path with a space stays one word. (A path holding a single quote is not one
# a scratch directory produces.)
timing_go_test_exec() {
	printf "'%s' dev test-cpu-exec" "$EVENER_DEV_BIN"
}

# go_test_json_to_tsv PACKAGE_JSON_LOG EXPECTED_PACKAGES_FILE
# — the rows for one `go test -json` stream; a package in the inventory that the
# stream never ends fails the parse.
go_test_json_to_tsv() {
	timing_parse --json "$(timing_abs "$1")" --packages "$(timing_abs "$2")"
}

# vitest_json_to_tsv REPORT_JSON — rolls the whole frontend suite up under one
# package key, "web": vitest has no per-package shape the way a Go module does,
# and the budget's own doc comment names this as the design.
vitest_json_to_tsv() {
	timing_parse --vitest "$(timing_abs "$1")"
}
