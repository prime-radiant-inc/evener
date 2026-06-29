#!/usr/bin/env bash
# fuzz-continuous.sh — a LOCAL, on-demand continuous fuzzing loop (roadmap 8.5,
# continuous infra). It rotates over every native fuzz target, giving each a
# bounded search turn, round after round, until a total budget elapses or you
# Ctrl-C. There is no scheduled CI: you start it when you want and stop it when
# you want. Each turn delegates the actual search + crash triage to
# scripts/fuzz-triage.sh, so a new DETERMINISTIC crash is flake-guarded,
# deduped, and turned into exactly one human-reviewable PR — the same pipeline
# as a one-shot triage, just driven continuously.
#
# Why a loop is more than one long run: Go's coverage-guided corpus persists in
# $GOCACHE/fuzz across invocations, so each turn a target gets resumes the
# search where the last turn left off and goes deeper. Rotating keeps every
# target progressing instead of spending the whole budget on the first one.
#
# Usage:
#   scripts/fuzz-continuous.sh [--total DUR] [--time DUR] [--sweep]
#                              [--max-turns N] [--dry-run] [--no-pr] [target ...]
#     --total DUR    total wall-clock budget for the whole session (e.g. 2h, 90m,
#                    3600s). Default: unlimited — runs until interrupted.
#     --time DUR     per-target search budget each turn, passed to fuzz-triage
#                    (default inherits run-fuzz.sh's 60s; e.g. --time 5m).
#     --sweep        each round runs ALL selected targets once before repeating;
#                    default is round-robin, one target per turn.
#     --max-turns N  stop after N turns regardless of the time budget (a bounded
#                    "do some rounds" run; also used by the self-test).
#     --dry-run      pass through to fuzz-triage: discover/guard/dedup only, no
#                    writes and no PR.
#     --no-pr        pass through to fuzz-triage: commit crashers to a local
#                    branch but do not push / open a PR.
#     target ...     restrict the rotation to these "module:FuzzName" entries
#                    from run-fuzz.sh's registry; default is every native target.
#
# A new crasher's committed corpus is left to an explicit `make fuzz-triage`
# (this loop passes --no-corpus per turn to avoid churning uncommitted testdata
# every round); the coverage-guided $GOCACHE corpus still accumulates regardless.
#
# Ctrl-C stops the session promptly: the in-flight `go test` exits (saving its
# corpus), and the loop prints a session summary before exiting.
#
# Env seams (defaults are the production values; used by the self-test):
#   SERF_FUZZ_REPO_ROOT  repo root (default: the parent of this script's dir)
#   SERF_FUZZ_RUNNER     the registry source (default: scripts/run-fuzz.sh)
#   SERF_FUZZ_TRIAGE     the per-turn engine  (default: scripts/fuzz-triage.sh)
set -uo pipefail

repo_root="${SERF_FUZZ_REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
runner="${SERF_FUZZ_RUNNER:-$repo_root/scripts/run-fuzz.sh}"
triage="${SERF_FUZZ_TRIAGE:-$repo_root/scripts/fuzz-triage.sh}"
ledger="$repo_root/fuzz/state/ledger.json"

total=""          # total budget in seconds (empty = unlimited)
duration=""       # per-turn budget passed through to fuzz-triage
sweep=false
max_turns=""
dry_run=false
no_pr=false
declare -a only=()

# to_seconds turns a Go-style duration (2h, 90m, 3600s, or a bare integer of
# seconds) into an integer number of seconds.
to_seconds() {
	local d="$1" n unit
	case "$d" in
		*h) n="${d%h}"; unit=3600 ;;
		*m) n="${d%m}"; unit=60 ;;
		*s) n="${d%s}"; unit=1 ;;
		*) n="$d"; unit=1 ;;
	esac
	case "$n" in
		''|*[!0-9]*) echo "fuzz-continuous: bad duration '$d'" >&2; exit 2 ;;
	esac
	echo $((n * unit))
}

while [ $# -gt 0 ]; do
	case "$1" in
		--total) total="$(to_seconds "$2")"; shift 2 ;;
		--total=*) total="$(to_seconds "${1#*=}")"; shift ;;
		--time) duration="$2"; shift 2 ;;
		--time=*) duration="${1#*=}"; shift ;;
		--sweep) sweep=true; shift ;;
		--max-turns) max_turns="$2"; shift 2 ;;
		--max-turns=*) max_turns="${1#*=}"; shift ;;
		--dry-run) dry_run=true; shift ;;
		--no-pr) no_pr=true; shift ;;
		-h|--help) sed -n '2,57p' "$0"; exit 0 ;;
		--*) echo "fuzz-continuous: unknown flag $1" >&2; exit 2 ;;
		*) only+=("$1"); shift ;;
	esac
done

# Resolve the rotation: every native target from the registry, filtered to the
# requested subset. Rapid targets are bounded property checks (run by `go test`
# in the normal suite), not coverage-guided searches that deepen with corpus, so
# they are excluded from the continuous rotation.
want() {
	[ ${#only[@]} -eq 0 ] && return 0
	local key="$1" o
	for o in "${only[@]}"; do [ "$o" = "$key" ] && return 0; done
	return 1
}

declare -a rotation=()
while IFS=: read -r tag module pkg name _rest; do
	[ "$tag" = native ] || continue
	want "$module:$name" || continue
	rotation+=("$module:$name")
done < <(bash "$runner" --list)

if [ ${#rotation[@]} -eq 0 ]; then
	echo "fuzz-continuous: no matching native targets" >&2
	exit 2
fi

# crasher_keys prints the set of ledger signatures currently recorded, one per
# line (empty when the ledger does not yet exist). Used to report crashers found
# during this session as a before/after delta.
crasher_keys() {
	[ -f "$ledger" ] || return 0
	jq -r 'keys[]' "$ledger" 2>/dev/null || true
}

stop=false
on_interrupt() { stop=true; echo; echo "fuzz-continuous: interrupt — finishing up…"; }
trap on_interrupt INT TERM

start_epoch="$(date +%s)"
keys_before="$(crasher_keys | sort)"
turn=0
idx=0
declare -A target_turns=()

elapsed() { echo $(( $(date +%s) - start_epoch )); }

mode=""
$dry_run && mode+=" [dry-run]"
$no_pr && mode+=" [no-pr]"
echo "=== fuzz-continuous: ${#rotation[@]} target(s)$mode ==="
[ -n "$total" ] && echo "    total budget: ${total}s" || echo "    total budget: unlimited (Ctrl-C to stop)"
[ -n "$duration" ] && echo "    per-turn: $duration" || echo "    per-turn: run-fuzz default (60s)"
$sweep && echo "    rotation: sweep (all targets per round)" || echo "    rotation: round-robin"

# one_turn runs fuzz-triage for a single target and reports any newly recorded
# crasher signature.
one_turn() {
	local target="$1" before after
	turn=$((turn + 1))
	target_turns["$target"]=$(( ${target_turns["$target"]:-0} + 1 ))
	echo "--- turn $turn | $target | elapsed $(elapsed)s${total:+/${total}s} ---"
	before="$(crasher_keys | sort)"

	local args=()
	[ -n "$duration" ] && args+=(--time "$duration")
	$dry_run && args+=(--dry-run)
	$no_pr && args+=(--no-pr)
	args+=(--no-corpus "$target")
	bash "$triage" "${args[@]}" || echo "fuzz-continuous: turn for $target exited non-zero (continuing)"

	after="$(crasher_keys | sort)"
	local new
	new="$(comm -13 <(printf '%s\n' "$before") <(printf '%s\n' "$after") | sed '/^$/d')"
	[ -n "$new" ] && echo "fuzz-continuous: NEW crasher signature(s) this turn:" && printf '    %s\n' $new
}

budget_left() {
	[ -z "$total" ] && return 0
	[ "$(elapsed)" -lt "$total" ] && return 0
	return 1
}

turns_left() {
	[ -z "$max_turns" ] && return 0
	[ "$turn" -lt "$max_turns" ] && return 0
	return 1
}

while ! $stop && budget_left && turns_left; do
	if $sweep; then
		for target in "${rotation[@]}"; do
			$stop && break
			budget_left || break
			turns_left || break
			one_turn "$target"
		done
	else
		one_turn "${rotation[$idx]}"
		idx=$(( (idx + 1) % ${#rotation[@]} ))
	fi
done

# --- session summary ---------------------------------------------------------
keys_after="$(crasher_keys | sort)"
new_session="$(comm -13 <(printf '%s\n' "$keys_before") <(printf '%s\n' "$keys_after") | sed '/^$/d')"

echo "=== fuzz-continuous: session summary ==="
echo "    turns: $turn   elapsed: $(elapsed)s"
echo "    targets exercised:"
for target in "${rotation[@]}"; do
	[ "${target_turns[$target]:-0}" -gt 0 ] && printf '      %-50s %d turn(s)\n' "$target" "${target_turns[$target]}"
done
if [ -n "$new_session" ]; then
	echo "    NEW crasher signature(s) this session:"
	printf '      %s\n' $new_session
	exit 1
fi
echo "    no new crashers this session"
