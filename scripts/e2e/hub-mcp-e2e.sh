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
# PM through the full loop: hub_overview, list_models, start_session,
# wait_for_activity, send_message (auto-steer mid-turn), read_transcript,
# get_session, interrupt_session, rename_session, list_sessions,
# search_sessions, list_tasks, stop_session, and the honest quiet-timeout —
# then read-only and project-scoped server instances prove the config gates.
# The mcp.json it wires in carries no token: the server's token-file default
# reads the hub's own auth-token under the isolated HOME (the zero-config
# path the docs recommend), so the one config a session's model can read
# holds no secret material: the server reads its token from the hub's own
# auth-token file, and reads EVENER_HUB_MCP_TOKEN — not EVENER_HUB_TOKEN,
# which hub-spawned daemons inherit holding the hub's internal spawner
# token, a bearer /rpc refuses. On success it tears
# the whole stack down; on failure the run directory stays for diagnosis and
# a reaper kills the processes it started.
#
# USAGE:
#   scripts/e2e/hub-mcp-e2e.sh           # run the workflow
#   scripts/e2e/hub-mcp-e2e.sh --keep     # keep the stack up for manual play
#   scripts/e2e/hub-mcp-e2e.sh --stop RUN_DIR
# END-USAGE (--help prints everything above this line)
set -euo pipefail

SCRIPT_NAME="hub-mcp-e2e"
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/e2e-lib.sh"
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/scratch-lib.sh"
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

# scratch_dir mints the run directory the way the repo's audit requires: a
# recursive delete may never take a variable a caller can clobber, so the
# teardown reclaims it with the no-argument scratch_rm instead of rm -rf.
# The marker file matches what e2e_make_run_dir stamped, so --stop and the
# reaper keep validating the same run directory.
scratch_dir run "evener-e2e-hub-mcp"
touch "$run/.$SCRIPT_NAME"
e2e_setup_reaper "$run" "$SCRIPT_NAME" "$repo_root"

cd "$repo_root"
echo "==> building evener and fakellm" >&2
e2e_build_binary fakellm "$run/fakellm" "$repo_root/test/e2e/fakellm/cmd"
e2e_build_binary evener "$run/evener" "$repo_root/cmd/evener"

e2e_isolate_home "$run"

workspace="$run/workspace"
mkdir -p "$workspace"
echo "notes for the fake tool round: the parser mentions TODO lexer cleanup" >"$workspace/NOTES.md"

echo "==> writing the PM session script fakellm will follow" >&2
# The scripted session is the PM: a real evener session, spawned with the
# hub MCP in its mcp.json, whose model rounds call real MCP tools through a
# real daemon. The marker identifies it by prompt text; every other session
# (the workers it starts, the workers this driver starts) keeps fakellm's
# default behaviour.
cat >"$run/pm-script.json" <<EOF
{
	"marker": "PM-SCRIPTED-DRIVER",
	"rounds": [
		{"tool": "hub__hub_overview", "args": {}},
		{"tool": "hub__start_session", "args": {"cwd": "$workspace", "prompt": "Read NOTES.md and index every parser mention.", "name": "scripted-worker", "model": "fake/fake-test-model"}},
		{"text": "The worker is delegated and running. Ending my turn as the scripted PM."}
	]
}
EOF

echo "==> starting fakellm (hold 1s, 3 rounds, PM script)" >&2
"$run/fakellm" --hold 1s --rounds 3 --script "$run/pm-script.json" 127.0.0.1:0 >"$run/fakellm.log" 2>&1 &
echo $! >"$run/fakellm.pid"
e2e_wait_for_port "$run/fakellm.log" "$(cat "$run/fakellm.pid")" fakellm
fakellm_port="$e2e_port"

cat >"$HOME/.config/evener/providers.toml" <<EOF
schema = 2
default = "fake"

[providers.fake]
base = "openai"
protocol = "openai-chat"
base_url = "http://127.0.0.1:$fakellm_port/v1"
api_key = "fakellm-not-a-secret"
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
mcp_entry="$repo_root/hub-mcp/dist/src/index.js"

echo "==> wiring the hub MCP into the isolated home's global mcp.json" >&2
# The global layer is the trusted one (mcpconfig's own split): the sessions
# this hub spawns get the hub MCP exactly the way the PM session kind will.
# No token in this file, on purpose: the server's token-file default reads the
# hub's own auth-token under this isolated HOME — the zero-config path the
# docs recommend — and the env a daemon merges over its own carries HOME down
# to the server process. The server reads EVENER_HUB_MCP_TOKEN, which nothing
# in a hub-spawned session sets: the hub injects its internal spawner token
# into every spawned daemon's env under the older EVENER_HUB_TOKEN name (a
# bearer for the hub→daemon dials, not for this hub's /rpc), and reading
# that name is exactly what would break the zero-config path. The guard
# below keeps the one config a session's model can read free of secret
# material.
mkdir -p "$HOME/.config/evener"
cat >"$HOME/.config/evener/mcp.json" <<EOF
{
	"mcpServers": {
		"hub": {
			"type": "stdio",
			"command": "node",
			"args": ["$mcp_entry"],
			"env": {
				"EVENER_HUB_RPC_URL": "ws://127.0.0.1:$hub_port/rpc"
			}
		}
	}
}
EOF
if grep -qF "$token" "$HOME/.config/evener/mcp.json"; then
	echo "$SCRIPT_NAME: mcp.json must not contain the hub token" >&2
	exit 1
fi

echo "==> running the PM workflow through the MCP" >&2
EVENER_E2E_RPC="ws://127.0.0.1:$hub_port/rpc" \
EVENER_E2E_TOKEN="$token" \
EVENER_E2E_WORKSPACE="$workspace" \
EVENER_E2E_MCP="$repo_root/hub-mcp/dist/src/index.js" \
	node "$repo_root/hub-mcp/scripts/e2e/pm-workflow.mjs"

if [ "$keep" -eq 1 ]; then
	e2e_disarm_reaper
	# The auth URL embeds the hub's full bearer token, so the URL itself is
	# a secret (docs/evener-hub.md treats it as one): print it redacted and
	# point at the file the token lives in instead of pasting the real URL.
	cat >&2 <<EOF

stack kept running for manual play:
  hub RPC:      ws://127.0.0.1:$hub_port/rpc
  hub auth URL: http://127.0.0.1:$hub_port/auth/<token redacted> — the full URL
                is a secret; its token lives in $HOME/.local/state/evener/auth-token
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
scratch_rm
echo "HUB MCP E2E PASSED" >&2
