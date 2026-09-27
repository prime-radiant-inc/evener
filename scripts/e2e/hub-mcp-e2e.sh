#!/usr/bin/env bash
# hub-mcp-e2e.sh — prove the hub MCP by using it the way the PM session will.
#
# WHY: the MCP's whole job is to let an agent supervise other agents — list
# them, spawn them, watch their turns, steer them, read their transcripts,
# stop them. Unit tests cover the tool logic against a scripted hub port;
# this run covers the rest: the MCP protocol over stdio (a raw JSON-RPC
# client, no SDK on the client side, so the interop is honest), a real
# AppWire connection with the real handshake and Bearer auth, real hub relay
# and subscriptions, and real daemon sessions running real turns — driven by
# fakellm (test/e2e/fakellm), so no credential, no network, no wall-clock
# guesswork.
#
# WHAT IT DOES: builds fresh evener and fakellm binaries into a throwaway
# run directory, starts fakellm and a HOME-isolated evener hub on
# kernel-assigned ports, points the hub's providers.toml at fakellm, builds
# the hub-mcp package (its own preflight repairs the SDK dist if needed),
# then runs hub-mcp/scripts/e2e/pm-workflow.mjs — one MCP client playing the
# PM through the full loop: hub_overview, start_session, wait_for_activity,
# send_message (auto-steer mid-turn), read_transcript, get_session,
# interrupt_session, list_sessions, search_sessions, list_tasks,
# stop_session, and the honest quiet-timeout. On success it tears the whole
# stack down; on failure the run directory stays for diagnosis and a reaper
# kills the processes it started.
#
# USAGE:
#   scripts/e2e/hub-mcp-e2e.sh           # run the workflow
#   scripts/e2e/hub-mcp-e2e.sh --keep     # keep the stack up for manual play
#   scripts/e2e/hub-mcp-e2e.sh --stop RUN_DIR
# END-USAGE (--help prints everything above this line)
set -euo pipefail

SCRIPT_NAME="hub-mcp-e2e"
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/e2e-lib.sh"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

keep=0
stop_dir=""
while [ $# -gt 0 ]; do
	case "$1" in
	--keep)
		keep=1
		shift
		;;
	--stop)
		e2e_need_value --stop $#
		stop_dir="$2"
		shift 2
		;;
	*)
		echo "$SCRIPT_NAME: unknown flag: $1" >&2
		exit 2
		;;
	esac
done

if [ -n "$stop_dir" ]; then
	e2e_stop_run "$stop_dir" ".$SCRIPT_NAME" \
		"$stop_dir/fakellm.pid" "$stop_dir/hub.pid"
fi

e2e_make_run_dir "evener-e2e-hub-mcp" ".$SCRIPT_NAME"
e2e_setup_reaper "$run" "$SCRIPT_NAME" "$repo_root"

cd "$repo_root"
echo "==> building evener and fakellm" >&2
e2e_build_binary fakellm "$run/fakellm" "$repo_root/test/e2e/fakellm/cmd"
e2e_build_binary evener "$run/evener" "$repo_root/cmd/evener"

e2e_isolate_home "$run"

workspace="$run/workspace"
mkdir -p "$workspace"
echo "notes for the fake tool round: the parser mentions TODO lexer cleanup" >"$workspace/NOTES.md"

echo "==> starting fakellm (hold 1s, 3 rounds per turn)" >&2
"$run/fakellm" --hold 1s --rounds 3 127.0.0.1:0 >"$run/fakellm.log" 2>&1 &
echo $! >"$run/fakellm.pid"
e2e_wait_for_port "$run/fakellm.log" "$(cat "$run/fakellm.pid")" fakellm
fakellm_port="$e2e_port"

cat >"$HOME/.config/evener/providers.toml" <<EOF
schema = 1
default = "fake"

[instances.fake]
type = "openai"
api_style = "chat-completions"
base_url = "http://127.0.0.1:$fakellm_port/v1"
api_key = "fakellm-not-a-secret"
send_session_affinity_headers = true
EOF

echo "==> starting the hub" >&2
"$run/evener" hub -addr 127.0.0.1:0 -evener "$run/evener" >"$run/hub.log" 2>&1 &
echo $! >"$run/hub.pid"
e2e_wait_for_port "$run/hub.log" "$(cat "$run/hub.pid")" hub
hub_port="$e2e_port"
e2e_health_check "http://127.0.0.1:$hub_port"
token="$(cat "$HOME/.local/state/evener/auth-token")"

echo "==> building the hub MCP package" >&2
cd "$repo_root/hub-mcp"
npm run preflight
npm run build

echo "==> running the PM workflow through the MCP" >&2
EVENER_E2E_RPC="ws://127.0.0.1:$hub_port/rpc" \
EVENER_E2E_TOKEN="$token" \
EVENER_E2E_WORKSPACE="$workspace" \
EVENER_E2E_MCP="$repo_root/hub-mcp/dist/src/index.js" \
	node "$repo_root/hub-mcp/scripts/e2e/pm-workflow.mjs"

if [ "$keep" -eq 1 ]; then
	e2e_disarm_reaper
	cat >&2 <<EOF

stack kept running for manual play:
  hub RPC:      ws://127.0.0.1:$hub_port/rpc
  hub auth URL: http://127.0.0.1:$hub_port/auth/$token
  fakellm log:  $run/fakellm.log
  hub log:      $run/hub.log
  stop with:    scripts/e2e/hub-mcp-e2e.sh --stop "$run"
EOF
	exit 0
fi

echo "==> tearing down" >&2
e2e_stop_owned_pid "$(cat "$run/hub.pid")" hub "$(basename "$run")"
e2e_stop_owned_pid "$(cat "$run/fakellm.pid")" fakellm "$(basename "$run")"
e2e_disarm_reaper
rm -rf "$run"
echo "HUB MCP E2E PASSED" >&2
