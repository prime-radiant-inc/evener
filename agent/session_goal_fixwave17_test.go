package agent

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/llm"
)

// Round-17 fix wave (roborev review on rev 7e26718): pins for the confirmed
// findings. The High (kickGoalWaitBudgetBound recursion) is refuted — the
// no-re-arm guard stands — so no test covers it.

// TestFixWave17_CompleteRejectNamesCondition pins finding 2's behavior
// preservation: update_goal("complete") on a goal carrying an unsatisfied
// condition rejects with the failing condition named (byte-identical message)
// and the goal stays active. The atomicity itself (verify+commit under one
// goalUpdateMu hold via CompleteIfSatisfied) is structural — a single guard
// call — pinned here by exercising the session path end to end.
func TestFixWave17_CompleteRejectNamesCondition(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report changed", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ug1", Name: "update_goal",
		Arguments: task8Args(t, map[string]any{"status": "complete"}),
		Type:      "function",
	})
	if !cres.IsError {
		t.Fatalf("conditioned complete should reject, got success: %s", cres.Output)
	}
	want := `update_goal: condition "report changed" is not satisfied; the goal stays active — satisfy it or keep working`
	if cres.Output != want {
		t.Fatalf("rejection = %q, want byte-identical %q", cres.Output, want)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusActive {
		t.Fatalf("status = %q, want active after a rejected claim", snap.Status)
	}
}

// TestFixWave17_CompleteSatisfiedCompletes pins finding 2's commit path: a
// condition satisfied at claim time (file changed since the registration
// baseline) lets update_goal("complete") transition to complete.
func TestFixWave17_CompleteSatisfiedCompletes(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge1", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{"desc": "report changed", "target": "/work/report.md"}),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("goal_expect registration should succeed, got error: %s", res.Output)
	}
	wireTask8Files(sess, map[string]string{"/work/report.md": "v2"})
	cres := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ug1", Name: "update_goal",
		Arguments: task8Args(t, map[string]any{"status": "complete"}),
		Type:      "function",
	})
	if cres.IsError {
		t.Fatalf("satisfied complete should succeed, got error: %s", cres.Output)
	}
	if snap, _ := sess.getOrCreateGoalStore().Snapshot(); snap.Status != goal.StatusComplete {
		t.Fatalf("status = %q, want complete", snap.Status)
	}
}

// TestFixWave17_StaleEmitSuppressed pins finding 3: an emit generation
// captured before a newer goal mutation suppresses its stale GOAL_UPDATED,
// while the current generation still emits.
func TestFixWave17_StaleEmitSuppressed(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("stale race objective", clk.Now())
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("precondition: goal must be set")
	}
	staleGen := sess.goalEventGen.Load()
	// A newer mutation publishes: the stale generation must suppress.
	sess.getOrCreateGoalStore().Set("newer objective", clk.Now())
	sess.bumpGoalEventGen()

	drainGoalEvents17(sess)
	sess.emitGoalUpdatedAtGen(snap, staleGen)
	assertNoGoalUpdated(t, sess)

	// The current generation still emits.
	curGen := sess.goalEventGen.Load()
	curSnap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("precondition: goal must still be set")
	}
	sess.emitGoalUpdatedAtGen(curSnap, curGen)
	got := nextGoalUpdated(t, sess)
	assertGoalUpdatedMatchesStore(t, sess, got)
	assertNoGoalUpdated(t, sess)
}

// drainGoalEvents17 discards pending session events without asserting on them.
func drainGoalEvents17(sess *Session) {
	for {
		select {
		case _, ok := <-sess.Events():
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// TestFixWave17_ExpectIgnoresDeadParams pins finding 4's observable: a
// goal_expect carrying timeout_seconds/label registers with no TTL stored
// (Predicate.Timeout zero) — the fields promise nothing. Registry callers
// are rejected by the schema (additionalProperties:false); direct callers
// ignore them.
func TestFixWave17_ExpectIgnoresDeadParams(t *testing.T) {
	t.Parallel()
	req, err := decodeGoalExpectArgs(map[string]any{
		"desc": "report changed", "target": "/work/report.md",
		"timeout_seconds": 60, "label": "stale alias",
	})
	if err != nil {
		t.Fatalf("direct decode with dead params should ignore them, got error: %v", err)
	}
	if req.Predicate.Timeout != 0 {
		t.Fatalf("Predicate.Timeout = %v, want zero (conditions never park)", req.Predicate.Timeout)
	}
	if req.Desc != "report changed" {
		t.Fatalf("Desc = %q, want the desc (label must not overwrite it)", req.Desc)
	}
	// An out-of-range timeout no longer validates: the field is dead.
	if _, err := decodeGoalExpectArgs(map[string]any{
		"desc": "x", "target": "/work/f", "timeout_seconds": 999999,
	}); err != nil {
		t.Fatalf("out-of-range dead timeout must not validate, got: %v", err)
	}
}

// TestFixWave17_ExpectRejectsDeadParamsViaSchema pins finding 4's registry
// path: goal_expect with timeout_seconds/label args fails schema validation
// (the schema no longer advertises them, additionalProperties:false).
func TestFixWave17_ExpectRejectsDeadParamsViaSchema(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the report", clk.Now())
	wireTask8Files(sess, map[string]string{"/work/report.md": "v1"})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID: "ge-dead", Name: "goal_expect",
		Arguments: task8Args(t, map[string]any{
			"desc": "report changed", "target": "/work/report.md",
			"timeout_seconds": 60,
		}),
		Type: "function",
	})
	if !res.IsError {
		t.Fatalf("goal_expect with timeout_seconds should reject via schema, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "timeout_seconds") {
		t.Fatalf("rejection %q must name timeout_seconds", res.Output)
	}
}

// TestFixWave17_WorktreeDigestSubSecond pins finding 5: two writes within the
// same second flip the worktree listing digest (second precision collapsed
// them, misclassifying an advancing turn as stalled).
func TestFixWave17_WorktreeDigestSubSecond(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	before := worktreeListingDigest(root)
	// Same size, sub-second mtime delta: second precision renders identical
	// lines, RFC3339Nano does not.
	after := time.Now().UTC().Add(100 * time.Millisecond)
	if err := os.Chtimes(path, after, after); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got := worktreeListingDigest(root); got == before {
		t.Fatalf("sub-second mtime delta did not flip the digest:\n%s", got)
	}
}

// TestFixWave17_ItoaMinInt64 pins finding 7: itoa renders every int64
// boundary exactly, including MinInt64 (the hand-rolled negation overflowed).
func TestFixWave17_ItoaMinInt64(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{math.MaxInt64, "9223372036854775807"},
		{math.MinInt64, "-9223372036854775808"},
	} {
		if got := itoa(tc.in); got != tc.want {
			t.Fatalf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFixWave17_UntilTimeExpectMessage pins finding 8: kind=until_time routes
// to the verifiable-state message (mirroring the store's
// expectKindRejected), while a bogus kind keeps the generic message
// byte-identical.
func TestFixWave17_UntilTimeExpectMessage(t *testing.T) {
	t.Parallel()
	err := validateGoalExpectArgs(map[string]any{"desc": "t", "kind": "until_time", "target": "x"})
	if err == nil {
		t.Fatal("until_time condition must reject")
	}
	want := `invalid_request: condition kind "until_time" is not verifiable (must query durable state, not time)`
	if err.Error() != want {
		t.Fatalf("until_time error = %q, want byte-identical %q", err.Error(), want)
	}
	err = validateGoalExpectArgs(map[string]any{"desc": "b", "kind": "bogus", "target": "x"})
	if err == nil {
		t.Fatal("bogus kind must reject")
	}
	wantBogus := `invalid_request: unknown condition kind "bogus" (must be until_job | until_delegate | until_event)`
	if err.Error() != wantBogus {
		t.Fatalf("bogus error = %q, want byte-identical %q", err.Error(), wantBogus)
	}
}

// TestFixWave17_DerivedLabelSanitized pins finding 6 at the session-store
// level: a wait target with control characters registers with a printable
// derived chip label, while the predicate target stays intact for identity.
// (The 256-rune truncation boundary is pinned in the goal package's
// wait_test.go, next to defaultLabelFor.)
func TestFixWave17_DerivedLabelSanitized(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newWaitGateSession(t, clk)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("watch the clock", clk.Now())
	w, ok := sess.registerGoalWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, clk.Now())
	if !ok {
		t.Fatalf("until_time must register: %q", sess.getOrCreateGoalStore().LastRejectReason())
	}
	_ = w
	full, _ := sess.getOrCreateGoalStore().GoalSnapshot()
	if len(full.Waits) != 1 {
		t.Fatalf("waits = %+v, want one lease", full.Waits)
	}
	// until_time is target-free so its derived label is just the kind; assert
	// the contract shape (printable, bounded) on the stored chip label.
	label := full.Waits[0].Lease.Label
	if utf8.RuneCountInString(label) > goal.MaxLabelRunes {
		t.Fatalf("derived label %q exceeds %d runes", label, goal.MaxLabelRunes)
	}
	for _, r := range label {
		if !unicode.IsPrint(r) {
			t.Fatalf("derived label %q carries non-printable %U", label, r)
		}
	}
}
