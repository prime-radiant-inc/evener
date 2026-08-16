#!/usr/bin/env bash
# e2e-webui-turn-controls.sh — stand up a disposable serf-hub with a real web
# UI whose only provider is fakellm (test/e2e/fakellm), a fake
# OpenAI-compatible backend that holds every model round open for a
# configurable interval.
#
# WHY: the web composer's mid-turn controls — Steer, Send (which routes to
# turn/queue while busy), and Stop — only exist while a turn is genuinely in
# flight. Against a real provider that window is seconds wide and needs an
# AGENTS.md pacing prompt to widen (docs/agentic-testing.md). Here it is a
# flag: --hold 30 --rounds 40 keeps one turn "running" for twenty minutes,
# for free, with no credential and no network, and fakellm's log shows
# exactly what reached the model each round — so "the steer never got
# through" is falsifiable rather than a feeling.
#
# WHAT IT DOES: builds fresh fakellm/serf/serf-hub binaries and the SPA into
# a throwaway run directory, starts fakellm and a HOME-isolated serf-hub
# (kata av1j — a second hub needs its own $HOME or it collides with, or
# silently adopts, your real one) each on a kernel-assigned port (kata 68fm —
# never a hardcoded one), points the hub's providers.toml at fakellm, and
# prints the browser auth URL plus a ready-made spawn call.
#
# USAGE:
#   scripts/e2e-webui-turn-controls.sh                # start; print the auth URL
#   scripts/e2e-webui-turn-controls.sh --hold 30      # seconds per model round
#   scripts/e2e-webui-turn-controls.sh --rounds 40    # rounds before the turn ends
#   scripts/e2e-webui-turn-controls.sh --skip-web     # reuse an existing dist
#   scripts/e2e-webui-turn-controls.sh --stop RUN_DIR # kill a prior run, remove RUN_DIR
#
# OUTPUT: fakellm and the hub keep running in the background after this
# script exits — that is the point, so a browser or a follow-up REST call has
# something to attach to. Nothing here needs, reads, or sets a real provider
# credential.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

hold=15
rounds=20
skip_web=0
stop_dir=""
while [ $# -gt 0 ]; do
	case "$1" in
	--hold)
		hold="$2"
		shift 2
		;;
	--rounds)
		rounds="$2"
		shift 2
		;;
	--skip-web)
		skip_web=1
		shift
		;;
	--stop)
		stop_dir="$2"
		shift 2
		;;
	-h | --help)
		sed -n '2,33p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "e2e-webui-turn-controls: unknown flag: $1" >&2
		exit 2
		;;
	esac
done

# --stop: kill a previous run's processes (if still alive) and remove its run
# directory. PIDs are read from files this script wrote at start time, so
# --stop works from a fresh invocation with no shared shell state.
if [ -n "$stop_dir" ]; then
	# Daemons are grandchildren the hub deliberately outlives, so killing the
	# hub alone leaves one serf process per spawned session behind. The hub
	# announces each one's pid in its log; reap those too.
	if [ -f "$stop_dir/hub.log" ]; then
		while read -r pid; do
			[ -n "$pid" ] || continue
			if kill -0 "$pid" 2>/dev/null; then
				kill "$pid"
				echo "e2e-webui-turn-controls: stopped daemon (pid $pid)" >&2
			fi
		done < <(grep -oE 'daemon session=[^ ]+ pid=[0-9]+' "$stop_dir/hub.log" | grep -oE '[0-9]+$')
	fi
	for pidfile in "$stop_dir/fakellm.pid" "$stop_dir/hub.pid"; do
		[ -f "$pidfile" ] || continue
		pid="$(cat "$pidfile")"
		if kill -0 "$pid" 2>/dev/null; then
			kill "$pid"
			echo "e2e-webui-turn-controls: stopped $(basename "$pidfile" .pid) (pid $pid)" >&2
		fi
	done
	# Only ever delete a directory this script made. --stop takes a path from
	# the caller, and the marker file is the one thing that distinguishes "a
	# run of ours" from a typo pointing at something that matters.
	if [ ! -f "$stop_dir/.e2e-webui-turn-controls" ]; then
		echo "e2e-webui-turn-controls: $stop_dir is not one of this script's run directories (no .e2e-webui-turn-controls marker); not deleting it" >&2
		exit 2
	fi
	rm -rf "$stop_dir"
	echo "e2e-webui-turn-controls: removed $stop_dir" >&2
	exit 0
fi

for value in "$hold" "$rounds"; do
	if ! [[ "$value" =~ ^[0-9]+$ ]]; then
		echo "e2e-webui-turn-controls: --hold and --rounds must be non-negative integers, got '$value'" >&2
		exit 2
	fi
done

run="$(mktemp -d -t serf-e2e-webui.XXXXXX)"
touch "$run/.e2e-webui-turn-controls" # the marker --stop refuses to delete without
echo "e2e-webui-turn-controls: run directory $run" >&2

if [ "$skip_web" -eq 0 ]; then
	echo "==> building the SPA (make build-web)" >&2
	make -C "$repo_root" build-web >"$run/build-web.log" 2>&1 || {
		echo "build-web failed; see $run/build-web.log" >&2
		tail -30 "$run/build-web.log" >&2
		exit 1
	}
fi

echo "==> building fakellm, serf, serf-hub" >&2
# From the repo root: `go build` resolves the module (and go.work) from its own
# working directory, so an absolute package path is not enough when the script
# is invoked from outside the checkout.
cd "$repo_root"
go build -o "$run/fakellm" "$repo_root/test/e2e/fakellm/cmd" || {
	echo "build fakellm failed" >&2
	exit 1
}
go build -o "$run/serf" "$repo_root/cmd/serf" || {
	echo "build serf failed" >&2
	exit 1
}
go build -o "$run/serf-hub" "$repo_root/cmd/serf-hub" || {
	echo "build serf-hub failed" >&2
	exit 1
}

# Isolate. A throwaway $HOME keeps auth-token, credentials.toml,
# providers.toml, hub.lock, and session history off the real ~/.serf and
# ~/.local/state/serf entirely (kata av1j).
export HOME="$run/home"
mkdir -p "$HOME/.serf"
# Everything that can redirect serf away from the throwaway $HOME, not just
# the state dir: an operator with any of these exported would otherwise have
# this hub read or write their real config, cache, run dir or hub token while
# the header above promises isolation.
unset XDG_STATE_HOME XDG_CONFIG_HOME XDG_CACHE_HOME
unset SERF_STATE_DIR SERF_RUN_DIR SERF_HUB_TOKEN SERF_HUB_ADDR SERF_HUB_SPAWNED

workspace="$run/workspace"
mkdir -p "$workspace"
echo "notes for the fake tool round" >"$workspace/NOTES.md"

echo "==> starting fakellm (hold=${hold}s rounds=${rounds})" >&2
# Flags BEFORE the positional address: Go's flag package stops parsing at the
# first non-flag argument, so the other order silently ran with the defaults
# while this script's banner reported the values it had asked for.
"$run/fakellm" --hold "${hold}s" --rounds "$rounds" 127.0.0.1:0 >"$run/fakellm.log" 2>&1 &
fakellm_pid=$!
echo "$fakellm_pid" >"$run/fakellm.pid"

# Read the real port back from fakellm's own startup log line — never a fixed
# port (kata 68fm). Poll rather than sleep a guessed interval.
fakellm_port=""
for _ in $(seq 1 50); do
	fakellm_port="$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/fakellm.log" 2>/dev/null | grep -oE '[0-9]+$')" || true
	[ -n "$fakellm_port" ] && break
	kill -0 "$fakellm_pid" 2>/dev/null || {
		echo "fakellm exited before it started listening:" >&2
		cat "$run/fakellm.log" >&2
		exit 1
	}
	sleep 0.1
done
[ -n "$fakellm_port" ] || {
	echo "fakellm never logged a listening port" >&2
	exit 1
}
echo "e2e-webui-turn-controls: fakellm up at 127.0.0.1:$fakellm_port (pid $fakellm_pid)" >&2

cat >"$HOME/.serf/providers.toml" <<EOF
schema = 1
default = "fake"

[instances.fake]
type = "openai"
api_style = "chat-completions"
base_url = "http://127.0.0.1:$fakellm_port/v1"
api_key = "fakellm-not-a-secret"
EOF

echo "==> starting serf-hub" >&2
"$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" >"$run/hub.log" 2>&1 &
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
curl -s -o /dev/null "$hub_addr/" || {
	echo "hub did not answer at $hub_addr" >&2
	exit 1
}
token="$(cat "$HOME/.serf/auth-token")"

cat >&2 <<EOF

Ready. Every model round through this hub pauses ${hold}s; a turn runs
${rounds} rounds before it ends, so a session stays "running" for roughly
$((hold * rounds))s.

  Open the UI (visit once per browser, then navigate freely):
    $hub_addr/auth?token=$token

  Spawn a session that will sit in a long turn:
    curl -s -X POST -H "Content-Type: application/json" \\
      -H "Authorization: Bearer $token" \\
      -d '{"prompt":"read NOTES.md and keep working","model":"fake/fake-test-model","working_dir":"$workspace","harness":"serf","branch":"","access_mode":"full","agent":"default","launch_overrides":{}}' \\
      $hub_addr/api/spawn

  Watch what actually reached the model each round:
    tail -f $run/fakellm.log

  Watch the hub's own view of the RPCs:
    tail -f $run/hub.log

  When done:
    $repo_root/scripts/e2e-webui-turn-controls.sh --stop "$run"
EOF
