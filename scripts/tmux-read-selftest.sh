#!/usr/bin/env bash
# tmux-read-selftest.sh — offline, deterministic tests for scripts/tmux-read.sh's
# argument construction and error handling, against a fake `tmux` binary. No real
# tmux server is started or required (kata dxdm, acceptance #4).
#
# The fake enforces the exact argv SHAPE tmux-read.sh must produce
# (`capture-pane -p -S <lines> -t <target>`) and refuses anything else, so a
# regression in how the real script builds that command fails loudly here
# instead of silently working around a more lenient fake. It also enforces
# that <target> is always the tool's required exact-match form `=name:` — see
# tmux-read.sh's header for why that trailing colon is not decorative.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/tmux-read.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else
		bad "$3 (want '$2', got '$1')"
	fi
}
assert_has() {
	if grep -qF -- "$2" "$1"; then ok "$3"; else
		bad "$3 (missing '$2' in: $(cat "$1"))"
	fi
}

work="$(mktemp -d -t tmux-read-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

# Each case gets its own fake-tmux bin dir + state dir, so scenarios never leak
# call counts or session lists into each other.
new_case() {
	case_dir="$(mktemp -d "$work/case.XXXXXX")"
	bin="$case_dir/bin"
	state="$case_dir/state"
	mkdir -p "$bin" "$state"
	cat >"$bin/tmux" <<'FAKE_TMUX'
#!/usr/bin/env bash
# Fake tmux: understands only the exact `capture-pane -p -S <lines> -t <target>`
# shape tmux-read.sh is required to produce. FAKE_TMUX_SESSIONS (space
# separated) lists the sessions that "exist"; FAKE_STATE/pane-<name>.content
# (if present) is what capture-pane "returns" for that session.
set -u

n=$(($(cat "$FAKE_STATE/call-count" 2>/dev/null || echo 0) + 1))
printf '%s' "$n" >"$FAKE_STATE/call-count"
printf '%s\0' "$@" >"$FAKE_STATE/call-$n.argv"

if [ "${1:-}" != "capture-pane" ]; then
	echo "fake-tmux: unsupported command: ${1:-}" >&2
	exit 127
fi
[ "${2:-}" = "-p" ] || { echo "fake-tmux: expected -p, got '${2:-}'" >&2; exit 1; }
[ "${3:-}" = "-S" ] || { echo "fake-tmux: expected -S, got '${3:-}'" >&2; exit 1; }
lines_arg="${4:-}"
[ "${5:-}" = "-t" ] || { echo "fake-tmux: expected -t, got '${5:-}'" >&2; exit 1; }
target="${6:-}"
if [ $# -ne 6 ]; then
	echo "fake-tmux: unexpected capture-pane argument count: $#: $*" >&2
	exit 1
fi
printf '%s' "$lines_arg" >"$FAKE_STATE/call-$n.lines"
printf '%s' "$target" >"$FAKE_STATE/call-$n.target"

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
	if [ "$s" = "$name" ]; then
		content_file="$FAKE_STATE/pane-$name.content"
		[ -f "$content_file" ] && cat "$content_file"
		exit 0
	fi
done
echo "can't find session: $name" >&2
exit 1
FAKE_TMUX
	chmod +x "$bin/tmux"
}

run_tmux_read() (
	# bash 3.2 (macOS's /bin/bash) treats "${args[@]}" on a zero-element array as
	# an unbound-variable error under `set -u`; ${args[@]+"${args[@]}"}" is the
	# portable safe-empty-array-expansion idiom.
	env PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_TMUX_SESSIONS="${FAKE_TMUX_SESSIONS:-}" "$script" "${args[@]+"${args[@]}"}"
)

calls_made() { cat "$state/call-count" 2>/dev/null || echo 0; }

# --- scenario 1: session exists, default --lines, correct target/content ---
new_case
printf 'hello from the pane\nsecond line\n' >"$state/pane-mysession.content"
FAKE_TMUX_SESSIONS="mysession" args=(mysession)
out="$(run_tmux_read)"
rc=$?
assert_eq "$rc" "0" "existing session exits 0"
assert_eq "$out" "hello from the pane
second line" "stdout is exactly the fake's pane content"
assert_eq "$(cat "$state/call-1.target")" "=mysession:" "target uses exact-match form '=name:'"
assert_eq "$(cat "$state/call-1.lines")" "-200" "default --lines is 200 (as -S -200)"
assert_eq "$(calls_made)" "1" "exactly one tmux invocation for a successful read"

# --- scenario 2: --lines overrides the default ---
new_case
printf 'x\n' >"$state/pane-mysession.content"
FAKE_TMUX_SESSIONS="mysession" args=(mysession --lines 50)
run_tmux_read >/dev/null
assert_eq "$(cat "$state/call-1.lines")" "-50" "--lines 50 becomes -S -50"

# --- scenario 3: -n is a short alias for --lines ---
new_case
printf 'x\n' >"$state/pane-mysession.content"
FAKE_TMUX_SESSIONS="mysession" args=(mysession -n 7)
run_tmux_read >/dev/null
assert_eq "$(cat "$state/call-1.lines")" "-7" "-n 7 becomes -S -7"

# --- scenario 4: missing session -> non-zero, useful stderr, no guessing ---
new_case
FAKE_TMUX_SESSIONS="othersession" args=(nosuchsession)
out="$(run_tmux_read 2>"$work/err4.txt")"
rc=$?
if [ "$rc" -ne 0 ]; then ok "missing session exits non-zero"; else bad "missing session exited 0"; fi
assert_eq "$out" "" "missing session prints nothing to stdout"
assert_has "$work/err4.txt" "nosuchsession" "error names the requested session"
assert_has "$work/err4.txt" "tmux list-sessions" "error hints how to list live sessions"

# --- scenario 5: exact match only — a same-prefix session must not be guessed ---
new_case
printf 'foo content\n' >"$state/pane-foo.content"
printf 'foobar content\n' >"$state/pane-foobar.content"
FAKE_TMUX_SESSIONS="foo foobar" args=(foo)
out="$(run_tmux_read)"
assert_eq "$out" "foo content" "exact name 'foo' reads foo's own content, not foobar's"

new_case
printf 'foo content\n' >"$state/pane-foo.content"
printf 'foobar content\n' >"$state/pane-foobar.content"
FAKE_TMUX_SESSIONS="foo foobar" args=(foob)
out="$(run_tmux_read 2>"$work/err5.txt")"
rc=$?
if [ "$rc" -ne 0 ]; then
	ok "ambiguous/partial name 'foob' (prefix of foobar, exact match of neither) is rejected"
else
	bad "'foob' was silently resolved instead of rejected: $out"
fi

# --- scenario 6: no session argument at all ---
new_case
args=()
out="$(run_tmux_read 2>"$work/err6.txt")"
rc=$?
assert_eq "$rc" "2" "missing <session> argument exits 2 (usage error)"
assert_eq "$(calls_made)" "0" "no tmux invocation when <session> is missing"

# --- scenario 7: extra positional argument is rejected, not silently joined ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession extra)
out="$(run_tmux_read 2>"$work/err7.txt")"
rc=$?
assert_eq "$rc" "2" "extra positional argument exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation with an extra positional argument"

# --- scenario 8: non-numeric --lines is rejected before shelling out ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession --lines abc)
out="$(run_tmux_read 2>"$work/err8.txt")"
rc=$?
assert_eq "$rc" "2" "--lines abc exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation with a non-numeric --lines"

# --- scenario 9: negative --lines is rejected (tmux wants the sign implicit) ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=(mysession --lines -5)
out="$(run_tmux_read 2>"$work/err9.txt")"
rc=$?
assert_eq "$rc" "2" "--lines -5 exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation with a negative --lines"

# --- scenario 10: a ':' in the target looks like session:window syntax ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=("mysession:1")
out="$(run_tmux_read 2>"$work/err10.txt")"
rc=$?
assert_eq "$rc" "2" "a target containing ':' exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation for a ':'-qualified target"
assert_has "$work/err10.txt" "session" "error explains this tool targets whole sessions only"

# --- scenario 11: a '.' in the target looks like session.pane syntax ---
new_case
FAKE_TMUX_SESSIONS="mysession" args=("my.session")
out="$(run_tmux_read 2>"$work/err11.txt")"
rc=$?
assert_eq "$rc" "2" "a target containing '.' exits 2"
assert_eq "$(calls_made)" "0" "no tmux invocation for a '.'-qualified target"

# --- scenario 12: --help exits 0, prints usage, never touches tmux ---
new_case
args=(--help)
out="$(run_tmux_read)"
rc=$?
assert_eq "$rc" "0" "--help exits 0"
assert_has <(printf '%s' "$out") "Usage" "--help output mentions Usage"
assert_eq "$(calls_made)" "0" "--help makes no tmux invocation"

# --- scenario 13: TMUX_BIN override works without relying on PATH injection ---
new_case
printf 'via-TMUX_BIN\n' >"$state/pane-mysession.content"
FAKE_TMUX_SESSIONS="mysession" args=(mysession)
out=$(env PATH="/usr/bin:/bin" TMUX_BIN="$bin/tmux" FAKE_STATE="$state" FAKE_TMUX_SESSIONS="$FAKE_TMUX_SESSIONS" "$script" "${args[@]}")
assert_eq "$out" "via-TMUX_BIN" "TMUX_BIN can point straight at a fake, bypassing PATH"

echo
echo "$checks checks, $fails failed"
[ "$fails" -eq 0 ]
