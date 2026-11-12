#!/usr/bin/env bash
# Drives the real run-ui-tests.sh cleanup and cancellable xcodebuild wait with
# fixture processes, so the Linux gate covers the trap and stop-budget logic a
# Mac run reaches only when it is cancelled or fails. Linux-only: it uses GNU
# date and exec -a; the macOS qualification lane exercises the real runner.
set -euo pipefail

if ! command -v ps >/dev/null 2>&1; then
	exit 77
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$here/../../../../.." && pwd -P)"
runner="$repo_root/scripts/native/ios/run-ui-tests.sh"

export TMPDIR="${TMPDIR:-/tmp}"
export RUN_UI_TESTS_LIB_ONLY=1
# Sourcing defines cleanup and run_xcodebuild and returns before the runner
# parses arguments or mints any state.
# shellcheck source=../../run-ui-tests.sh
source "$runner"

fixture_pids=()
cleanup_work() {
	for pid in ${fixture_pids[@]+"${fixture_pids[@]}"}; do
		kill -KILL "$pid" 2>/dev/null || true
	done
	scratch_rm
}
trap cleanup_work EXIT
scratch_dir work evener-ui-runner-test

fail() {
	echo "runner cleanup fixture: $*" >&2
	exit 1
}

now_ms() {
	echo $(($(date +%s%N) / 1000000))
}

wait_dead() {
	local pid="$1"
	local deadline=$((SECONDS + 5))
	while kill -0 "$pid" 2>/dev/null; do
		[ "$SECONDS" -lt "$deadline" ] || return 1
		sleep 0.05
	done
}

mkdir -p "$work/tmp" "$work/home" "$work/output"

# --- cleanup: owned daemons stopped, recycled pids left alone, budget kept ---

cat >"$work/cleanup-child.sh" <<'CHILD'
#!/usr/bin/env bash
set -euo pipefail
export RUN_UI_TESTS_LIB_ONLY=1
export TMPDIR="$WORK/tmp"
source "$RUNNER"
output="$WORK/output"
mkdir -p "$output"
workspace_link="$WORK/home/evener-qualification-workspace"
state_home=""
hub_pid=""
provider_pid=""
xcode_pid=""
daemons_stopped=0
trap cleanup EXIT
scratch_dir run_dir evener-ui-qualification
printf '%s\n' "$run_dir" >"$WORK/run-dir"
state_home="$run_dir/state"
workspace="$run_dir/workspace"
mkdir -p "$state_home/evener/run/logs" "$workspace"
ln -s "$workspace" "$workspace_link"
echo fixture >"$state_home/evener/run/logs/daemon-fixture.log"

# The owned daemon carries the run directory's basename in argv, as the hub's
# spawned `$run_dir/evener serve` does. The foreign one does not, so its pid
# stands in for one the hub log recorded before the pid was recycled.
bash -c 'exec -a "$1" sleep 30' owned "$run_dir/evener" &
owned_pid=$!
sleep 30 &
foreign_pid=$!
sleep 30 &
hub_pid=$!
sleep 30 &
provider_pid=$!
{
	printf '[hub] daemon session=owned pid=%s log=/fixture.log\n' "$owned_pid"
	printf '[hub] daemon session=foreign pid=%s log=/fixture.log\n' "$foreign_pid"
} >"$output/hub.log"
printf '%s %s %s %s\n' "$owned_pid" "$foreign_pid" "$hub_pid" "$provider_pid" >"$WORK/cleanup-pids"
exit 0
CHILD
chmod 700 "$work/cleanup-child.sh"
cleanup_status=0
started=$(now_ms)
RUNNER="$runner" WORK="$work" "$work/cleanup-child.sh" || cleanup_status=$?
elapsed=$(( $(now_ms) - started ))
[ "$cleanup_status" -eq 0 ] || fail "cleanup child exited $cleanup_status"
[ "$elapsed" -lt 5000 ] || fail "cleanup took ${elapsed}ms, over the parent's 5s grace"
read -r owned_pid foreign_pid hub_pid provider_pid <"$work/cleanup-pids"
fixture_pids+=("$foreign_pid")
run_dir="$(cat "$work/run-dir")"
[ ! -e "$work/home/evener-qualification-workspace" ] || fail "cleanup left the workspace link"
[ ! -e "$run_dir" ] || fail "cleanup left the scratch run directory"
wait_dead "$owned_pid" || fail "cleanup left the owned daemon running"
kill -0 "$foreign_pid" 2>/dev/null || fail "cleanup signalled an unrelated recycled pid"
wait_dead "$hub_pid" || fail "cleanup left the hub running"
wait_dead "$provider_pid" || fail "cleanup left the provider running"
[ -f "$work/output/daemon-fixture.log" ] || fail "cleanup did not copy the daemon log"
echo "ok cleanup stopped owned children inside the grace and spared a recycled pid"

# --- cancellation: TERM during the long xcodebuild runs cleanup at once ---

cat >"$work/fake-xcodebuild" <<'FAKE'
#!/bin/sh
# Ignore TERM so cleanup must spend its shared wait and KILL this child.
trap '' TERM
printf '%s\n' "$$" > "$FAKE_STARTED"
sleep 30
FAKE
chmod 700 "$work/fake-xcodebuild"

cat >"$work/cancel-child.sh" <<'CHILD'
#!/usr/bin/env bash
set -euo pipefail
export RUN_UI_TESTS_LIB_ONLY=1
export TMPDIR="$WORK/tmp"
source "$RUNNER"
output="$WORK/output-cancel"
mkdir -p "$output"
workspace_link="$WORK/home-cancel/evener-qualification-workspace"
state_home=""
hub_pid=""
provider_pid=""
xcode_pid=""
daemons_stopped=0
trap cleanup EXIT
trap 'exit 143' TERM
scratch_dir run_dir evener-ui-qualification
printf '%s\n' "$run_dir" >"$WORK/child-run-dir"
state_home="$run_dir/state"
run_xcodebuild "$output/xcodebuild.log" "$WORK/fake-xcodebuild" 30
echo "xcodebuild returned without a cancellation" >&2
exit 9
CHILD
chmod 700 "$work/cancel-child.sh"
export FAKE_STARTED="$work/xcode-started"
RUNNER="$runner" WORK="$work" "$work/cancel-child.sh" &
child_pid=$!
fixture_pids+=("$child_pid")
for _ in $(seq 1 100); do
	[ -f "$FAKE_STARTED" ] && break
	sleep 0.05
done
[ -f "$FAKE_STARTED" ] || fail "the fixture xcodebuild never started"
fake_xcode_pid="$(cat "$FAKE_STARTED")"

child_status=0
started=$(now_ms)
kill -TERM "$child_pid"
wait "$child_pid" || child_status=$?
elapsed=$(( $(now_ms) - started ))
[ "$child_status" -eq 143 ] || fail "cancel child exited $child_status, want the TERM trap's 143"
[ "$elapsed" -lt 5000 ] || fail "cancellation cleanup took ${elapsed}ms, over the parent's 5s grace"
run_dir="$(cat "$work/child-run-dir")"
[ ! -e "$run_dir" ] || fail "cancellation left the scratch run directory"
wait_dead "$fake_xcode_pid" || fail "cancellation left the fixture xcodebuild running"
echo "ok cancellation reached cleanup before the child's own wait and inside the grace"
