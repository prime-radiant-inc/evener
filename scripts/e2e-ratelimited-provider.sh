#!/usr/bin/env bash
# e2e-ratelimited-provider.sh — stand up a disposable evener-hub whose only
# provider is fake429 (test/e2e/fake429), a fake OpenAI-compatible backend
# that answers /v1/models normally and 429s every completion request.
#
# WHY: verifying that evener surfaces a provider rate-limit — the retry
# backoff, the "may be stalled" liveness line, a TUI notification — needs a
# provider that is ACTUALLY throttling, for as long as the check takes. A
# real provider won't reliably do that on demand. This harness existed only
# in Claude session scratchpads and was rebuilt from scratch twice (kata
# 3mn9) before landing here; it live-verified kata 4zn8 and e79v's fixes.
#
# WHAT IT DOES: builds fresh fake429/evener/evener-hub/evener-tui binaries into a
# throwaway run directory, starts fake429 and a HOME-isolated evener-hub (kata
# av1j — a second hub needs its own $HOME or it collides with, or silently
# adopts, your real one) each on a kernel-assigned port (kata 68fm — never a
# hardcoded one), points the hub's providers.toml at fake429, and prints the
# evener-tui command to attach. It does NOT drive evener-tui itself — that needs
# a terminal (tmux) and is the caller's next step.
#
# USAGE:
#   scripts/e2e-ratelimited-provider.sh                  # start; print the
#                                                         # evener-tui command
#   scripts/e2e-ratelimited-provider.sh --retry-after 3  # tune the 429
#                                                         # Retry-After (default 8)
#   scripts/e2e-ratelimited-provider.sh --stop RUN_DIR   # kill fake429 + hub
#                                                         # from a prior run
#                                                         # and remove RUN_DIR
#
# OUTPUT: fake429 and the hub keep running in the background after this
# script exits (like scripts/agent-chrome.sh) — that is the point, so a
# follow-up evener-tui or REST call has something to attach to. Nothing here
# needs, reads, or sets a real provider credential.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixtures_dir="$repo_root/test/e2e/fake429"

retry_after=8
stop_dir=""
while [ $# -gt 0 ]; do
	case "$1" in
	--retry-after)
		retry_after="$2"
		shift 2
		;;
	--stop)
		stop_dir="$2"
		shift 2
		;;
	-h | --help)
		sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "e2e-ratelimited-provider: unknown flag: $1" >&2
		exit 2
		;;
	esac
done

# --stop: kill a previous run's processes (if still alive) and remove its
# run directory. PIDs are read from files this script wrote at start time,
# not re-derived, so --stop works from a fresh invocation with no shared
# shell state.
if [ -n "$stop_dir" ]; then
	# The marker is checked FIRST, before a single file is read or a single
	# signal is sent. --stop takes a path from the caller, and the marker is
	# the one thing that distinguishes "a run of ours" from a typo pointing
	# at something that matters — so a mistyped path that happens to hold a
	# fake429.pid or a hub.pid must lose nothing at all, not just escape the
	# deletion at the end.
	if [ ! -f "$stop_dir/.e2e-ratelimited-provider" ]; then
		echo "e2e-ratelimited-provider: $stop_dir is not one of this script's run directories (no .e2e-ratelimited-provider marker); not touching it" >&2
		exit 2
	fi
	for pidfile in "$stop_dir/fake429.pid" "$stop_dir/hub.pid"; do
		[ -f "$pidfile" ] || continue
		pid="$(cat "$pidfile")"
		if kill -0 "$pid" 2>/dev/null; then
			kill "$pid"
			echo "e2e-ratelimited-provider: stopped $(basename "$pidfile" .pid) (pid $pid)" >&2
		fi
	done
	rm -rf "$stop_dir"
	echo "e2e-ratelimited-provider: removed $stop_dir" >&2
	exit 0
fi

if ! [[ "$retry_after" =~ ^[0-9]+$ ]]; then
	echo "e2e-ratelimited-provider: --retry-after must be a non-negative integer, got '$retry_after'" >&2
	exit 2
fi

# One unique run directory. Binaries, $HOME, fixtures, logs, and PID files
# all live under it, so two concurrent runs (two agents, or two calls of
# this script) cannot collide with each other or with a real hub — the same
# reasoning docs/agentic-testing.md's setup checklist uses.
run="$(mktemp -d -t evener-e2e-fake429.XXXXXX)"
touch "$run/.e2e-ratelimited-provider" # the marker --stop refuses to delete without
echo "e2e-ratelimited-provider: run directory $run" >&2

echo "==> building fake429, evener, evener-hub, evener-tui" >&2
go build -o "$run/fake429" "$repo_root/test/e2e/fake429" || {
	echo "build fake429 failed" >&2
	exit 1
}
go build -o "$run/evener" "$repo_root/cmd/evener" || {
	echo "build evener failed" >&2
	exit 1
}
go build -o "$run/evener-hub" "$repo_root/cmd/evener-hub" || {
	echo "build evener-hub failed" >&2
	exit 1
}
go build -o "$run/evener-tui" "$repo_root/cmd/evener-tui" || {
	echo "build evener-tui failed" >&2
	exit 1
}

# Isolate. A throwaway $HOME keeps auth-token, credentials.toml,
# providers.toml, hub.lock, and session history off the real ~/.evener and
# ~/.local/state/evener entirely (kata av1j: those paths are not overridable
# individually, so the only safe way to run a second hub is a fresh $HOME).
export HOME="$run/home"
mkdir -p "$HOME/.evener"
unset XDG_STATE_HOME

echo "==> starting fake429 (retry-after=${retry_after}s)" >&2
"$run/fake429" 127.0.0.1:0 "$retry_after" >"$run/fake429.log" 2>&1 &
fake429_pid=$!
echo "$fake429_pid" >"$run/fake429.pid"

# Read the real port back from fake429's own startup log line — never a
# fixed port (kata 68fm). Poll rather than sleep a guessed interval.
fake429_port=""
for _ in $(seq 1 50); do
	fake429_port="$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/fake429.log" 2>/dev/null | grep -oE '[0-9]+$')" || true
	[ -n "$fake429_port" ] && break
	kill -0 "$fake429_pid" 2>/dev/null || {
		echo "fake429 exited before it started listening:" >&2
		cat "$run/fake429.log" >&2
		exit 1
	}
	sleep 0.1
done
[ -n "$fake429_port" ] || {
	echo "fake429 never logged a listening port" >&2
	exit 1
}
fake429_addr="127.0.0.1:$fake429_port"
echo "e2e-ratelimited-provider: fake429 up at $fake429_addr (pid $fake429_pid)" >&2

# Wire providers.toml at fake429's real address, into the isolated hub's own
# $HOME/.evener — a copy, not a pointer back into the repo tree, so nothing
# here can leave the checkout dirty even if the hub later rewrites the file.
sed "s|FAKE429_ADDR|$fake429_addr|" "$fixtures_dir/providers.toml" >"$HOME/.evener/providers.toml"

echo "==> starting evener-hub" >&2
"$run/evener-hub" -addr 127.0.0.1:0 -config "$fixtures_dir/hub.toml" -evener "$run/evener" >"$run/hub.log" 2>&1 &
hub_pid=$!
echo "$hub_pid" >"$run/hub.pid"

hub_port=""
for _ in $(seq 1 50); do
	hub_port="$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$')" || true
	[ -n "$hub_port" ] && break
	kill -0 "$hub_pid" 2>/dev/null || {
		echo "hub exited before it started listening:" >&2
		cat "$run/hub.log" >&2
		exit 1
	}
	sleep 0.1
done
[ -n "$hub_port" ] || {
	echo "hub never logged a listening port" >&2
	exit 1
}
hub_addr="http://127.0.0.1:$hub_port"

# Confirm this hub (this PID) is the one that answered $hub_port, not some
# unrelated process already on it.
kill -0 "$hub_pid" || {
	echo "hub failed to start on $hub_port" >&2
	exit 1
}
curl -s -o /dev/null "$hub_addr/" || {
	echo "hub did not answer at $hub_addr" >&2
	exit 1
}
token="$(cat "$HOME/.evener/auth-token")"

echo "e2e-ratelimited-provider: hub up at $hub_addr (pid $hub_pid)" >&2
cat >&2 <<EOF

Ready. Every completion call through this hub will 429 after ${retry_after}s
Retry-After; /v1/models still resolves so launch-check succeeds.

  Attach a TUI:
    "$run/evener-tui" --hub-addr "$hub_addr" --auth-token "$token" --no-auto-start-hub

  Or drive the REST shim directly (see docs/agentic-testing.md):
    curl -H "Authorization: Bearer $token" "$hub_addr/api/..."

  When done:
    $repo_root/scripts/e2e-ratelimited-provider.sh --stop "$run"
EOF
