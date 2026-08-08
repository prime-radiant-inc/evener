#!/usr/bin/env bash
# deploy-hub.sh — build and restart THIS worktree's launchd-managed serf-hub,
# without ever taking the old hub down before the new one is confirmed built
# and healthy (kata mssy).
#
# Why this exists: shipping a WebUI fix live used to mean manually finding
# the launchd label and binary path, building embedded assets by hand,
# diagnosing PATH/GOCACHE, running `launchctl kickstart -k`, comparing PIDs
# by eye, probing /api/health, and inspecting built CSS — each step a place
# to get it wrong, and an interrupted `kickstart` can return an ambiguous
# exit status even when the restart itself succeeded (see
# docs/serf-hub-remote-operations.md, "Restarting an ad hoc Hub"). This
# script automates the whole sequence and treats /api/health, not
# `kickstart`'s exit code, as the source of truth for whether the restart
# worked.
#
# What it does, in order:
#   1. Preflight   — find the launchd label for serf-hub (auto-detected via
#                     `launchctl list`, or pass --label), confirm the job's
#                     `program` path is THIS worktree's serf-hub binary (a
#                     mismatch means the label belongs to a different
#                     checkout — refuse rather than restart someone else's
#                     hub), and record the old PID.
#   2. Build       — `make build-hub`, which builds the frontend then the
#                     serf/serf-hub pair into a temp dir and `mv`s them into
#                     place only on success (scripts/build-runtime-pair.sh).
#                     A failed build never touches the binary that's
#                     currently running — launchd/macOS keeps executing the
#                     old file's inode regardless of what happens to the
#                     path on disk, so the OLD hub is left running untouched
#                     by construction, not by any rollback logic here.
#   3. Restart     — `launchctl kickstart -k` on the specific label found in
#                     step 1 only. Never a blanket restart of anything else.
#   4. Verification — poll /api/health until it answers (or --timeout
#                     elapses), then report old PID vs new PID, old vs new
#                     `version` (compared against `git rev-parse --short
#                     HEAD` in this checkout, +"-dirty" if the tree is
#                     dirty), and the frontend dist bundle's mtime.
#
# This script never deletes or overwrites a known-good binary as a "rollback
# path" — there isn't one to keep. If the build fails, nothing after step 1
# ran, so the old process is simply still running. If kickstart or the
# health probe fails AFTER a successful build, the new binary is already the
# one on disk (same as any restart of a supervised service); this script
# reports the failure clearly and stops rather than guessing further.
#
# Usage:
#   scripts/deploy-hub.sh [--label LABEL] [--addr HOST:PORT] [--timeout N]
#   scripts/deploy-hub.sh --help
#
#   --label LABEL     Skip launchd auto-discovery and use this label
#                      directly (gui/$(id -u)/LABEL). Required if more than
#                      one serf-hub-like job is registered, or if the job's
#                      label doesn't contain "serf-hub".
#   --addr HOST:PORT  Health-check address. Default: parsed from the
#                      launchd job's recorded launch arguments (-addr), or
#                      127.0.0.1:9180 if that flag wasn't set (matches the
#                      hub's own built-in default).
#   --timeout N        Seconds to poll /api/health for after restart.
#                      Default: 30.
#
# This only manages a job already registered with launchd (see
# docs/serf-hub-remote-operations.md, "Ad hoc macOS background launch"). It
# does not create one — a hub with no launchd job at all has no label to
# `kickstart`, and starting one from scratch is a one-time setup decision
# outside the scope of a routine rebuild-and-restart.
set -uo pipefail

label=""
addr=""
timeout=30

while [ $# -gt 0 ]; do
	case "$1" in
	--label)
		label="${2:-}"
		shift 2
		;;
	--addr)
		addr="${2:-}"
		shift 2
		;;
	--timeout)
		timeout="${2:-}"
		shift 2
		;;
	-h | --help)
		awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"
		exit 0
		;;
	*)
		echo "deploy-hub: unknown argument: $1 (try --help)" >&2
		exit 2
		;;
	esac
done

die() {
	echo "deploy-hub: $*" >&2
	exit 1
}

repo_root=$(git rev-parse --show-toplevel) || die "not inside a git repository"
cd "$repo_root" || die "could not cd to $repo_root"
binary_path="$repo_root/serf-hub"
uid=$(id -u)

echo "== preflight =="

if [ -z "$label" ]; then
	matches=$(launchctl list 2>/dev/null | awk '$3 ~ /serf-hub/ {print $3}')
	count=$(printf '%s\n' "$matches" | grep -c . || true)
	if [ "$count" -eq 0 ]; then
		die "no launchd job matching *serf-hub* found in \`launchctl list\`. If one is registered under a different name, pass --label; if none is registered at all, see docs/serf-hub-remote-operations.md's \"Ad hoc macOS background launch\" to set one up first."
	elif [ "$count" -gt 1 ]; then
		echo "deploy-hub: multiple serf-hub-like launchd jobs found; pass --label to pick one:" >&2
		printf '%s\n' "$matches" >&2
		exit 2
	fi
	label="$matches"
fi

print_out=$(launchctl print "gui/$uid/$label" 2>&1) || die "launchctl print gui/$uid/$label failed (is the label right?):
$print_out"

old_program=$(printf '%s\n' "$print_out" | awk -F' = ' '/^\tprogram = /{print $2; exit}')
old_pid=$(printf '%s\n' "$print_out" | awk -F' = ' '/^\tpid = /{print $2; exit}')
[ -n "$old_program" ] || die "could not find a 'program' field in launchctl print output for gui/$uid/$label"

if [ "$old_program" != "$binary_path" ]; then
	die "job '$label' runs '$old_program', not this worktree's '$binary_path'. Restarting it would deploy the wrong checkout's binary onto a possibly different worktree's hub. Pass --label for the correct job, or confirm you meant to target this one and adjust the path expectations."
fi

if [ -z "$addr" ]; then
	# launchctl print lists a launchctl-submit job's argv one token per line
	# inside an "arguments" block; -addr's value is the line right after it.
	addr=$(printf '%s\n' "$print_out" | awk '
		/-addr$/ { want = 1; next }
		want { gsub(/^[ \t]+|[ \t]+$/, ""); print; exit }
	')
fi
addr="${addr:-127.0.0.1:9180}"

echo "  label:        $label"
echo "  binary:       $binary_path"
echo "  old pid:      ${old_pid:-<not running>}"
echo "  health addr:  $addr"

echo "== build =="

expected_version=$(git rev-parse --short HEAD)
if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
	expected_version="${expected_version}-dirty"
fi

if ! make build-hub; then
	die "build failed; old hub (pid ${old_pid:-<not running>}) left running untouched. Nothing was restarted."
fi

dist_dir="$repo_root/cmd/serf-hub/frontend/dist"
dist_mtime="unknown"
if [ -d "$dist_dir" ]; then
	dist_mtime=$(stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%S' "$dist_dir" 2>/dev/null || date -r "$(stat -c '%Y' "$dist_dir" 2>/dev/null || echo 0)" '+%Y-%m-%dT%H:%M:%S' 2>/dev/null || echo "unknown")
fi
echo "  frontend dist rebuilt: $dist_mtime"
echo "  build target version (git rev-parse --short HEAD, +dirty if applicable): $expected_version"

echo "== restart =="

launchctl kickstart -k "gui/$uid/$label"
kickstart_status=$?
echo "  kickstart exit status: $kickstart_status (informational only — an interrupted kickstart can report failure even when the restart succeeded; /api/health below is the real check)"

echo "== verify =="

health_url="http://$addr/api/health"
deadline=$((SECONDS + timeout))
body=""
while [ "$SECONDS" -lt "$deadline" ]; do
	if body=$(curl -fsS --max-time 3 "$health_url" 2>/dev/null); then
		break
	fi
	body=""
	sleep 1
done

if [ -z "$body" ]; then
	die "$health_url did not answer within ${timeout}s after kickstart. Old pid was ${old_pid:-<not running>}; check whether a new process is up at all (\`launchctl print gui/$uid/$label\`) and inspect its log before retrying."
fi

if command -v jq >/dev/null 2>&1; then
	new_version=$(printf '%s' "$body" | jq -r '.version // empty')
	started_at=$(printf '%s' "$body" | jq -r '.started_at // empty')
else
	new_version=$(printf '%s' "$body" | grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*:"([^"]*)"/\1/')
	started_at=$(printf '%s' "$body" | grep -o '"started_at"[[:space:]]*:[[:space:]]*"[^"]*"' | sed -E 's/.*:"([^"]*)"/\1/')
fi

new_pid=$(launchctl print "gui/$uid/$label" 2>/dev/null | awk -F' = ' '/^\tpid = /{print $2; exit}')

echo "  health probe:  OK ($health_url)"
echo "  started_at:    ${started_at:-<unknown>}"
echo "  old pid:       ${old_pid:-<not running>}"
echo "  new pid:       ${new_pid:-<unknown>}"
echo "  version (build): $expected_version"
echo "  version (running): ${new_version:-<unknown>}"

status=0
if [ -n "$old_pid" ] && [ -n "$new_pid" ] && [ "$old_pid" = "$new_pid" ]; then
	echo "  WARNING: pid did not change; kickstart may not have replaced the process." >&2
	status=1
fi
if [ -n "$new_version" ] && [ "$new_version" != "$expected_version" ]; then
	echo "  WARNING: running version '$new_version' does not match built version '$expected_version'." >&2
	status=1
fi

if [ "$status" -eq 0 ]; then
	echo "deploy-hub: OK — $label restarted onto $expected_version, healthy at $health_url"
else
	echo "deploy-hub: restarted but verification found a mismatch above; investigate before trusting the deploy." >&2
fi
exit "$status"
