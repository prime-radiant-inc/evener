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

SCRIPT_NAME="e2e-ratelimited-provider"
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/e2e-lib.sh"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixtures_dir="$repo_root/test/e2e/fake429"

retry_after=8
stop_dir=""

while [ $# -gt 0 ]; do
	case "$1" in
	--retry-after)
		e2e_need_value --retry-after $#
		retry_after="$2"
		shift 2
		;;
	--stop)
		e2e_need_value --stop $#
		stop_dir="$2"
		# An empty path is how `--stop "$dir"` fails when $dir was never set.
		# Falling through to the start path would have a teardown command
		# build binaries and stand up a stack, which is the opposite of the
		# one thing it was asked to do.
		if [ -z "$stop_dir" ]; then
			echo "$SCRIPT_NAME: --stop needs a run directory, got an empty path" >&2
			exit 2
		fi
		shift 2
		;;
	-h | --help)
		sed -n '2,29p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "$SCRIPT_NAME: unknown flag: $1" >&2
		exit 2
		;;
	esac
done

# --stop: kill a previous run's processes (if still alive) and remove its run
# directory. PIDs are read from files this script wrote at start time, so
# --stop works from a fresh invocation with no shared shell state.
if [ -n "$stop_dir" ]; then
	e2e_stop_run "$stop_dir" ".e2e-ratelimited-provider" \
		"$stop_dir/fake429.pid" "$stop_dir/hub.pid"
fi

if ! [[ "$retry_after" =~ ^[0-9]+$ ]]; then
	echo "$SCRIPT_NAME: --retry-after must be a non-negative integer, got '$retry_after'" >&2
	exit 2
fi

# One unique run directory. Binaries, $HOME, fixtures, logs, and PID files
# all live under it, so two concurrent runs (two agents, or two calls of
# this script) cannot collide with each other or with a real hub — the same
# reasoning docs/agentic-testing.md's setup checklist uses.
e2e_make_run_dir "evener-e2e-fake429" ".e2e-ratelimited-provider"
e2e_setup_reaper "$run" "$SCRIPT_NAME" "$repo_root"

# From the repo root: `go build` resolves the module (and go.work) from its
# own working directory, so an absolute package path is not enough when the
# script is invoked from outside the checkout.
cd "$repo_root"
e2e_build_binary fake429 "$run/fake429" "$repo_root/test/e2e/fake429"
e2e_build_binary evener "$run/evener" "$repo_root/cmd/evener"
e2e_build_binary evener-hub "$run/evener-hub" "$repo_root/cmd/evener-hub"
e2e_build_binary evener-tui "$run/evener-tui" "$repo_root/cmd/evener-tui"

e2e_isolate_home "$run"

echo "==> starting fake429 (retry-after=${retry_after}s)" >&2
"$run/fake429" 127.0.0.1:0 "$retry_after" >"$run/fake429.log" 2>&1 &
fake429_pid=$!
echo "$fake429_pid" >"$run/fake429.pid"

e2e_wait_for_port "$run/fake429.log" "$fake429_pid" fake429
fake429_addr="127.0.0.1:$e2e_port"

# Wire providers.toml at fake429's real address, into the isolated hub's own
# $HOME/.evener — a copy, not a pointer back into the repo tree, so nothing
# here can leave the checkout dirty even if the hub later rewrites the file.
sed "s|FAKE429_ADDR|$fake429_addr|" "$fixtures_dir/providers.toml" >"$HOME/.evener/providers.toml"

echo "==> starting evener-hub" >&2
"$run/evener-hub" -addr 127.0.0.1:0 -config "$fixtures_dir/hub.toml" -evener "$run/evener" >"$run/hub.log" 2>&1 &
hub_pid=$!
echo "$hub_pid" >"$run/hub.pid"

e2e_wait_for_port "$run/hub.log" "$hub_pid" hub
hub_port="$e2e_port"
hub_addr="http://127.0.0.1:$hub_port"
e2e_health_check "$hub_addr"
token="$(cat "$HOME/.evener/auth-token")"

echo "$SCRIPT_NAME: hub up at $hub_addr (pid $hub_pid)" >&2

# Ready: the stack is up, so the processes stay up too. Disarm the reaper.
e2e_disarm_reaper

cat >&2 <<EOF

Ready. Every completion call through this hub will 429 after ${retry_after}s
Retry-After; /v1/models still resolves so launch-check succeeds.

  Attach a TUI:
    "$run/evener-tui" --hub-addr "$hub_addr" --auth-token "$token" --no-auto-start-hub

  Or drive the REST shim directly (see docs/agentic-testing.md):
    curl -H "Authorization: Bearer $token" "$hub_addr/api/..."

  When done:
    $repo_root/scripts/e2e/$SCRIPT_NAME.sh --stop "$run"
EOF
