#!/usr/bin/env bash
# e2e-lib.sh — shared harness logic for the disposable evener-hub e2e scripts
# (scripts/e2e-webui-turn-controls.sh, scripts/e2e-ratelimited-provider.sh).
#
# This file is SOURCED, never executed. It defines functions only; the caller
# sets `SCRIPT_NAME` (used in every diagnostic message so each script's errors
# name themselves) and `repo_root` before calling into the harness, and sources
# this file after its own `set -euo pipefail`.
#
# The two harnesses are near-duplicates that stand up a disposable evener-hub
# plus a fake backend on kernel-assigned ports. The webui harness matured first
# and fixed several bugs the ratelimited harness still carried; centralising
# the logic here is what keeps the second copy from drifting again.

# e2e_need_value FLAG ARGC — a flag that takes a value must have one.
# Without this, `set -u` aborts on "$2" with a raw "line NN: $2: unbound
# variable", which tells the operator nothing about which flag they left
# dangling. ARGC is the caller's remaining `$#` at the point of the call.
e2e_need_value() {
	if [ "$2" -lt 2 ]; then
		echo "$SCRIPT_NAME: $1 needs a value" >&2
		exit 2
	fi
}

# e2e_stop_owned_pid PID LABEL TAG — signal PID, but only while it is still
# one of this run's own processes.
#
# `kill -0` answers "is anything alive at this pid", which is the wrong
# question by the time --stop is typed. hub.log accumulates one
# `daemon session=<id> pid=<n>` line per session for the whole life of a run
# this script advertises as lasting tens of minutes, and a pid belonging to a
# session that finished an hour ago may since have been recycled by anything
# on the machine. Same for the backend .pid and hub.pid after a crash.
#
# The run directory's name is the ownership proof: mktemp made it unique, and
# every process this script starts carries it in argv — the backend and the
# hub because they are exec'd from it, and each daemon because the hub execs
# the `-evener` binary out of it. Matching the basename rather than the path
# keeps it working where the two differ (macOS hands out /var/... and reports
# /private/var/...).
e2e_stop_owned_pid() {
	local pid="$1"
	local label="$2"
	local tag="$3"
	kill -0 "$pid" 2>/dev/null || return 0
	if ! ps -o args= -p "$pid" 2>/dev/null | grep -qF -- "$tag"; then
		echo "$SCRIPT_NAME: pid $pid is alive but is not this run's $label (recycled pid); leaving it alone" >&2
		return 0
	fi
	kill "$pid"
	echo "$SCRIPT_NAME: stopped $label (pid $pid)" >&2
}

# e2e_stop_run STOP_DIR MARKER PIDFILES... — the --stop handler.
#
# Checks for `$STOP_DIR/$MARKER` first (exits 2 if missing: the marker is the
# one thing that distinguishes "a run of ours" from a typo pointing at
# something that matters, so a mistyped path that happens to hold a .pid or a
# hub.log must lose nothing at all, not just escape the deletion at the end).
# Then reads daemon pids from hub.log if present (the hub deliberately
# outlives its spawned daemons, so killing the hub alone leaves one evener
# process per session behind; the hub announces each one's pid in its log).
# Then loops over PIDFILES, calling e2e_stop_owned_pid for each. Then removes
# the run directory. Exits 0.
e2e_stop_run() {
	local stop_dir="$1"
	local marker="$2"
	shift 2
	if [ ! -f "$stop_dir/$marker" ]; then
		echo "$SCRIPT_NAME: $stop_dir is not one of this script's run directories (no $marker marker); not touching it" >&2
		exit 2
	fi
	if [ -f "$stop_dir/hub.log" ]; then
		while read -r pid; do
			[ -n "$pid" ] || continue
			e2e_stop_owned_pid "$pid" daemon "$(basename "$stop_dir")"
		done < <(grep -oE 'daemon session=[^ ]+ pid=[0-9]+' "$stop_dir/hub.log" | grep -oE '[0-9]+$')
	fi
	for pidfile in "$@"; do
		[ -f "$pidfile" ] || continue
		e2e_stop_owned_pid "$(cat "$pidfile")" "$(basename "$pidfile" .pid)" "$(basename "$stop_dir")"
	done
	rm -rf "$stop_dir"
	echo "$SCRIPT_NAME: removed $stop_dir" >&2
	exit 0
}

# e2e_make_run_dir PREFIX MARKER — create a unique run directory and mark it.
#
# An explicit template, not `mktemp -t`: macOS's mktemp ignores TMPDIR for -t
# and uses the Darwin per-user temp directory instead, which puts the run
# directory outside the dev-tooling wave's per-suite TMPDIR isolation — and
# so outside the leftover check that is supposed to catch exactly this.
# Touches `$run/$MARKER` (the marker --stop refuses to delete without), prints
# the run dir path, and sets the global `run` for the caller.
e2e_make_run_dir() {
	local prefix="$1"
	local marker="$2"
	local tmpbase=${TMPDIR:-/tmp}
	if ! run="$(mktemp -d "${tmpbase%/}/${prefix}.XXXXXX")"; then
		echo "$SCRIPT_NAME: cannot create a run directory under ${tmpbase%/}" >&2
		exit 1
	fi
	touch "$run/$marker"
	echo "$SCRIPT_NAME: run directory $run" >&2
}

# e2e_setup_reaper RUN_DIR SCRIPT_NAME REPO_ROOT — arm the failure reaper.
#
# Nothing below here may leave a half-built stack behind. Every failure
# between this line and the "Ready" banner (disarmed by e2e_disarm_reaper) is
# a bare exit, and the operator who just read "build evener-hub failed" is the
# least likely person to go looking for a backend still running or a run
# directory still on disk. The directory itself is deliberately NOT removed —
# its logs are the only record of what failed — so the reaper prints the one
# command that removes it. Armed here rather than at the first background
# process so a failed build, which starts nothing but still leaves a
# directory, says the same thing.
#
# The trap kills any *.pid file in RUN_DIR (whichever of the backend/hub have
# been started by the time the failure hits) and prints the --stop command.
# The three arguments are stashed in globals because a trap fires in a scope
# where this function's locals no longer exist.
e2e_setup_reaper() {
	e2e_reaper_run_dir="$1"
	e2e_reaper_script_name="$2"
	e2e_reaper_repo_root="$3"
	started_ready=0
	on_failed_start() {
		[ "$started_ready" -eq 1 ] && return 0
		started_ready=1 # never run twice: INT runs this, then so does EXIT
		for pidfile in "$e2e_reaper_run_dir"/*.pid; do
			[ -f "$pidfile" ] || continue
			e2e_stop_owned_pid "$(cat "$pidfile")" "$(basename "$pidfile" .pid)" "$(basename "$e2e_reaper_run_dir")"
		done
		echo "$e2e_reaper_script_name: startup failed; its logs are in $e2e_reaper_run_dir. Remove it with" >&2
		echo "    $e2e_reaper_repo_root/scripts/e2e/$e2e_reaper_script_name.sh --stop \"$e2e_reaper_run_dir\"" >&2
	}
	trap on_failed_start EXIT
	trap 'on_failed_start; exit 130' INT TERM
}

# e2e_disarm_reaper — the stack is up, so the processes stay up too.
e2e_disarm_reaper() {
	started_ready=1
	trap - EXIT INT TERM
}

# e2e_isolate_home RUN_DIR — point HOME at a throwaway dir and clear everything
# that could redirect evener away from it.
#
# A throwaway $HOME keeps hub.lock, auth-token, and session history off the
# real ~/.local/state/evener, and credentials.toml/providers.toml off the
# real ~/.config/evener, entirely. Everything that can redirect evener away
# from the throwaway $HOME is unset, not just the state dir: an operator with
# any of these exported would otherwise have this hub read or write their
# real config, cache, run dir or hub token while the header above promises
# isolation. EVENER_PROVIDERS_CONFIG belongs in this list above all: it
# outranks $HOME/.config/evener/providers.toml, so leaving it set would load
# the operator's real providers instead of the fake backend — a network call
# and a paid request out of a fixture whose whole point is that neither
# happens.
e2e_isolate_home() {
	local run_dir="$1"
	export HOME="$run_dir/home"
	mkdir -p "$HOME/.config/evener" "$HOME/.local/state/evener"
	unset XDG_STATE_HOME XDG_CONFIG_HOME XDG_CACHE_HOME
	unset EVENER_STATE_DIR EVENER_RUN_DIR EVENER_HUB_TOKEN EVENER_HUB_ADDR EVENER_HUB_SPAWNED
	unset EVENER_PROVIDERS_CONFIG
}

# e2e_wait_for_port LOG_FILE PID LABEL — poll until the process logs a port.
#
# Reads LOG_FILE for `listening on 127.0.0.1:NNNN`, polling up to 600 times
# with a 0.1s sleep, and bailing immediately if PID dies. The ceiling only has
# to be impossible for a healthy run to exhaust: the poll exits the moment the
# line appears, and the liveness arm catches a dead child immediately, so a
# long ceiling costs nothing when things work. 50 was too few and flaked under
# load; 600 is past anything a busy-but-healthy machine has shown. Sets the
# global `e2e_port` to the port number. Exits 1 on failure.
e2e_wait_for_port() {
	local log_file="$1"
	local pid="$2"
	local label="$3"
	e2e_port=""
	for _ in $(seq 1 600); do
		e2e_port="$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$log_file" 2>/dev/null | grep -oE '[0-9]+$')" || true
		[ -n "$e2e_port" ] && break
		kill -0 "$pid" 2>/dev/null || {
			echo "$label exited before it started listening:" >&2
			cat "$log_file" >&2
			exit 1
		}
		sleep 0.1
	done
	[ -n "$e2e_port" ] || {
		echo "$label never logged a listening port" >&2
		exit 1
	}
	echo "$SCRIPT_NAME: $label up at 127.0.0.1:$e2e_port (pid $pid)" >&2
}

# e2e_build_binary LABEL DEST PKG — build one Go binary, or fail loudly.
e2e_build_binary() {
	local label="$1"
	local dest="$2"
	local pkg="$3"
	echo "==> building $label" >&2
	go build -o "$dest" "$pkg" || { echo "$label build failed" >&2; exit 1; }
}

# e2e_health_check HUB_ADDR — confirm the hub answers at its root.
e2e_health_check() {
	local hub_addr="$1"
	curl -s -o /dev/null "$hub_addr/" || { echo "hub did not answer at $hub_addr" >&2; exit 1; }
}
