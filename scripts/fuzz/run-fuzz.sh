#!/usr/bin/env bash
# run-fuzz.sh — run each evener fuzz target's coverage-guided search for a bounded
# time. This is the NIGHTLY/manual campaign, not the gate: `make fuzz` (seed
# corpus only) is what runs in CI. A failing input found here is auto-saved by
# the Go toolchain to the target's testdata/fuzz/<FuzzName>/ directory, where it
# becomes a permanent regression seed that `make fuzz` replays forever.
#
# Usage:
#   scripts/fuzz/run-fuzz.sh [--time DURATION] [target ...]
#     --time DURATION   per-target fuzz budget (default 60s; any go -fuzztime value)
#     target            one or more "module:FuzzName" to restrict the run;
#                       default is every known target below.
#
# Examples:
#   scripts/fuzz/run-fuzz.sh                       # all targets, 60s each
#   scripts/fuzz/run-fuzz.sh --time 5m            # all targets, 5 minutes each
#   scripts/fuzz/run-fuzz.sh llm:FuzzParseSSE     # just the SSE target, 60s
set -uo pipefail

# The fuzz target registry lives in scripts/fuzz/fuzz-targets.txt — one colon-delimited
# entry per line (tag:module:package-relpath:name[:coverpkg[:focus]]); see that file
# for the field documentation. This script loads it and emits the list verbatim via
# `--list`, consumed by scripts/fuzz/fuzz-coverage.sh, scripts/fuzz/fuzz-triage.sh, and the
# static gap gate (cmd/evener-fuzzcov -gap-only). Comment lines (beginning with '#')
# and blank lines in the data file are skipped, so `--list` yields only real entries.
# (macOS bash 3.2 lacks mapfile; this while-read loop is the portable equivalent of
# `mapfile -t TARGETS < "$registry_file"` with comment filtering.)
registry_file="$(dirname "$0")/fuzz-targets.txt"
TARGETS=()
while IFS= read -r _line; do
	case "$_line" in ''|'#'*) continue ;; esac
	TARGETS+=("$_line")
done < "$registry_file"

# The fd-anchored secure-path target exercises Linux-only primitives. Keep it
# in Linux manifests and campaigns without making macOS registry checks report
# a live target as stale.
if [ "$(go env GOOS)" = "linux" ]; then
	TARGETS+=("native:agent:./execenv:FuzzSecurePathEdgeContractProgram::")
	TARGETS+=("native:agent:./execenv:FuzzRuntimeBoundaryEdges::command_runtime.go;local.go;securepath.go")
fi

duration="60s"
declare -a only=()
while [ $# -gt 0 ]; do
	case "$1" in
		--time) duration="$2"; shift 2 ;;
		--time=*) duration="${1#*=}"; shift ;;
		--list) printf '%s\n' "${TARGETS[@]}"; exit 0 ;;
		-h|--help) sed -n '2,20p' "$0"; exit 0 ;;
		*) only+=("$1"); shift ;;
	esac
done

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
fail=0

want() {
	[ ${#only[@]} -eq 0 ] && return 0
	local entry="$1" o
	for o in "${only[@]}"; do
		[ "$o" = "$entry" ] && return 0
	done
	return 1
}

for t in "${TARGETS[@]}"; do
	IFS=: read -r tag module pkg name cover focus <<<"$t"
	want "$module:$name" || continue
	case "$tag" in
		native)
			echo "=== fuzzing $module:$name for $duration ==="
			# -tags evenerfuzz makes the internal/invariant assertions live so a
			# tripped invariant is found as a crasher (see docs/fuzzing.md).
			# run-capped.sh gives each target its own memory ceiling so a leaky
			# search OOMs that one target's scope, never the host. The targets run
			# sequentially, so a per-target cap is the tightest safe bound.
			( cd "$repo_root/$module" && "$repo_root/scripts/fuzz/run-capped.sh" go test -tags evenerfuzz -run '^$' -fuzz "^${name}\$" -fuzztime "$duration" "$pkg" ) || fail=1
			;;
			test)
				echo "=== fuzz-test $module:$name ==="
				( cd "$repo_root/$module" && "$repo_root/scripts/fuzz/run-capped.sh" go test -tags evenerfuzz -run "^${name}\$" -count=1 "$pkg" ) || fail=1
				;;

			rapid)
				# rapid surfaces are property checks driven by `go test -run`; the
				# search depth is governed by -rapid.checks, not -fuzztime, so the
				# --time budget does not apply to them. Keep the campaign depth at
				# rapid's historical default unless the caller intentionally narrows it.
				echo "=== rapid $module:$name ==="
				# EVENER_FUZZ_TESTS=1: the seqfuzz/schemafuzz family t.Skip()s under a
				# plain `go test` (moved out of `make test` per the fuzz-family
				# ruling); this campaign must still drive them.
				( cd "$repo_root/$module" && EVENER_FUZZ_TESTS=1 RAPID_CHECKS="${RAPID_CHECKS:-100}" "$repo_root/scripts/fuzz/run-capped.sh" go test -tags evenerfuzz -run "^${name}\$" -count=1 "$pkg" ) || fail=1
				;;

			*)
			echo "run-fuzz: unknown tag '$tag' in entry '$t'" >&2; fail=1
			;;
	esac
done

exit "$fail"
