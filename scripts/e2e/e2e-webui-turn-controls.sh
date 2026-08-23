#!/usr/bin/env bash
# e2e-webui-turn-controls.sh — stand up a disposable evener hub with a real web
# UI whose only provider is fakellm (test/e2e/fakellm), a fake
# OpenAI-compatible backend that holds every model round open for a
# configurable interval.
#
# WHY: the web composer's mid-turn controls — Steer, Send (which routes to
# turn/queue while busy), and Stop — only exist while a turn is genuinely in
# flight. Against a real provider that window is seconds wide and needs an
# AGENTS.md pacing prompt to widen (docs/developing-evener/agentic-testing.md). Here it is a
# flag: --hold 30 --rounds 40 keeps one turn "running" for twenty minutes,
# for free, with no credential and no network, and fakellm's log shows
# exactly what reached the model each round — so "the steer never got
# through" is falsifiable rather than a feeling.
#
# WHAT IT DOES: builds fresh fakellm/evener binaries and the SPA into
# a throwaway run directory, starts fakellm and a HOME-isolated evener hub
# (kata av1j — a second hub needs its own $HOME or it collides with, or
# silently adopts, your real one) each on a kernel-assigned port (kata 68fm —
# never a hardcoded one), points the hub's providers.toml at fakellm, and
# prints the browser auth URL plus a ready-made spawn call.
#
# USAGE:
#   scripts/e2e/e2e-webui-turn-controls.sh                # start; print the auth URL
#   scripts/e2e/e2e-webui-turn-controls.sh --hold 30      # seconds per model round
#   scripts/e2e/e2e-webui-turn-controls.sh --rounds 40    # rounds per turn, per session
#   scripts/e2e/e2e-webui-turn-controls.sh --background-job  # every session spawned
#                                                       # goes idle holding a
#                                                       # background job; touch
#                                                       # $run/release-the-job to
#                                                       # wake them with a
#                                                       # notification turn
#   scripts/e2e/e2e-webui-turn-controls.sh --skip-web     # reuse an existing dist
#   scripts/e2e/e2e-webui-turn-controls.sh --stop RUN_DIR # kill a prior run, remove RUN_DIR
#
# OUTPUT: fakellm and the hub keep running in the background after this
# script exits — that is the point, so a browser or a follow-up REST call has
# something to attach to. Nothing here needs, reads, or sets a real provider
# credential.
# END-USAGE (--help prints everything above this line)
set -euo pipefail

SCRIPT_NAME="e2e-webui-turn-controls"
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../lib" && pwd)/e2e-lib.sh"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

hold=15
rounds=20
background_job=0
skip_web=0
stop_dir=""

while [ $# -gt 0 ]; do
	case "$1" in
	--hold)
		e2e_need_value --hold $#
		hold="$2"
		shift 2
		;;
	--rounds)
		e2e_need_value --rounds $#
		rounds="$2"
		shift 2
		;;
	--background-job)
		background_job=1
		shift
		;;
	--skip-web)
		skip_web=1
		shift
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
		# Print the header comment up to its sentinel, never a line count: a
		# range ending at a fixed number silently drops whatever the header
		# grows past it, which is how --stop — the only documented teardown —
		# went unprinted.
		sed -n '2,/^# END-USAGE/p' "${BASH_SOURCE[0]}" | grep -v '^# END-USAGE' | sed 's/^# \{0,1\}//'
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
	e2e_stop_run "$stop_dir" ".e2e-webui-turn-controls" \
		"$stop_dir/fakellm.pid" "$stop_dir/hub.pid"
fi

for value in "$hold" "$rounds"; do
	if ! [[ "$value" =~ ^[0-9]+$ ]]; then
		echo "$SCRIPT_NAME: --hold and --rounds must be non-negative integers, got '$value'" >&2
		exit 2
	fi
done

e2e_make_run_dir "evener-e2e-webui" ".e2e-webui-turn-controls"
e2e_setup_reaper "$run" "$SCRIPT_NAME" "$repo_root"

if [ "$skip_web" -eq 0 ]; then
	echo "==> building the SPA (make build-web)" >&2
	make -C "$repo_root" build-web >"$run/build-web.log" 2>&1 || {
		echo "build-web failed; see $run/build-web.log" >&2
		tail -30 "$run/build-web.log" >&2
		exit 1
	}
fi

# From the repo root: `go build` resolves the module (and go.work) from its own
# working directory, so an absolute package path is not enough when the script
# is invoked from outside the checkout.
cd "$repo_root"
e2e_build_binary fakellm "$run/fakellm" "$repo_root/test/e2e/fakellm/cmd"
e2e_build_binary evener "$run/evener" "$repo_root/cmd/evener"

e2e_isolate_home "$run"

workspace="$run/workspace"
mkdir -p "$workspace"
echo "notes for the fake tool round" >"$workspace/NOTES.md"

echo "==> starting fakellm (hold=${hold}s rounds=${rounds})" >&2
# Flags BEFORE the positional address: Go's flag package stops parsing at the
# first non-flag argument, so the other order silently ran with the defaults
# while this script's banner reported the values it had asked for.
job_release="$workspace/release-the-job"
fakellm_args=(--hold "${hold}s" --rounds "$rounds")
if [ "$background_job" -eq 1 ]; then
	# A bare name: the shell tool runs in the session's working directory,
	# so nothing needs quoting and a run path with a space cannot break it.
	fakellm_args+=(--background-job-until release-the-job)
fi
"$run/fakellm" "${fakellm_args[@]}" 127.0.0.1:0 >"$run/fakellm.log" 2>&1 &
fakellm_pid=$!
echo "$fakellm_pid" >"$run/fakellm.pid"

e2e_wait_for_port "$run/fakellm.log" "$fakellm_pid" fakellm
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

echo "==> starting evener hub" >&2
"$run/evener" hub -addr 127.0.0.1:0 -evener "$run/evener" >"$run/hub.log" 2>&1 &
hub_pid=$!
echo "$hub_pid" >"$run/hub.pid"

e2e_wait_for_port "$run/hub.log" "$hub_pid" hub
hub_port="$e2e_port"
hub_addr="http://127.0.0.1:$hub_port"
e2e_health_check "$hub_addr"
token="$(cat "$HOME/.local/state/evener/auth-token")"

# Ready: the stack is up, so the processes stay up too. Disarm the reaper.
e2e_disarm_reaper

if [ "$background_job" -eq 1 ]; then
	mode_line="Every session you spawn launches a background job on its first
round and goes idle holding it. Release the jobs to wake each session with a
job-completion notification turn, which ends after one ${hold}s round:

    touch '$job_release'
"
else
	mode_line="Every model round through this hub pauses ${hold}s, and every
turn of every session ends after ${rounds} of its own rounds — so each session
you spawn stays \"running\" for roughly $((hold * rounds))s per turn, however
many of them there are.
"
fi

cat >&2 <<EOF

Ready. $mode_line

  Open the UI (visit once per browser, then navigate freely):
    $hub_addr/auth?token=$token

  Spawn a session that will sit in a long turn:
    curl -s -X POST -H "Content-Type: application/json" \\
      -H "Authorization: Bearer $token" \\
      -d '{"prompt":"read NOTES.md and keep working","model":"fake/fake-test-model","working_dir":"$workspace","harness":"evener","branch":"","access_mode":"full","agent":"default","launch_overrides":{}}' \\
      $hub_addr/api/spawn

  Watch what actually reached the model each round:
    tail -f $run/fakellm.log

  Sessions run at once and their rounds interleave. Each round line names its
  session -- "session <tool-call-id> round N" -- so to follow just one, take
  the name off its first round line and grep for it. Use -w: the ids are
  numbered, so a bare call_fakellm_1 also matches call_fakellm_10 and up,
  putting two sessions back in one view.
    grep -w call_fakellm_1 $run/fakellm.log

  Watch the hub's own view of the RPCs:
    tail -f $run/hub.log

  When done:
    $repo_root/scripts/e2e/$SCRIPT_NAME.sh --stop "$run"
EOF
