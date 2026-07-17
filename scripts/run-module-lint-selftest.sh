#!/usr/bin/env bash
# run-module-lint-selftest.sh - offline behavioral tests for module linting.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
runner="$script_dir/run-module-lint.sh"
makefile="$(cd "$script_dir/.." && pwd)/Makefile"
work="$(mktemp -d -t serf-module-lint-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL - %s\n' "$1"; }
assert_eq() {
	if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi
}
assert_has() {
	if grep -qF -- "$2" "$1"; then
		ok "$3"
	else
		bad "$3 (missing '$2')"
		sed 's/^/    | /' "$1"
	fi
}
assert_not_has() {
	if grep -qF -- "$2" "$1"; then
		bad "$3 (unexpected '$2')"
		sed 's/^/    | /' "$1"
	else
		ok "$3"
	fi
}

assert_count() {
	actual="$(grep -cF -- "$2" "$1" || :)"
	assert_eq "$actual" "$3" "$4"
}

new_case() {
	case_dir="$(mktemp -d "$work/case.XXXXXX")"
	repo="$case_dir/repo"
	state="$case_dir/state"
	bin="$case_dir/bin"
	mkdir -p "$repo" "$state" "$bin"
	for module in agent llm auth envvars invariant identifier one two three four five; do
		mkdir -p "$repo/$module"
	done
	cat >"$bin/golangci-lint" <<'FAKE_LINT'
#!/usr/bin/env bash
set -u
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/calls"
printf 'stdout:%s\n' "$module"
printf 'stderr:%s\n' "$module" >&2
case " ${FAKE_FAIL_MODULES:-} " in
	*" $module "*) exit 7 ;;
esac
exit 0
FAKE_LINT
	chmod +x "$bin/golangci-lint"
}

run_lint() {
	modules="$1"
	output="$2"
	shift 2
	(
		cd "$repo" || exit 1
		env TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			MODULES="$modules" "$@" "$runner"
	) >"$output" 2>&1
}

# Bound cases that may expose a scheduler hang. The watchdog first requests a
# normal TERM so runner cleanup remains observable, then uses a second FIFO to
# force a terminal state only if the runner does not exit.
run_lint_bounded() {
	modules="$1"
	output="$2"
	shift 2
	mkfifo "$state/runner-watchdog-cancel" "$state/runner-terminated"
	exec 8<>"$state/runner-watchdog-cancel"
	exec 9<>"$state/runner-terminated"
	(
		cd "$repo" || exit 1
		exec env TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
			MODULES="$modules" "$@" "$runner"
	) >"$output" 2>&1 &
	lint_pid=$!
	(
		if IFS= read -r -t 5 _ <&8; then
			exit 0
		fi
		printf 'timed out\n' >"$state/runner-timeout"
		kill -TERM "$lint_pid" 2>/dev/null || :
		if ! IFS= read -r -t 1 _ <&9; then
			kill -KILL "$lint_pid" 2>/dev/null || :
		fi
	) &
	watchdog_pid=$!
	if wait "$lint_pid"; then bounded_rc=0; else bounded_rc=$?; fi
	printf 'cancel\n' >&8 || :
	printf 'terminated\n' >&9 || :
	wait "$watchdog_pid" || :
	exec 8>&-
	exec 9>&-
}

new_case
out="$case_dir/success.out"
if run_lint ". agent llm" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "all-success exits zero"
assert_eq "$(wc -l <"$out" | tr -d ' ')" "2" "all-success has exactly two output lines"
assert_eq "$(sed -n '1p' "$out")" "lint: checking 3 modules" "all-success prints one start line"
case "$(sed -n '2p' "$out")" in
	"PASS lint (3 modules, "*"s)") ok "all-success prints one final PASS line" ;;
	*) bad "all-success PASS line has the wrong shape" ;;
esac
assert_not_has "$out" "stdout:" "successful stdout chatter is absent"
assert_not_has "$out" "stderr:" "successful stderr chatter is absent"
assert_eq "$(cut -f1 "$state/calls" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')" ". agent llm" "all requested modules ran"
assert_eq "$(cut -f2 "$state/calls" 2>/dev/null | sort -u)" "run --allow-parallel-runners ./..." "every module receives the parallel-safe golangci-lint argv"
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "successful temporary logs are removed"

# golangci-lint refuses overlapping processes by default. Model that external
# contract directly so the aggregate runner must explicitly admit its own
# bounded parallel children rather than depending on scheduler timing.
new_case
cat >"$bin/golangci-lint" <<'FAKE_PARALLEL_ADMISSION'
#!/usr/bin/env bash
case " $* " in
	*" --allow-parallel-runners "*) exit 0 ;;
esac
printf 'Error: parallel golangci-lint is running\n' >&2
exit 3
FAKE_PARALLEL_ADMISSION
chmod +x "$bin/golangci-lint"
out="$case_dir/parallel-admission.out"
if run_lint ". agent" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "bounded parallel checks opt in to concurrent golangci-lint processes"
assert_not_has "$out" "parallel golangci-lint is running" "parallel lock failures do not escape the aggregate runner"

new_case
out="$case_dir/failure.out"
if FAKE_FAIL_MODULES="identifier llm" run_lint "identifier agent llm auth" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "mixed failures exit nonzero"; else bad "mixed failures unexpectedly exit zero"; fi
assert_eq "$(cut -f1 "$state/calls" 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')" "agent auth identifier llm" "every module runs after early failures"
assert_eq "$(cut -f2 "$state/calls" 2>/dev/null | sort -u)" "run --allow-parallel-runners ./..." "failed and passing modules use the same parallel-safe argv"
assert_has "$out" "stdout:identifier" "first failed module stdout is complete"
assert_has "$out" "stderr:identifier" "first failed module stderr is complete"
assert_has "$out" "stdout:llm" "second failed module stdout is complete"
assert_has "$out" "stderr:llm" "second failed module stderr is complete"
assert_not_has "$out" "stdout:agent" "passing module stdout stays hidden"
assert_not_has "$out" "stderr:auth" "passing module stderr stays hidden"
identifier_line="$(grep -nF -- '----- identifier -----' "$out" | cut -d: -f1)"
llm_line="$(grep -nF -- '----- llm -----' "$out" | cut -d: -f1)"
if [ -n "$identifier_line" ] && [ -n "$llm_line" ] && [ "$identifier_line" -lt "$llm_line" ]; then
	 ok "failure logs follow MODULES order"
else
	bad "failure logs are out of MODULES order"
fi
assert_eq "$(tail -n 1 "$out")" "FAIL lint (2/4 modules: identifier llm)" "one final summary names every failed module in order"
logdir="$(sed -n 's/^full logs: //p' "$out")"
if [ -n "$logdir" ] && [ -d "$logdir" ]; then
	assert_eq "$(find "$logdir" -type f -name '*.log' | wc -l | tr -d ' ')" "2" "only failed module logs are retained"
	assert_eq "$(find "$logdir" -type f -name '*.status' | wc -l | tr -d ' ')" "0" "retained failure directory has no status files"
else
	bad "failure output names an existing retained-log directory"
	bad "retained failure directory has no status files"
fi

# An explicit empty value uses the documented default, while malformed or
# non-positive explicit values fail before creating logs or launching checks.
new_case
out="$case_dir/empty-parallel.out"
if run_lint "." "$out" LINT_PARALLEL=; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "empty LINT_PARALLEL uses the default"
assert_eq "$(cut -f1 "$state/calls" 2>/dev/null)" "." "empty LINT_PARALLEL still launches the requested check"

for value in 0 -1 nope 00 08 010; do
	new_case
	out="$case_dir/invalid-parallel.out"
	run_lint_bounded ". agent" "$out" LINT_PARALLEL="$value"
	rc=$bounded_rc
	if [ "$rc" -ne 0 ]; then ok "LINT_PARALLEL=$value exits nonzero"; else bad "LINT_PARALLEL=$value exits zero"; fi
	assert_has "$out" "LINT_PARALLEL must be a positive integer" "LINT_PARALLEL=$value has one useful diagnostic"
	assert_eq "$(wc -l <"$out" | tr -d ' ')" "1" "LINT_PARALLEL=$value emits one diagnostic"
	if [ ! -e "$state/calls" ]; then ok "LINT_PARALLEL=$value launches no checks"; else bad "LINT_PARALLEL=$value launched a check"; fi
	assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "LINT_PARALLEL=$value creates no log directory"
done

# Missing executable: run Bash's missing-command path once, retain that
# diagnostic once, and name every unchecked module once.
new_case
rm "$bin/golangci-lint"
out="$case_dir/missing.out"
(
	cd "$repo" || exit 1
	TMPDIR="$case_dir" PATH="/usr/bin:/bin" MODULES=". agent llm" "$runner"
) >"$out" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then ok "missing golangci-lint exits nonzero"; else bad "missing golangci-lint exits zero"; fi
assert_eq "$(grep -c 'command not found' "$out")" "1" "missing-command diagnostic appears once"
assert_eq "$(tail -n 1 "$out")" "FAIL lint (3 modules not checked: . agent llm)" "missing-command summary names every skipped module once"
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "missing-command setup logs are removed"

# Temporary-log setup failure stops before any linter can launch.
new_case
cat >"$bin/mktemp" <<'FAKE_MKTEMP'
#!/usr/bin/env bash
printf 'mktemp: injected failure\n' >&2
exit 1
FAKE_MKTEMP
chmod +x "$bin/mktemp"
out="$case_dir/mktemp.out"
if run_lint ". agent" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "temporary-log creation failure exits nonzero"; else bad "temporary-log creation failure exits zero"; fi
assert_has "$out" "mktemp: injected failure" "temporary-log creation keeps the original diagnostic"
if [ ! -e "$state/calls" ]; then ok "temporary-log failure launches no checks"; else bad "temporary-log failure launched a check"; fi
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "temporary-log failure leaves no runner directory"

# Concurrency: all fake linters hold an atomic active slot until the test sends
# release tokens. This makes an over-limit launch observable without sleeps.
new_case
mkfifo "$state/started" "$state/release"
exec 3<>"$state/started"
exec 4<>"$state/release"
cat >"$bin/golangci-lint" <<'FAKE_CONCURRENCY'
#!/usr/bin/env bash
set -u
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active" 2>/dev/null || printf 0)"
active=$((active + 1))
printf '%s\n' "$active" >"$FAKE_STATE/active"
max="$(cat "$FAKE_STATE/max" 2>/dev/null || printf 0)"
[ "$active" -le "$max" ] || printf '%s\n' "$active" >"$FAKE_STATE/max"
rmdir "$FAKE_STATE/lock"
printf '%s\n' "$$" >"$FAKE_STATE/started"
IFS= read -r _ <"$FAKE_STATE/release"
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active")"
printf '%s\n' "$((active - 1))" >"$FAKE_STATE/active"
rmdir "$FAKE_STATE/lock"
FAKE_CONCURRENCY
chmod +x "$bin/golangci-lint"
(
	cd "$repo" || exit 1
	TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
		MODULES="one two three four five" LINT_PARALLEL=2 "$runner"
) >"$case_dir/concurrency.out" 2>&1 &
lint_pid=$!
if IFS= read -r -t 5 _ <&3 && IFS= read -r -t 5 _ <&3; then
	ok "configured wave launches two checks"
else
	bad "configured wave did not launch two checks"
fi
for _ in one two three four five; do printf 'go\n' >&4; done
if wait "$lint_pid"; then rc=0; else rc=$?; fi
exec 3>&-
exec 4>&-
assert_eq "$rc" "0" "bounded-concurrency scenario exits zero"
assert_eq "$(cat "$state/max" 2>/dev/null)" "2" "concurrency never exceeds LINT_PARALLEL=2"

# Defaults: synchronized canonical modules prove both the exact default module
# set and the default four-child bound without inspecting runner source.
new_case
mkfifo "$state/started" "$state/release"
exec 3<>"$state/started"
exec 4<>"$state/release"
cat >"$bin/golangci-lint" <<'FAKE_DEFAULTS'
#!/usr/bin/env bash
set -u
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/calls"
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active" 2>/dev/null || printf 0)"
active=$((active + 1))
printf '%s\n' "$active" >"$FAKE_STATE/active"
max="$(cat "$FAKE_STATE/max" 2>/dev/null || printf 0)"
[ "$active" -le "$max" ] || printf '%s\n' "$active" >"$FAKE_STATE/max"
rmdir "$FAKE_STATE/lock"
printf '%s\n' "$$" >"$FAKE_STATE/started"
IFS= read -r _ <"$FAKE_STATE/release"
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active")"
printf '%s\n' "$((active - 1))" >"$FAKE_STATE/active"
rmdir "$FAKE_STATE/lock"
FAKE_DEFAULTS
chmod +x "$bin/golangci-lint"
(
	cd "$repo" || exit 1
	exec env -u MODULES -u LINT_PARALLEL TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" \
		FAKE_REPO="$repo" FAKE_STATE="$state" "$runner"
) >"$case_dir/defaults.out" 2>&1 &
lint_pid=$!
default_started=0
for _ in one two three four; do
	if IFS= read -r -t 5 _ <&3; then
		default_started=$((default_started + 1))
	fi
done
assert_eq "$default_started" "4" "default wave launches four checks"
for _ in one two three four five six seven; do printf 'go\n' >&4; done
if wait "$lint_pid"; then rc=0; else rc=$?; fi
exec 3>&-
exec 4>&-
assert_eq "$rc" "0" "default-path scenario exits zero"
assert_eq "$(wc -l <"$state/calls" | tr -d ' ')" "7" "default path runs exactly seven modules"
assert_eq "$(cut -f1 "$state/calls" | sort | tr '\n' ' ' | sed 's/ $//')" ". agent auth envvars identifier invariant llm" "default path runs the canonical module set"
assert_eq "$(cut -f2 "$state/calls" | sort -u)" "run --allow-parallel-runners ./..." "default modules receive the parallel-safe argv"
assert_eq "$(cat "$state/max" 2>/dev/null)" "4" "default concurrency is bounded at four"
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "default path removes temporary logs"

# Interruption: the fake publishes its PID, then blocks. The acknowledged TERM
# and a bounded watchdog prove relay, wait, and cleanup without polling races.
new_case
mkfifo "$state/started" "$state/release" "$state/stopped-ack" "$state/runner-watchdog-cancel"
exec 3<>"$state/started"
exec 4<>"$state/release"
exec 5<>"$state/stopped-ack"
exec 6<>"$state/runner-watchdog-cancel"
cat >"$bin/golangci-lint" <<'FAKE_INTERRUPT'
#!/usr/bin/env bash
set -u
trap 'printf stopped >"$FAKE_STATE/stopped"; printf "stopped\n" >"$FAKE_STATE/stopped-ack"; exit 143' HUP INT TERM
printf '%s\n' "$PPID" >"$FAKE_STATE/parent"
printf '%s\n' "$$" >"$FAKE_STATE/started"
IFS= read -r _ <"$FAKE_STATE/release"
FAKE_INTERRUPT
chmod +x "$bin/golangci-lint"
(
	cd "$repo" || exit 1
	exec env TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
		MODULES="one" LINT_PARALLEL=1 "$runner"
) >"$case_dir/interrupt.out" 2>&1 &
lint_pid=$!
if IFS= read -r -t 5 child_pid <&3; then
	ok "interrupt fake published its PID"
else
	bad "interrupt fake did not start"
	child_pid=missing
fi
assert_eq "$(cat "$state/parent" 2>/dev/null)" "$lint_pid" "active linter is a direct runner child"
kill -TERM "$lint_pid" 2>/dev/null || :
if IFS= read -r -t 5 acknowledgement <&5 && [ "$acknowledgement" = stopped ]; then
	ok "runner relays termination to the active child"
else
	bad "runner did not relay termination within the bounded acknowledgement window"
	printf 'release\n' >&4 || :
	[ "$child_pid" = missing ] || kill -TERM "$child_pid" 2>/dev/null || :
	if ! IFS= read -r -t 1 _ <&5; then
		[ "$child_pid" = missing ] || kill -KILL "$child_pid" 2>/dev/null || :
	fi
	kill -KILL "$lint_pid" 2>/dev/null || :
fi

(
	if IFS= read -r -t 5 _ <"$state/runner-watchdog-cancel"; then
		exit 0
	fi
	printf 'timed out\n' >"$state/runner-timeout"
	printf 'release\n' >"$state/release" 2>/dev/null || :
	if [ "$child_pid" != missing ]; then
		kill -TERM "$child_pid" 2>/dev/null || :
		kill -KILL "$child_pid" 2>/dev/null || :
	fi
	kill -KILL "$lint_pid" 2>/dev/null || :
) &
watchdog_pid=$!
if wait "$lint_pid"; then rc=0; else rc=$?; fi
printf 'cancel\n' >&6 || :
wait "$watchdog_pid" || :
exec 3>&-
exec 4>&-
exec 5>&-
exec 6>&-
if [ -f "$state/runner-timeout" ]; then bad "interrupted runner exceeded its bounded exit window"; else ok "interrupted runner exits before the watchdog deadline"; fi
if [ "$rc" -ne 0 ]; then ok "interrupted runner exits nonzero"; else bad "interrupted runner exits zero"; fi
if [ -f "$state/stopped" ]; then ok "interrupted child handled termination"; else bad "interrupted child was not terminated"; fi
if [ "$child_pid" != missing ] && ! kill -0 "$child_pid" 2>/dev/null; then ok "interrupted child was waited"; else bad "interrupted child remains alive"; fi
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "interruption removes temporary logs"

# Makefile integration: copy the real build entry point, fake only external
# commands, and prove both quiet success and unchanged lint-family coverage.
case_dir="$(mktemp -d "$work/make-case.XXXXXX")"
repo="$case_dir/repo"
state="$case_dir/state"
bin="$case_dir/bin"
tmp="$case_dir/tmp"
mkdir -p "$repo/scripts" "$state" "$bin" "$tmp"
cp "$makefile" "$repo/Makefile"
cp "$runner" "$repo/scripts/run-module-lint.sh"
chmod +x "$repo/scripts/run-module-lint.sh"
for module in agent llm auth envvars invariant identifier; do mkdir -p "$repo/$module"; done

cat >"$bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
case "$*" in
	"env GOOS") printf 'darwin\n'; exit 0 ;;
	"env GOARCH") printf 'arm64\n'; exit 0 ;;
esac
printf 'go %s\n' "$*" >>"$FAKE_STATE/families"
printf 'go-success-chatter\n'
if [ "${FAKE_FAIL_DOCS:-0}" = 1 ] && [ "$*" = "run ./cmd/serf-docscheck" ]; then
	printf 'docs stdout diagnostic\n'
	printf 'docs stderr diagnostic\n' >&2
	exit 9
fi
FAKE_GO
cat >"$bin/git" <<'FAKE_GIT'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$FAKE_STATE/families"
printf 'git-success-chatter\n'
FAKE_GIT
cat >"$bin/golangci-lint" <<'FAKE_MAKE_LINT'
#!/usr/bin/env bash
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/modules"
printf 'golangci-success-chatter\n'
FAKE_MAKE_LINT
cat >"$repo/scripts/gitleaks-scan.sh" <<'FAKE_SECRET_SCAN'
#!/usr/bin/env bash
printf 'secret-scan %s\n' "$*" >>"$FAKE_STATE/families"
if [ "${FAKE_GITLEAKS_MISSING:-0}" = 1 ]; then
	printf 'warning: gitleaks not installed; skipping repo secret scan (install: https://github.com/gitleaks/gitleaks)\n' >&2
	exit 0
fi
printf 'secret-success-chatter\n'
FAKE_SECRET_SCAN
chmod +x "$bin/go" "$bin/git" "$bin/golangci-lint" "$repo/scripts/gitleaks-scan.sh"

out="$case_dir/make-lint.out"
if (
	cd "$repo" || exit 1
	TMPDIR="$tmp" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" make lint
) >"$out" 2>&1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "real make lint wiring exits zero with healthy fakes"
assert_eq "$(wc -l <"$out" | tr -d ' ')" "2" "real make lint keeps the two-line success contract"
assert_eq "$(sed -n '1p' "$out")" "lint: checking 7 modules" "Makefile passes the canonical module count"
case "$(sed -n '2p' "$out")" in
	"PASS lint (7 modules, "*"s)") ok "real make lint prints the final PASS line" ;;
	*) bad "real make lint PASS line has the wrong shape" ;;
esac
assert_not_has "$out" "success-chatter" "all successful lint-family chatter is hidden"
assert_eq "$(cut -f1 "$state/modules" | sort | tr '\n' ' ' | sed 's/ $//')" ". agent auth envvars identifier invariant llm" "Makefile passes every canonical non-fuzz module"
assert_eq "$(cut -f2 "$state/modules" | sort -u)" "run --allow-parallel-runners ./..." "Makefile wiring admits its bounded parallel golangci-lint children"
cat >"$state/want-families" <<'WANT_FAMILIES'
go run ./cmd/serf-namingcheck
go run ./cmd/serf-internalcheck
go run ./cmd/serf-docscheck
go generate ./appwire/...
git diff --exit-code -- docs/appwire-protocol.md
secret-scan repo
WANT_FAMILIES
if cmp -s "$state/want-families" "$state/families"; then
	ok "make lint retains all six existing lint families with exact arguments"
else
	bad "make lint changed the existing lint families or arguments"
	diff -u "$state/want-families" "$state/families" || :
fi
assert_eq "$(find "$tmp" -type f -name 'serf-lint-check.*' | wc -l | tr -d ' ')" "0" "healthy checks remove quiet-wrapper logs"

# The missing-gitleaks path is successful but degraded. Its existing warning is
# part of the secret-scan policy and must survive routine-success suppression.
out="$case_dir/make-secret-degraded.out"
if (
	cd "$repo" || exit 1
	TMPDIR="$tmp" PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_GITLEAKS_MISSING=1 make secret-scan
) >"$out" 2>&1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "missing-gitleaks secret scan remains successful"
assert_eq "$(cat "$out")" "warning: gitleaks not installed; skipping repo secret scan (install: https://github.com/gitleaks/gitleaks)" "missing-gitleaks warning remains exact and visible"
assert_not_has "$out" "secret-success-chatter" "routine secret-scan success chatter remains hidden"
assert_eq "$(find "$tmp" -type f -name 'serf-lint-check.*' | wc -l | tr -d ' ')" "0" "degraded secret scan removes its quiet-wrapper log"

: >"$state/families"
out="$case_dir/make-docs-failure.out"
if (
	cd "$repo" || exit 1
	TMPDIR="$tmp" PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_FAIL_DOCS=1 make lint-docs
) >"$out" 2>&1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "failed non-module lint family returns nonzero"; else bad "failed non-module lint family returns zero"; fi
assert_count "$out" "docs stdout diagnostic" "1" "failed non-module stdout is replayed completely once"
assert_count "$out" "docs stderr diagnostic" "1" "failed non-module stderr is replayed completely once"
assert_has "$out" "Error 9" "failed non-module check reports its original status"
assert_eq "$(find "$tmp" -type f -name 'serf-lint-check.*' | wc -l | tr -d ' ')" "0" "failed check removes its quiet-wrapper log"

echo "----"
echo "run-module-lint-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
