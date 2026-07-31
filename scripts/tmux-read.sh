#!/usr/bin/env bash
# tmux-read.sh — inspect a named tmux session's recent output without attaching.
#
# Why this exists (kata dxdm): coordinating several long-running agent sessions,
# each living in its own tmux session, needs a way to check "what is it doing"
# without attaching interactively — attaching steals the terminal, and a slipped
# keystroke while attached can corrupt whatever the agent is mid-typing.
#
# Canonical source: tmux PANE SCROLLBACK (`tmux capture-pane`), NOT a session
# log file. This tool only ever acts on an already-running session it did not
# create, so it cannot assume the session was launched with logging enabled
# (`tmux pipe-pane`), nor guess what path such a log would live at — depending
# on a log file would mean either guessing a path (forbidden by this kata) or
# silently showing nothing when logging was never configured. Pane scrollback
# needs no setup, exists for every session unconditionally, and is exactly what
# a human would see if they attached — it is the only source available with
# zero coordination with however the target session was started.
# Trade-off, on the record: scrollback is bounded by the pane's history-limit
# and disappears once the session is killed. A durable, unbounded log is a
# session-LAUNCH-time decision (a companion helper opting into `pipe-pane`),
# not something a read-only inspector can retrofit onto a session it didn't
# start — out of scope here.
#
# Targeting: sessions are matched by EXACT name only (tmux target `=name:`),
# never tmux's default prefix/glob matching — this tool must never guess which
# session you meant. The trailing `:` is not decorative: `-t "=name"` alone
# (no window component) fails pane-level commands like capture-pane with
# "can't find pane" even when the session unquestionably exists — verified
# live against tmux 3.6a. Appending an empty window component (`:`) is what
# restores tmux's normal "current window, active pane" default; `=name`
# (bare, no colon) works fine for target-SESSION commands like has-session,
# but capture-pane takes a target-PANE, which resolves differently.
#
# Usage:
#   scripts/tmux-read.sh <session>             # last 200 lines of scrollback
#   scripts/tmux-read.sh <session> --lines 50  # last 50 lines
#   scripts/tmux-read.sh <session> -n 50       # same, short form
#
# TMUX_BIN overrides the tmux binary (default: "tmux", resolved via PATH) —
# tests point it at a fake; a fake named "tmux" earlier on PATH works too.
#
# Exit codes: 0 success, 1 no session named exactly <session>, 2 bad usage.
set -uo pipefail

prog="tmux-read"
TMUX_BIN="${TMUX_BIN:-tmux}"
lines=200
session=""

while [ $# -gt 0 ]; do
	case "$1" in
	-n | --lines)
		shift
		lines="${1:-}"
		;;
	-h | --help)
		sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*)
		echo "$prog: unknown argument: $1 (try --help)" >&2
		exit 2
		;;
	*)
		if [ -n "$session" ]; then
			echo "$prog: unexpected extra argument: $1 (only one <session> is accepted; try --help)" >&2
			exit 2
		fi
		session="$1"
		;;
	esac
	shift
done

[ -n "$session" ] || {
	echo "$prog: missing <session> (try --help)" >&2
	exit 2
}

# A real tmux session can never contain ':' or '.' — tmux itself strips both
# at creation time — so a target containing either is always a caller mistake
# (session:window or session.pane syntax), never a legitimate name to reject
# by accident.
case "$session" in
*:* | *.*)
	echo "$prog: '$session' looks like session:window or session.pane syntax — this tool targets a whole session by exact name only, no window/pane component" >&2
	exit 2
	;;
esac

case "$lines" in
'' | *[!0-9]*)
	echo "$prog: --lines needs a non-negative integer, got '$lines'" >&2
	exit 2
	;;
esac

# Exact match only (see header): a bare session name lets tmux fall back to
# prefix/glob matching, which can silently resolve to the WRONG session.
target="=$session:"

errfile=$(mktemp -t "$prog.XXXXXX")
trap 'rm -f "$errfile"' EXIT

if ! "$TMUX_BIN" capture-pane -p -S "-$lines" -t "$target" 2>"$errfile"; then
	echo "$prog: no session named exactly '$session' ($(cat "$errfile"))" >&2
	echo "$prog: list live sessions: tmux list-sessions" >&2
	exit 1
fi
