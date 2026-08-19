#!/usr/bin/env bash
# gate-capability-preflight.sh — classifies the sandbox-sensitive host
# capabilities merge-approval-gate's live/e2e components depend on (loopback
# binds, a Chrome/Chromium binary, process inspection via `ps`, and a
# writable external git cache directory), ONCE, before any gate phase runs.
#
# USAGE (from the Makefile's merge-approval-gate recipe):
#   eval "$(scripts/gate-capability-preflight.sh)"
#
# This script's own stdout is not meant to be read directly - it is shell
# code for the caller to eval. That code:
#   - exports EVENER_GATE_CAPABILITY_SKIP, a `go test -skip` regex covering
#     every known test-name pattern that needs a capability this preflight
#     found blocked (empty when nothing is blocked, so run-module-tests.sh
#     never sees an unset variable under `set -u`);
#   - echoes a structured, human-readable summary to stderr: which
#     capabilities are AVAILABLE, which are BLOCKED with why, and the exact
#     command to re-probe or re-run each blocked capability's tests;
#   - on an internal failure (the probe tool itself could not run, or
#     produced output this script cannot parse), echoes a diagnostic to
#     stderr and evaluates to `exit 1`, stopping the caller's gate before any
#     phase runs rather than silently proceeding uncertain.
#
# FAKE_GATE_PROBE_BLOCKED is a selftest-only override: when set (even to an
# empty string), every capability in it (space-separated) is reported BLOCKED
# without invoking the real probe tool at all, and every capability NOT in it
# is reported AVAILABLE. Real invocations never set this.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
. "$script_dir/../lib/gate-surface-lib.sh"

capability_ids="loopback-bind chrome-cdp process-inspect git-cache"
tab="$(printf '\t')"

# emit_fatal DIAGNOSTIC — print shell code that reports DIAGNOSTIC on stderr
# and stops the caller's `&&` chain, then return successfully: this script's
# own job (printing valid shell for the caller to eval) is done either way.
emit_fatal() {
	printf 'echo %s >&2\n' "$(printf '%q' "merge-approval-gate: capability preflight failed: $1")"
	printf 'exit 1\n'
	exit 0
}

if [ "${FAKE_GATE_PROBE_BLOCKED+set}" = set ]; then
	lines=""
	for id in $capability_ids; do
		case " $FAKE_GATE_PROBE_BLOCKED " in
			*" $id "*)
				lines="${lines}${id}${tab}BLOCKED${tab}forced blocked via FAKE_GATE_PROBE_BLOCKED (selftest)${tab}go run ./cmd/evener-gate-probe -only=$id
"
				;;
			*)
				lines="${lines}${id}${tab}AVAILABLE${tab}${tab}
"
				;;
		esac
	done
else
	if ! lines="$(cd "$repo_root" && go run ./cmd/evener-gate-probe 2>&1)"; then
		emit_fatal "go run ./cmd/evener-gate-probe exited nonzero: $lines"
	fi
fi

skip_regex=""
summary_lines=()
seen_ids=""

while IFS="$tab" read -r id status reason rerun; do
	[ -n "$id" ] || continue
	seen_ids="$seen_ids $id"
	case "$status" in
		BLOCKED)
			pattern="$(gate_capability_skip_pattern "$id")"
			if [ -n "$pattern" ]; then
				if [ -n "$skip_regex" ]; then
					skip_regex="${skip_regex}|${pattern}"
				else
					skip_regex="$pattern"
				fi
				summary_lines+=("BLOCKED $id: $reason -- skips tests matching '$pattern'; rerun once fixed: ROOT_FULL=1 go test ./... -run '$pattern' -v; reprobe: $rerun")
			else
				summary_lines+=("BLOCKED $id: $reason -- no gate component currently depends on this; reprobe: $rerun")
			fi
			;;
		AVAILABLE)
			summary_lines+=("AVAILABLE $id")
			;;
		*)
			emit_fatal "unrecognized capability status for $id: $status (from: $id$tab$status$tab$reason$tab$rerun)"
			;;
	esac
done <<PROBE_LINES
$lines
PROBE_LINES

for id in $capability_ids; do
	case " $seen_ids " in
		*" $id "*) ;;
		*) emit_fatal "the probe never classified $id" ;;
	esac
done

printf 'export EVENER_GATE_CAPABILITY_SKIP=%s\n' "$(printf '%q' "$skip_regex")"
for line in "${summary_lines[@]}"; do
	printf 'echo %s >&2\n' "$(printf '%q' "$line")"
done
