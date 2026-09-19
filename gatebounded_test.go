package evener_test

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// gateBoundedLib holds the bounded process runner and process-tree stopper the
// gate uses for its `go list` enumerations. Testing it through the library, not
// by inspecting run-module-tests.sh's text, is what lets these cases drive the
// real failure modes: a command that finishes at the deadline, a command that
// never finishes, and a child that refuses to die.
const gateBoundedLib = "scripts/lib/gate-bounded.sh"

// runBoundedCase sources the library in a bash shell (the library uses SECONDS
// and declare) and runs script, returning its combined output.
func runBoundedCase(t *testing.T, script string) string {
	t.Helper()
	if _, err := os.Stat(gateBoundedLib); err != nil {
		t.Fatalf("stat %s: %v", gateBoundedLib, err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = envOverride([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -c:\n%s\nexit: %v\noutput:\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRunBoundedReportsSuccess(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
export EVENER_STOP_TREE_GRACE_SECONDS=1
dir=$(mktemp -d)
run_bounded 10 unit test "$dir/log" bash -c 'echo hello'
rc=$?
printf 'rc=%s status=%s out=%s\n' "$rc" "$(cat "$dir/log.status")" "$(cat "$dir/log")"
rm -rf "$dir"
`)
	if want := "rc=0 status=0 out=hello"; got != want {
		t.Fatalf("run_bounded success = %q, want %q", got, want)
	}
}

func TestRunBoundedReplaysFailure(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
export EVENER_STOP_TREE_GRACE_SECONDS=1
dir=$(mktemp -d)
run_bounded 10 unit test "$dir/log" bash -c 'echo boom >&2; exit 3'
rc=$?
printf 'rc=%s\n' "$rc"
rm -rf "$dir"
`)
	if !strings.Contains(got, "rc=3") || !strings.Contains(got, "boom") {
		t.Fatalf("run_bounded failure = %q, want rc=3 and the replayed stderr", got)
	}
}

// TestRunBoundedRejectsAStatusPublishedAfterTheDeadline pins the rule that a
// status file absent at the deadline is final: a command that ignores TERM and
// exits zero during the grace must not turn the timed-out run into a success and
// let the caller consume output produced after the bound.
func TestRunBoundedRejectsAStatusPublishedAfterTheDeadline(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
export EVENER_STOP_TREE_GRACE_SECONDS=3
. `+gateBoundedLib+`
dir=$(mktemp -d)
run_bounded 1 unit test "$dir/log" bash -c 'trap "" TERM; sleep 1.2; exit 0'
printf 'rc=%s status=%s\n' "$?" "$(cat "$dir/log.status" 2>/dev/null)"
rm -rf "$dir"
`)
	if !strings.Contains(got, "rc=1") {
		t.Fatalf("run_bounded = %q, want rc=1 (the command finished only after the deadline)", got)
	}
}

// TestRunBoundedTimesOutWithoutWaiting is the bound the gate claims: a command
// that outlives it is stopped, the diagnostic hook runs, and run_bounded
// returns promptly. It must not wait on the stopped process, or a survivor in
// an uninterruptible wait would hold the gate past the bound it advertises.
func TestRunBoundedTimesOutWithoutWaiting(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
export EVENER_STOP_TREE_GRACE_SECONDS=1
dir=$(mktemp -d)
run_bounded_timeout_diagnostic() { echo "diagnostic:$1:$2"; }
start=$SECONDS
run_bounded 1 "slow step" agent "$dir/log" bash -c 'sleep 60'
rc=$?
printf 'rc=%s elapsed=%s status_present=%s\n' "$rc" "$((SECONDS - start))" "$([ -f "$dir/log.status" ] && echo yes || echo no)"
rm -rf "$dir"
`)
	if !strings.Contains(got, "rc=1") {
		t.Fatalf("run_bounded timeout = %q, want rc=1", got)
	}
	if !strings.Contains(got, "diagnostic:slow step:1") {
		t.Fatalf("run_bounded timeout = %q, want the diagnostic hook called", got)
	}
	// Bound plus the 5s TERM grace, with slack; it must not hang on the child.
	var elapsed int
	for f := range strings.FieldsSeq(got) {
		if v, ok := strings.CutPrefix(f, "elapsed="); ok {
			elapsed, _ = strconv.Atoi(v)
		}
	}
	if elapsed > 12 {
		t.Fatalf("run_bounded timeout took %ds, want it bounded near 1s+grace", elapsed)
	}
}

// pollGoneSnippet waits, bounded, for a pid to disappear. A killed child whose
// parent is gone is reparented and reaped by whatever adopts it, which may not
// reap promptly, so a zombie counts as terminated: kill -0 would still succeed
// for it, and PID 1 in a container is not obliged to reap.
const pollGoneSnippet = `
poll_gone() {
	for _ in $(seq 1 100); do
		st=$(ps -o stat= -p "$1" 2>/dev/null || true)
		case "$st" in ''|Z*) return 0;; esac
		sleep 0.05
	done
	return 1
}
`

// TestRunBoundedLetsACommandHandleTERM pins the wrapper's TERM disposition: a
// caught no-op keeps the wrapper alive without making the command inherit
// SIG_IGN, so a command that handles TERM actually runs its handler. The
// command's handler writes a marker, so the test fails if the handler never
// executed -- a command that merely died on the default TERM would also finish
// inside the grace and must not pass this.
func TestRunBoundedLetsACommandHandleTERM(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
export EVENER_STOP_TREE_GRACE_SECONDS=4
. `+gateBoundedLib+`
dir=$(mktemp -d)
cat > "$dir/cmd.sh" <<'CMD'
trap 'printf handled > "$MARK"; exit 0' TERM
while :; do sleep 0.05; done
CMD
start=$SECONDS
MARK="$dir/handled" run_bounded 1 unit test "$dir/log" bash "$dir/cmd.sh"
printf 'rc=%s elapsed=%s marker=%s\n' "$?" "$((SECONDS - start))" "$(cat "$dir/handled" 2>/dev/null)"
rm -rf "$dir"
`)
	if !strings.Contains(got, "rc=1") {
		t.Fatalf("run_bounded = %q, want rc=1", got)
	}
	if !strings.Contains(got, "marker=handled") {
		t.Fatalf("run_bounded = %q, want the command's own TERM handler to have run", got)
	}
	var elapsed int
	for f := range strings.FieldsSeq(got) {
		if v, ok := strings.CutPrefix(f, "elapsed="); ok {
			elapsed, _ = strconv.Atoi(v)
		}
	}
	if elapsed >= 4 {
		t.Fatalf("run_bounded = %q, want the command's TERM handler to end it well before the 4s grace", got)
	}
}

// TestEnumerationArgvCarriesTheSelectionFlags and
// TestRunEnumerationHandsTheFlagsToGoList cover the wiring that feeds the
// derived selection flags into the enumeration: a regression there would
// silently reintroduce the missing-package bug with every helper test still
// green.
func TestEnumerationArgvCarriesTheSelectionFlags(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
list_flags=(-race -tags=integration)
enumeration_argv
`)
	if want := "go\nlist\n-race\n-tags=integration\n./..."; got != want {
		t.Fatalf("enumeration_argv = %q, want %q", got, want)
	}
}

func TestRunEnumerationHandsTheFlagsToGoList(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
list_flags=(-race -tags=integration)
run_bounded() { shift 4; printf 'argv:%s\n' "$*"; }
run_enumeration . /tmp/unused
`)
	if want := "argv:go list -race -tags=integration ./..."; got != want {
		t.Fatalf("run_enumeration argv = %q, want %q", got, want)
	}
}

// TestStopCommandReapsALateFork is the reason run_bounded uses a process group:
// a parent that forks a child after cleanup has begun puts that child behind any
// PID snapshot, but the child is still inside the command's process group, so
// one group KILL reaches it. The parent forks only when cleanup TERMs it, so the
// fork is ordered after cleanup began -- not at a fixed delay that could land
// before the snapshot or after the grace. The job is launched under job control
// to lead its own group, exactly as run_bounded launches it.
func TestStopCommandReapsALateFork(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
`+pollGoneSnippet+`
export EVENER_STOP_TREE_GRACE_SECONDS=2
dir=$(mktemp -d)
cat > "$dir/child.sh" <<'CHILD'
trap "" TERM
exec sleep 60
CHILD
cat > "$dir/parent.sh" <<'PARENT'
trap 'bash "$DIR/child.sh" & echo $! > "$LATE"; exit 0' TERM
: > "$READY"
while :; do sleep 0.05; done
PARENT
set -m
DIR="$dir" READY="$dir/ready" LATE="$dir/late" bash "$dir/parent.sh" &
pid=$!
set +m
ready=no
for _ in $(seq 1 100); do [ -f "$dir/ready" ] && { ready=yes; break; }; sleep 0.05; done
if [ "$ready" != yes ]; then
	stop_command "$pid"
	rm -rf "$dir"
	echo "parent never signalled readiness"
	exit 1
fi
pgid=$(ps -o pgid= -p "$pid" 2>/dev/null | tr -d ' ')
group_leader=no
[ -n "$pgid" ] && [ "$pgid" = "$pid" ] && group_leader=yes
stop_command "$pid"
for _ in $(seq 1 100); do [ -s "$dir/late" ] && break; sleep 0.05; done
late=$(cat "$dir/late" 2>/dev/null)
state=unknown
[ -n "$late" ] && { if poll_gone "$late"; then state=gone; else state=alive; fi; }
printf 'group_leader=%s late=%s state=%s\n' "$group_leader" "$late" "$state"
kill -KILL -"$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || :
kill -KILL "$late" 2>/dev/null || :
wait "$pid" 2>/dev/null || :
rm -rf "$dir"
`)
	if !strings.Contains(got, "group_leader=yes") {
		t.Fatalf("stop_command = %q, want the job to lead its own process group so the group kill is what reaps the fork", got)
	}
	if !strings.Contains(got, "state=gone") {
		t.Fatalf("stop_command = %q, want the child forked during cleanup reaped by the group kill", got)
	}
}

// TestStopProcessTreeReapsAChildWhenTheParentExits pins the accumulated list: a
// parent that dies on TERM leaves behind a child that ignores TERM, which is
// reparented and invisible to every later rescan. Only a list kept from when
// the child was still discoverable reaches it. The child installs its TERM
// handler and signals readiness before cleanup starts, so this cannot pass by
// killing a child that had not yet ignored TERM.
func TestStopProcessTreeReapsAChildWhenTheParentExits(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
`+pollGoneSnippet+`
export EVENER_STOP_TREE_GRACE_SECONDS=1
dir=$(mktemp -d)
cat > "$dir/child.sh" <<'CHILD'
trap "" TERM
: > "$CHILD_READY"
exec sleep 60
CHILD
cat > "$dir/parent.sh" <<'PARENT'
bash "$DIR/child.sh" &
echo $! > "$LATE"
wait
PARENT
DIR="$dir" LATE="$dir/late" CHILD_READY="$dir/child-ready" bash "$dir/parent.sh" &
pid=$!
ready=no
for _ in $(seq 1 100); do [ -s "$dir/late" ] && [ -f "$dir/child-ready" ] && { ready=yes; break; }; sleep 0.05; done
if [ "$ready" != yes ]; then
	stop_process_tree "$pid"
	rm -rf "$dir"
	echo "child never signalled readiness"
	exit 1
fi
late=$(cat "$dir/late" 2>/dev/null)
stop_process_tree "$pid"
state=unknown
[ -n "$late" ] && { if poll_gone "$late"; then state=gone; else state=alive; fi; }
printf 'late=%s state=%s\n' "$late" "$state"
kill -KILL "$pid" "$late" 2>/dev/null || :
wait "$pid" 2>/dev/null || :
rm -rf "$dir"
`)
	if !strings.Contains(got, "state=gone") {
		t.Fatalf("stop_process_tree = %q, want the orphaned TERM-ignoring child KILLed", got)
	}
}

// TestStopProcessTreeEscalatesToKill pins the escalation: a child that ignores
// TERM is KILLed after the grace, and stop_process_tree returns once the grace
// expires rather than waiting on a process that will not die. The child signals
// readiness after installing its TERM handler, so the test cannot race the
// install and see a plain SIGTERM death (143) instead of the KILL (137).
func TestStopProcessTreeEscalatesToKill(t *testing.T) {
	got := runBoundedCase(t, `
set -uo pipefail
. `+gateBoundedLib+`
export EVENER_STOP_TREE_GRACE_SECONDS=1
dir=$(mktemp -d)
READY="$dir/ready" bash -c 'trap "" TERM; : > "$READY"; exec sleep 60' &
pid=$!
ready=no
for _ in $(seq 1 100); do
	[ -f "$dir/ready" ] && { ready=yes; break; }
	sleep 0.05
done
if [ "$ready" != yes ]; then
	# Kill and reap the child before giving up: it holds CombinedOutput's pipe,
	# so leaving it alive would block this test until its 60s sleep ended.
	kill -KILL "$pid" 2>/dev/null || :
	wait "$pid" 2>/dev/null || :
	rm -rf "$dir"
	echo "child never installed its TERM handler"
	exit 1
fi
start=$SECONDS
stop_process_tree "$pid"
elapsed=$((SECONDS - start))
wait "$pid" 2>/dev/null
printf 'wait_status=%s elapsed=%s\n' "$?" "$elapsed"
rm -rf "$dir"
`)
	if !strings.Contains(got, "wait_status=137") {
		t.Fatalf("stop_process_tree = %q, want the TERM-ignoring child KILLed (137)", got)
	}
}
