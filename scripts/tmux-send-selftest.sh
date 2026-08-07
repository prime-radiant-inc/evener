#!/usr/bin/env bash
# tmux-send-selftest.sh — offline, deterministic tests for scripts/tmux-send.sh's
# argument construction, byte-for-byte message fidelity, and error handling,
# against a fake `tmux` binary. No real tmux server is started or required
# (kata dxdm, acceptance #4).
#
# The fake enforces the exact argv SHAPE tmux-send.sh must produce for both
# calls it is required to make per send — `send-keys -t <target> -l <text>`
# then `send-keys -t <target> Enter` — and refuses anything else, so a
# regression (e.g. dropping -l, or submitting via a literal newline instead of
# a real Enter keypress) fails loudly here instead of silently passing. It
# also enforces the exact-match `=name:` target form — see tmux-send.sh's and
# tmux-read.sh's headers for why the trailing colon is not decorative.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/tmux-send.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t tmux-send-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

# Each case gets its own fake-tmux bin dir + state dir, so scenarios never leak
# call counts or session lists into each other. Also resets the FAKE_TMUX_*
# knobs: `FAKE_TMUX_SESSIONS=x FAKE_TMUX_FAIL_ON_CALL=y args=(...)` is a plain
# assignment (not a command), so unlike `VAR=val cmd`, bash does NOT scope
# these to one call — a scenario setting FAKE_TMUX_FAIL_ON_CALL would silently
# stay in effect for every scenario after it without this reset.
new_case() {
	FAKE_TMUX_SESSIONS=""
	FAKE_TMUX_FAIL_ON_CALL=""
	case_dir="$(mktemp -d "$work/case.XXXXXX")"
	bin="$case_dir/bin"
	state="$case_dir/state"
	mkdir -p "$bin" "$state"
	cat >"$bin/tmux" <<'FAKE_TMUX'
#!/usr/bin/env bash
# Fake tmux: understands only the exact `send-keys -t <target> -l <text>` and
# `send-keys -t <target> Enter` shapes tmux-send.sh is required to produce.
# FAKE_TMUX_SESSIONS (space separated) lists the sessions that "exist".
set -u

n=$(($(cat "$FAKE_STATE/call-count" 2>/dev/null || echo 0) + 1))
printf '%s' "$n" >"$FAKE_STATE/call-count"
printf '%s\0' "$@" >"$FAKE_STATE/call-$n.argv"

# FAKE_TMUX_FAIL_ON_CALL=N: force invocation N to fail regardless of target,
# simulating the session vanishing mid-operation (e.g. between the literal
# send and the Enter submit that follows it).
if [ "$n" = "${FAKE_TMUX_FAIL_ON_CALL:-}" ]; then
	echo "fake-tmux: forced failure on call $n (FAKE_TMUX_FAIL_ON_CALL)" >&2
	exit 1
fi

if [ "${1:-}" != "send-keys" ]; then
	echo "fake-tmux: unsupported command: ${1:-}" >&2
	exit 127
fi
[ "${2:-}" = "-t" ] || { echo "fake-tmux: expected -t, got '${2:-}'" >&2; exit 1; }
target="${3:-}"
printf '%s' "$target" >"$FAKE_STATE/call-$n.target"

case "${4:-}" in
-l)
	if [ $# -ne 5 ]; then
		echo "fake-tmux: expected exactly one literal-text argument after -l, got $(($# - 4)): $*" >&2
		exit 1
	fi
	printf '%s' "literal" >"$FAKE_STATE/call-$n.kind"
	printf '%s' "$5" >"$FAKE_STATE/call-$n.text"
	;;
Enter)
	if [ $# -ne 4 ]; then
		echo "fake-tmux: Enter must be the only key after -t <target>, got $(($# - 3)) trailing arguments" >&2
		exit 1
	fi
	printf '%s' "enter" >"$FAKE_STATE/call-$n.kind"
	;;
*)
	echo "fake-tmux: unexpected send-keys form after target: ${4:-}" >&2
	exit 1
	;;
esac

case "$target" in
=*:) ;;
*)
	echo "fake-tmux: target '$target' is not this tool's required exact-match form '=name:' — refusing to guess" >&2
	exit 1
	;;
esac
name="${target#=}"
name="${name%:}"

for s in $FAKE_TMUX_SESSIONS; do
	[ "$s" = "$name" ] && exit 0
done
echo "can't find session: $name" >&2
exit 1
FAKE_TMUX
	chmod +x "$bin/tmux"
}

run_tmux_send() (
	# bash 3.2 (macOS's /bin/bash) treats "${args[@]}" on a zero-element array as
	# an unbound-variable error under `set -u`; "${args[@]+"${args[@]}"}" is the
	# portable safe-empty-array-expansion idiom.
	env PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_TMUX_SESSIONS="${FAKE_TMUX_SESSIONS:-}" \
		FAKE_TMUX_FAIL_ON_CALL="${FAKE_TMUX_FAIL_ON_CALL:-}" "$script" "${args[@]+"${args[@]}"}"
)

calls_made() { cat "$state/call-count" 2>/dev/null || echo 0; }
call_text() { # $1 = call number; extracts the LAST NUL-delimited field (the -l text)
	# Two separate `local` statements, deliberately: bash 3.2 (macOS's /bin/bash)
	# does not see an earlier name in the SAME `local a=.. b=$a..` statement as
	# set yet, and raises "unbound variable" under `set -u` when the later
	# assignment's right-hand side references it.
	local n="$1"
	local f="$state/call-$n.argv" out="" part
	while IFS= read -r -d '' part; do
		out="$part"
	done <"$f"
	printf '%s' "$out"
}

# --- scenario 1: basic send — two calls, right shapes, right order ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession "hello world")
run_tmux_send >/dev/null 2>"$work/err1.txt"
rc=$?
assert_eq "$rc" "0" "basic send exits 0"
assert_eq "$(cat "$work/err1.txt")" "" "basic send prints nothing to stderr"
assert_eq "$(calls_made)" "2" "basic send makes exactly two tmux calls"
assert_eq "$(cat "$state/call-1.kind")" "literal" "call 1 is the literal -l send"
assert_eq "$(cat "$state/call-1.target")" "=mysession:" "call 1 targets the exact-match form '=name:'"
assert_eq "$(call_text 1)" "hello world" "call 1's literal text is exactly the message"
assert_eq "$(cat "$state/call-2.kind")" "enter" "call 2 is the plain Enter keypress (not -l)"
assert_eq "$(cat "$state/call-2.target")" "=mysession:" "call 2 targets the same exact-match form"

# --- scenario 2: byte-for-byte fidelity — spaces, quotes, $, backticks, *, embedded newline ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession)
# Single-quoted on purpose: $HOME/`pwd`/$var must reach tmux-send.sh UNEXPANDED
# so the test proves the script does not expand them either.
# shellcheck disable=SC2016
printf '%s' 'hello "world" $HOME `pwd` spaced   out
second line with $var and *glob* and \backslash\' >"$work/expected2.txt"
args=(mysession "$(cat "$work/expected2.txt")")
run_tmux_send >/dev/null
call_text 1 >"$work/got2.txt"
if cmp -s "$work/expected2.txt" "$work/got2.txt"; then
	ok "tricky message (spaces/quotes/\$/backticks/glob/embedded newline) survives byte-for-byte"
else
	bad "tricky message was mutated: expected $(xxd <"$work/expected2.txt" | head -3), got $(xxd <"$work/got2.txt" | head -3)"
fi

# --- scenario 3: --stdin reads the literal message, trailing newline preserved ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession --stdin)
# shellcheck disable=SC2016  # $var deliberately left unexpanded, see scenario 2
printf 'line one\nline two with $var and "quotes"\n' >"$work/expected3.txt"
run_tmux_send <"$work/expected3.txt" >/dev/null
call_text 1 >"$work/got3.txt"
if cmp -s "$work/expected3.txt" "$work/got3.txt"; then
	ok "--stdin message (including trailing newline) survives byte-for-byte"
else
	bad "--stdin message was mutated: expected $(xxd <"$work/expected3.txt" | head -3), got $(xxd <"$work/got3.txt" | head -3)"
fi

# --- scenario 4: message text exactly equal to a tmux key name still goes through -l ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession "Enter")
run_tmux_send >/dev/null
assert_eq "$(cat "$state/call-1.kind")" "literal" "a message that is itself the word 'Enter' is still sent via -l"
assert_eq "$(call_text 1)" "Enter" "the literal text is the 4 characters 'Enter', not a keypress"

# --- scenario 5: missing session — non-zero, useful stderr, no Enter call after a failed send ---
new_case
FAKE_TMUX_SESSIONS="othersession" args=(nosuchsession "hi")
out="$(run_tmux_send 2>"$work/err5.txt")"
rc=$?
[ "$rc" -ne 0 ] || bad "missing session exited 0"
[ "$rc" -eq 0 ] || ok "missing session exits non-zero"
assert_eq "$out" "" "missing session prints nothing to stdout"
assert_has "$work/err5.txt" "nosuchsession" "error names the requested session"
assert_has "$work/err5.txt" "tmux list-sessions" "error hints how to list live sessions"
assert_eq "$(calls_made)" "1" "only the failed literal-send call is attempted, no Enter follows a failure"

# --- scenario 5b: the literal text lands but the Enter submit itself fails
# (session vanished between the two calls) — a distinct error, not the
# generic "no session named exactly" one, because by then it plainly did. ---
new_case
FAKE_TMUX_SESSIONS="mysession" FAKE_TMUX_FAIL_ON_CALL=2 args=(mysession "hi")
out="$(run_tmux_send 2>"$work/err5b.txt")"
rc=$?
assert_eq "$rc" "1" "a failed Enter submit after a successful literal send exits 1"
assert_eq "$out" "" "a failed Enter submit prints nothing to stdout"
assert_eq "$(calls_made)" "2" "both calls were attempted (the literal send did succeed)"
assert_eq "$(cat "$state/call-1.kind")" "literal" "call 1 (the literal send) is the one that succeeded"
assert_has "$work/err5b.txt" "mysession" "the partial-failure error names the session"
assert_has "$work/err5b.txt" "sent" "the partial-failure error says the text WAS already sent"

# --- scenario 6: exact match only — a same-prefix session must not be guessed ---
new_case
FAKE_TMUX_SESSIONS="foo foobar" args=(foo "hi")
run_tmux_send >/dev/null
assert_eq "$(cat "$state/call-1.target")" "=foo:" "sending to 'foo' targets exactly 'foo', not 'foobar'"

new_case
FAKE_TMUX_SESSIONS="foo foobar" args=(foob "hi")
out="$(run_tmux_send 2>"$work/err6.txt")"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "ambiguous/partial name 'foob' (prefix of foobar, exact match of neither) is rejected"
else
	bad "'foob' was silently resolved instead of rejected: $out"
fi

# --- scenario 7: no text and no --stdin ---
new_case
args=(mysession)
out="$(run_tmux_send 2>"$work/err7.txt")"
rc=$?
assert_eq "$rc" "2" "missing text and --stdin exits 2 (usage error)"
assert_eq "$(calls_made)" "0" "no tmux invocation when text is missing"

# --- scenario 8: both a text argument AND --stdin is ambiguous, rejected ---
new_case
args=(mysession "hi" --stdin)
out="$(echo unused | run_tmux_send 2>"$work/err8.txt")"
rc=$?
assert_eq "$rc" "2" "text argument AND --stdin together exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation when text and --stdin conflict"

# --- scenario 9: extra positional argument (unquoted-message mistake) is rejected ---
new_case
args=(mysession hello world)
out="$(run_tmux_send 2>"$work/err9.txt")"
rc=$?
assert_eq "$rc" "2" "extra positional argument exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation with an extra positional argument"
assert_has "$work/err9.txt" "quote" "error suggests quoting the message or using --stdin"

# --- scenario 10: an explicitly empty message is allowed (still an explicit choice) ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession "")
run_tmux_send >/dev/null
rc=$?
assert_eq "$rc" "0" "an explicit empty message is accepted"
assert_eq "$(call_text 1)" "" "the literal text sent is the empty string"
assert_eq "$(calls_made)" "2" "an empty literal send is still followed by the Enter submit"

# --- scenario 11: missing <session> argument entirely ---
new_case
args=()
out="$(run_tmux_send 2>"$work/err11.txt")"
rc=$?
assert_eq "$rc" "2" "missing <session> exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation when <session> is missing"

# --- scenario 12: a ':' in the target looks like session:window syntax ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=("mysession:1" "hi")
out="$(run_tmux_send 2>"$work/err12.txt")"
rc=$?
assert_eq "$rc" "2" "a target containing ':' exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation for a ':'-qualified target"

# --- scenario 13: a '.' in the target looks like session.pane syntax ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=("my.session" "hi")
out="$(run_tmux_send 2>"$work/err13.txt")"
rc=$?
assert_eq "$rc" "2" "a target containing '.' exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation for a '.'-qualified target"

# --- scenario 14: --help exits 0, prints usage, never touches tmux ---
new_case
args=(--help)
out="$(run_tmux_send </dev/null)"
rc=$?
assert_eq "$rc" "0" "--help exits 0"
assert_has <(printf '%s' "$out") "Usage" "--help output mentions Usage"
assert_eq "$(calls_made)" "0" "--help makes no tmux invocation"

# --- scenario 15: TMUX_BIN override works without relying on PATH injection ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession "via TMUX_BIN")
env PATH="/usr/bin:/bin" TMUX_BIN="$bin/tmux" FAKE_STATE="$state" FAKE_TMUX_SESSIONS="$FAKE_TMUX_SESSIONS" "$script" "${args[@]}" >/dev/null
rc=$?
assert_eq "$rc" "0" "TMUX_BIN can point straight at a fake, bypassing PATH"
assert_eq "$(call_text 1)" "via TMUX_BIN" "the message sent via TMUX_BIN override is intact"

selftest_summary
