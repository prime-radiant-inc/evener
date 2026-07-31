#!/usr/bin/env bash
# tmux-send.sh — deliver literal text to a named tmux session and submit it.
#
# Why this exists (kata dxdm): companion to tmux-read.sh for coordinating
# long-running agent sessions — send a message into a session's active pane
# (e.g. a running agent's prompt) without attaching, and without tmux's own
# key-name parsing or the invoking shell mangling the text along the way.
#
# The danger this guards against: tmux `send-keys` WITHOUT `-l` treats its
# argument as a KEY NAME first, falling back to literal characters only when
# the whole string is not a recognized name. A message that happens to equal
# a key name is swallowed as that keypress instead of typed — verified live:
# sending the single word "Enter" without -l submits an empty line instead of
# typing the four characters "Enter". `-l` (literal) disables that entirely:
# the whole argument is sent byte-for-byte, no key-name lookup, ever. This
# script always uses `-l` and always passes the message as ONE argv element
# (never through a shell that could word-split, glob-expand, or interpolate
# `$`/backticks in it), which is what actually keeps spaces/quotes/$/newlines
# unmutated — `-l` alone would not help a message already corrupted by an
# unquoted `$var` upstream of this script.
#
# Targeting: same exact-name-only `=session:` scheme as tmux-read.sh — see
# that script's header for why the trailing `:` is required. Never guesses.
#
# "Submit": after the literal text lands, ONE plain (non-literal) Enter
# keypress is sent to submit it, same as a human pressing Enter after typing.
# Any newline INSIDE the message is delivered as literal bytes too (tmux -l
# does not skip them) — what the receiving program does with an embedded
# newline (submit early, or accept it as part of multi-line input) is that
# program's own line-editing behaviour, not something a keystroke sender can
# change.
#
# Usage:
#   scripts/tmux-send.sh <session> <text>   # send <text>, then Enter
#   scripts/tmux-send.sh <session> --stdin  # read the literal message from
#                                            # stdin instead — the easiest way
#                                            # to hand over a multi-line
#                                            # message without fighting your
#                                            # own shell's quoting:
#                                            #   scripts/tmux-send.sh sess --stdin <<'EOF'
#                                            #   line one
#                                            #   line two, with a $var and "quotes"
#                                            #   EOF
#
# TMUX_BIN overrides the tmux binary (default: "tmux", resolved via PATH) —
# tests point it at a fake; a fake named "tmux" earlier on PATH works too.
#
# Exit codes: 0 success, 1 no session named exactly <session> (or the submit
# failed after the text was already sent), 2 bad usage.
set -uo pipefail

prog="tmux-send"
TMUX_BIN="${TMUX_BIN:-tmux}"
session=""
text=""
have_text=0
use_stdin=0

while [ $# -gt 0 ]; do
	case "$1" in
	--stdin)
		use_stdin=1
		;;
	-h | --help)
		sed -n '2,49p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	-*)
		echo "$prog: unknown argument: $1 (try --help)" >&2
		exit 2
		;;
	*)
		if [ -z "$session" ]; then
			session="$1"
		elif [ "$have_text" -eq 0 ]; then
			text="$1"
			have_text=1
		else
			echo "$prog: unexpected extra argument: $1 (the message must be a single argument — quote it, or use --stdin; try --help)" >&2
			exit 2
		fi
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

if [ "$use_stdin" -eq 1 ] && [ "$have_text" -eq 1 ]; then
	echo "$prog: pass the message as an argument OR --stdin, not both" >&2
	exit 2
fi
if [ "$use_stdin" -eq 0 ] && [ "$have_text" -eq 0 ]; then
	echo "$prog: missing <text> (pass it as an argument, or use --stdin; try --help)" >&2
	exit 2
fi

if [ "$use_stdin" -eq 1 ]; then
	# Command substitution strips ALL trailing newlines — append a sentinel so
	# a message that legitimately ends in one or more newlines survives.
	text=$(cat; printf x)
	text=${text%x}
fi

# Exact match only (see header): a bare session name lets tmux fall back to
# prefix/glob matching, which can silently resolve to the WRONG session.
target="=$session:"

errfile=$(mktemp -t "$prog.XXXXXX")
trap 'rm -f "$errfile"' EXIT

if ! "$TMUX_BIN" send-keys -t "$target" -l "$text" 2>"$errfile"; then
	echo "$prog: no session named exactly '$session' ($(cat "$errfile"))" >&2
	echo "$prog: list live sessions: tmux list-sessions" >&2
	exit 1
fi

if ! "$TMUX_BIN" send-keys -t "$target" Enter 2>"$errfile"; then
	echo "$prog: message text was sent to '$session' but submitting Enter failed ($(cat "$errfile")) — it may be sitting unsent in the target's input" >&2
	exit 1
fi
