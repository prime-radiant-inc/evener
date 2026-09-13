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
	if result.pgid <= 1 {
		t.Fatalf("pgid = %d, so nothing could have been signalled", result.pgid)
	}
	// The group, not just the leader: the child ignored SIGTERM, so only the
	// escalation to SIGKILL can have emptied it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(-result.pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still there after the bound (kill answered %v)", result.pgid, err)
		}
		time.Sleep(20 * time.Millisecond)
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
