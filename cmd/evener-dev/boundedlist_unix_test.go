//go:build unix

package dev

import (
	"bytes"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A child that ignores SIGTERM is what the bound exists for: a `go list` wedged
// on a stalled cache volume does not answer the polite signal either.
const termProofChild = `trap "" TERM; sleep 60`

func TestBoundedAttemptPassesAFastChildsOutputThrough(t *testing.T) {
	var stderr bytes.Buffer
	result := runBoundedAttempt([]string{"sh", "-c", "echo one; echo two"}, 5*time.Second, time.Second, &stderr)
	if result.err != nil {
		t.Fatalf("err = %v, stderr = %q", result.err, stderr.String())
	}
	if result.timedOut {
		t.Fatal("timedOut = true for a child that finished at once")
	}
	if got := string(result.stdout); got != "one\ntwo\n" {
		t.Fatalf("stdout = %q, want %q", got, "one\ntwo\n")
	}
}

func TestBoundedAttemptStopsAGroupThatIgnoresTerm(t *testing.T) {
	var stderr bytes.Buffer
	start := time.Now()
	result := runBoundedAttempt([]string{"sh", "-c", termProofChild}, 200*time.Millisecond, 500*time.Millisecond, &stderr)
	if !result.timedOut {
		t.Fatalf("timedOut = false, want true; err = %v", result.err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the bound took %s, which is not a bound", elapsed)
	}
	// The group, not just the leader: the child ignored SIGTERM, so only the
	// escalation to SIGKILL can have emptied it.
	requireGroupGone(t, result.pgid)
}

func TestBoundedAttemptStopsAChildTheLeaderLeftBehind(t *testing.T) {
	var stderr bytes.Buffer
	// The leader exits at once, and the child it leaves behind ignores SIGTERM
	// and holds neither end of the pipe, so the wait returns with the group
	// still populated. Being reaped says nothing about the group.
	const orphanMaker = `sh -c 'trap "" TERM; sleep 60' >/dev/null 2>&1 & exit 0`
	result := runBoundedAttempt([]string{"sh", "-c", orphanMaker}, 5*time.Second, 300*time.Millisecond, &stderr)
	if result.timedOut {
		t.Fatalf("timedOut = true, want the leader's own prompt exit; stderr = %q", stderr.String())
	}
	if result.err != nil {
		t.Fatalf("err = %v, stderr = %q", result.err, stderr.String())
	}
	requireGroupGone(t, result.pgid)
}

// requireGroupGone waits for the process group to empty, which is the only
// evidence that the attempt stopped everything it started.
func requireGroupGone(t *testing.T, pgid int) {
	t.Helper()
	if pgid <= 1 {
		t.Fatalf("pgid = %d, so nothing could have been signalled", pgid)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still there after the attempt (kill answered %v)", pgid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoundedListRejectsANonPositiveGrace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedList([]string{"-grace", "0s", "--", "true"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for a grace that would SIGKILL at once", code)
	}
	if got := stderr.String(); !strings.Contains(got, "-grace") {
		t.Fatalf("stderr = %q, want it to name -grace", got)
	}
}

func TestBoundedListRetriesThenFailsWithADiagnostic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedList([]string{"-timeout", "200ms", "-attempts", "2", "-grace", "500ms", "--", "sh", "-c", termProofChild}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = 0 for a command that never finished; stderr = %q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "timed out after 200ms on each of 2 attempts") {
		t.Fatalf("stderr = %q, want it to name the budget and the attempts", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing: no attempt produced a list", stdout.String())
	}
}

func TestBoundedListKeepsAFailedCommandsStatusAndDoesNotRetry(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedList([]string{"-timeout", "5s", "-attempts", "3", "--", "sh", "-c", "echo broken >&2; exit 3"}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3: a command that decided something is not a timeout", code)
	}
	if got := stderr.String(); !strings.Contains(got, "broken") {
		t.Fatalf("stderr = %q, want the command's own diagnostic", got)
	}
	if strings.Count(stderr.String(), "broken") != 1 {
		t.Fatalf("stderr = %q, want one attempt only", stderr.String())
	}
}

func TestBoundedListWritesTheListOnSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := boundedList([]string{"-timeout", "5s", "-attempts", "2", "--", "sh", "-c", "echo primeradiant.com/evener"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "primeradiant.com/evener\n" {
		t.Fatalf("stdout = %q", got)
	}
}
