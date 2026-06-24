//go:build !short

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/goal"
)

// driveGoalToTerminal runs ProcessInput, then pumps further ProcessInput calls
// while the goal is still active and the session is not closed, bounded by
// maxPumps. In practice the drain-loop gate runs all continuations inside the
// first ProcessInput call, so one call usually suffices; the pump is a safety net
// for a turn that returns to idle with the goal still active (e.g. a transient
// interrupt). It returns once the goal is terminal or the bound is hit.
func driveGoalToTerminal(ctx context.Context, t *testing.T, sess *Session, first string, maxPumps int) {
	t.Helper()
	input := first
	for i := 0; i < maxPumps; i++ {
		if _, err := sess.ProcessInput(ctx, input, nil); err != nil {
			t.Fatalf("ProcessInput (pump %d): %v", i, err)
		}
		input = "continue"
		snap, ok := sess.getOrCreateGoalStore().Snapshot()
		if !ok || snap.Status != goal.StatusActive {
			return
		}
	}
}

func goalEndedEvents(evs []events.SessionEvent) []events.GoalEndedData {
	var out []events.GoalEndedData
	for _, ev := range evs {
		if ev.Kind != events.EventGoalEnded {
			continue
		}
		if d, ok := ev.Data.(events.GoalEndedData); ok {
			out = append(out, d)
		}
	}
	return out
}

// TestGoalLive_MultiTurnCompletion is the live proof that the goal loop drives a
// real cheap model across at least one continuation to a verified completion. The
// objective structurally requires two ordered steps (b.txt depends on a.txt's
// byte count), so it cannot be satisfied in a single shortcut turn.
//
// Asserts: count(EventGoalContinuation) >= 1 (the loop continued — the single most
// important assertion), exactly one EventGoalEnded{Status:"complete"}, both files
// exist with the right contents, and the terminal report reached the stream.
func TestGoalLive_MultiTurnCompletion(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)
	workDir := sess.env.WorkingDirectory()
	aPath := filepath.Join(workDir, "a.txt")
	bPath := filepath.Join(workDir, "b.txt")

	objective := "Create the file a.txt containing exactly the text `seed` (4 bytes, no trailing " +
		"newline). Then, only after a.txt exists, create the file b.txt whose contents are exactly " +
		"the number of bytes in a.txt (as decimal digits, no other text). Both files go in the " +
		"current working directory. Verify a.txt before computing b.txt."
	sess.getOrCreateGoalStore().Set(objective, time.Now())

	collectEvents := drainEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	driveGoalToTerminal(ctx, t, sess, "Begin working toward the goal.", 8)
	sess.Close()
	evs := collectEvents()

	if got := countGoalContinuations(evs); got < 1 {
		t.Fatalf("count(EventGoalContinuation) = %d, want >= 1 (the loop must have continued)", got)
	}

	ended := goalEndedEvents(evs)
	if len(ended) != 1 {
		t.Fatalf("EventGoalEnded count = %d, want exactly 1 (events: %+v)", len(ended), ended)
	}
	if ended[0].Status != "complete" {
		t.Fatalf("EventGoalEnded.Status = %q, want %q (reason=%q)", ended[0].Status, "complete", ended[0].Reason)
	}

	aData, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("a.txt not created: %v", err)
	}
	if strings.TrimRight(string(aData), "\r\n") != "seed" {
		t.Fatalf("a.txt content = %q, want \"seed\"", string(aData))
	}
	bData, err := os.ReadFile(bPath)
	if err != nil {
		t.Fatalf("b.txt not created: %v", err)
	}
	wantBytes := strconv.Itoa(len(aData))
	if strings.TrimRight(string(bData), "\r\n") != wantBytes {
		t.Fatalf("b.txt content = %q, want %q (the byte count of a.txt)", string(bData), wantBytes)
	}

	t.Logf("LIVE PROOF: continuations=%d, EventGoalEnded{Status:%q Iterations:%d}; a.txt=%q b.txt=%q",
		countGoalContinuations(evs), ended[0].Status, ended[0].Iterations, string(aData), string(bData))
}

// TestGoalLive_ImpossibleAutoBlocks is the live proof of the no-progress breaker:
// an objective that cannot be completed must auto-block within NoProgressLimit+1
// continuation turns, emitting exactly one EventGoalEnded{Status:"blocked"}.
//
// The error output the failing turns may produce is captured to keep go test
// output pristine.
func TestGoalLive_ImpossibleAutoBlocks(t *testing.T) {
	t.Parallel()
	skipWithoutAPIKey(t)
	sess := integrationSession(t)

	// The agent routes all failure output through its events channel (collected
	// by drainEvents), not through log/stdout/stderr, so go test output stays
	// pristine even as the impossible-objective turns fail their reads.

	objective := "Read the file /nonexistent/secret-key-vault/master.key (it does not exist and you " +
		"cannot create it) and report its exact contents. There is no substitute; only the real " +
		"contents of that exact path satisfy this goal."
	sess.getOrCreateGoalStore().Set(objective, time.Now())

	collectEvents := drainEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	driveGoalToTerminal(ctx, t, sess, "Begin working toward the goal.", goal.NoProgressLimit+3)
	sess.Close()
	evs := collectEvents()

	ended := goalEndedEvents(evs)
	if len(ended) != 1 {
		t.Fatalf("EventGoalEnded count = %d, want exactly 1 (events: %+v)", len(ended), ended)
	}
	if ended[0].Status != "blocked" {
		t.Fatalf("EventGoalEnded.Status = %q, want %q", ended[0].Status, "blocked")
	}
	// The breaker (or a model self-declaring blocked) must fire within the bound:
	// NoProgressLimit+1 continuation turns (Iterations counts continuation turns).
	if ended[0].Iterations > goal.NoProgressLimit+1 {
		t.Fatalf("blocked after %d continuation turns, want <= %d", ended[0].Iterations, goal.NoProgressLimit+1)
	}

	t.Logf("LIVE PROOF: blocked after %d continuation turns, reason=%q", ended[0].Iterations, ended[0].Reason)
}
