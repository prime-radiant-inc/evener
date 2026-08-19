package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// This file drives the loop guard through consumeModelStream itself (issue
// #94's integration point: "agent/session_stream.go region"), reconstructing
// the documented runaway shapes as synthetic streams. The captured streams are
// not in the repo; these are honest synthetic stand-ins built to the
// documented shapes, not replays.
//
// Every scenario here runs consumeModelStream in a goroutine (started before
// or concurrently with feeding events, so a fixture larger than
// llm.NewChanStream's 128-event buffer never deadlocks the producer) with a
// bounded timeout as a tripwire against a genuine hang. Every feed closure
// calls st.CloseSend() once it is done sending, even the ones that
// deliberately send only a PARTIAL fixture (fewer calls than the shape's
// documented total): consumeModelStream's own deferred st.Close() blocks on
// the producer side closing (llm.ChanStream.Close's documented contract), so
// withholding CloseSend would hang consumeModelStream in its own cleanup
// regardless of whether the guard tripped correctly -- a false failure, not
// a signal. A partial feed plus an early CloseSend still proves the guard
// stopped consuming early: an un-tripped implementation drains the partial
// channel, sees it closed, and returns the "stream ended without finish
// event" error instead of hanging, which the tests below assert against
// directly (err == nil) rather than relying on a timeout to catch it.

// runConsumeModelStream starts consumeModelStream on st in a goroutine, then
// runs feed (typically a series of st.Send calls, ending in st.CloseSend())
// on the calling goroutine, then waits up to 5s for consumeModelStream to
// return -- failing the test on timeout as a last-resort tripwire, not the
// primary correctness signal. Starting the consumer before or during feed
// means a fixture larger than the stream's buffered channel capacity cannot
// deadlock: Send blocks only until the concurrently-running consumer drains
// it.
func runConsumeModelStream(t *testing.T, sess *Session, req llm.Request, st llm.Stream, feed func()) (sessionModelResponse, attemptObservation, error) {
	t.Helper()
	type outcome struct {
		resp sessionModelResponse
		obs  attemptObservation
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, obs, err := sess.consumeModelStream(context.Background(), req, st)
		done <- outcome{resp, obs, err}
	}()
	if feed != nil {
		feed()
	}
	select {
	case o := <-done:
		return o.resp, o.obs, o.err
	case <-time.After(5 * time.Second):
		t.Fatal("consumeModelStream did not return; the guard did not stop the stream")
		return sessionModelResponse{}, attemptObservation{}, nil
	}
}

// sendToolCall writes a minimal Start+End pair for one completed tool call --
// the shape TestConsumeModelStream_Observation_ToolCallEndOnlyThenError pins
// as a real provider emission (google's adapter skips Delta entirely).
func sendToolCall(st *llm.ChanStream, id, name, args string) {
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: id, Name: name}})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: id, Arguments: []byte(args)}})
}

// cyclePattern is the primary captured incident's shape: a k=3 cycle of
// (manage_worktree, task_list, communicate).
func cyclePattern() []toolSig {
	return []toolSig{
		{"manage_worktree", `{"op":"list"}`},
		{"task_list", `{}`},
		{"communicate", `{"message":"still working","end_turn":false}`},
	}
}

// TestConsumeModelStream_LoopGuard_CycleShape is fixture (a): (manage_worktree,
// task_list, communicate) repeating -- a k=3 cycle -- reconstructing the
// primary captured incident (228 calls from 4 argument blocks, x76). The
// fixture only sends 20 calls' worth of events (well short of 76 repeats): if
// the guard works, it never needs the rest.
func TestConsumeModelStream_LoopGuard_CycleShape(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	pattern := cyclePattern()
	const sent = 20 // well past the 15-call trip point, short of the real incident's 228
	// Sends only a partial fixture (20 of the shape's 228 calls), then closes
	// the producer: consumeModelStream must stop consuming — and act on the
	// trip — using only the first 15, never touching (let alone needing) the
	// remaining 5 this fixture still provides.
	resp, _, err := runConsumeModelStream(t, sess, req, st, func() {
		for i := range sent {
			p := pattern[i%3]
			sendToolCall(st, callID(i), p.name, p.args)
		}
		st.CloseSend()
	})
	if err != nil {
		t.Fatalf("err = %v, want nil (a tier-1 trip forces a clean finish, not an error)", err)
	}
	calls := resp.Response.ToolCalls()
	if len(calls) != 15 {
		t.Fatalf("got %d tool calls, want exactly 15 (trip fires at the 5th repeat of the 3-call cycle)", len(calls))
	}
	for i, c := range calls {
		want := pattern[i%3]
		if c.Name != want.name || string(c.Arguments) != want.args {
			t.Fatalf("call %d = %+v, want name=%q args=%q (no truncation/corruption at the cut point)", i, c, want.name, want.args)
		}
	}
	if resp.Response.Finish.Reason != llm.FinishReasonToolCalls {
		t.Fatalf("Finish.Reason = %q, want %q (a legal boundary: calls are complete, not cut mid-arguments)", resp.Response.Finish.Reason, llm.FinishReasonToolCalls)
	}

	sess.mu.Lock()
	nudge := sess.pendingStreamLoopNudge
	streak := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if nudge == "" {
		t.Fatal("pendingStreamLoopNudge is empty, want a nudge naming the repeated call")
	}
	if !containsAll(nudge, "manage_worktree", "task_list", "communicate") {
		t.Fatalf("nudge = %q, want it to name the repeated calls", nudge)
	}
	if streak != 1 {
		t.Fatalf("streamLoopTripStreak = %d, want 1 (first trip)", streak)
	}
}

// TestConsumeModelStream_LoopGuard_CeilingShape is fixture (b): 83 calls, 48
// distinct signatures, max repeat 12, built with no period-1..5 cycle
// anywhere (see buildEightyThreeCallNoCycleFixture). Records which detector
// actually catches it.
func TestConsumeModelStream_LoopGuard_CeilingShape(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	sigs := buildEightyThreeCallNoCycleFixture(t)
	resp, _, err := runConsumeModelStream(t, sess, req, st, func() {
		for i, s := range sigs {
			sendToolCall(st, callID(i), s.name, s.args)
		}
		st.CloseSend()
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	calls := resp.Response.ToolCalls()
	if len(calls) != loopGuardRawCeiling {
		t.Fatalf("got %d tool calls, want exactly %d (the ceiling, since this fixture has no short cycle)", len(calls), loopGuardRawCeiling)
	}
	sess.mu.Lock()
	nudge := sess.pendingStreamLoopNudge
	sess.mu.Unlock()
	if !containsAll(nudge, "50") {
		t.Fatalf("nudge = %q, want it to name the ceiling count", nudge)
	}
	t.Logf("shape (b) 83-call/48-distinct/max-repeat-12 fixture: caught by the CEILING detector at call %d, not the cycle detector", len(calls))
}

// TestConsumeModelStream_LoopGuard_EighteenDistinctCalls_NoTrip is fixture
// (d): a legitimate round of 18 distinct tool calls must complete normally,
// with a real StreamEventFinish -- the field's documented false positive
// (odysseus #3185).
func TestConsumeModelStream_LoopGuard_EighteenDistinctCalls_NoTrip(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	resp, _, err := runConsumeModelStream(t, sess, req, st, func() {
		for i := range 18 {
			sendToolCall(st, callID(i), distinctToolName(i), `{"n":`+itoaTest(i)+`}`)
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonToolCalls}})
		st.CloseSend()
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(resp.Response.ToolCalls()) != 18 {
		t.Fatalf("got %d tool calls, want 18 (nothing truncated)", len(resp.Response.ToolCalls()))
	}
	sess.mu.Lock()
	nudge := sess.pendingStreamLoopNudge
	streak := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if nudge != "" {
		t.Fatalf("pendingStreamLoopNudge = %q, want empty: 18 distinct calls must never trip", nudge)
	}
	if streak != 0 {
		t.Fatalf("streamLoopTripStreak = %d, want 0", streak)
	}
}

// TestConsumeModelStream_LoopGuard_DefersUntilParallelCallCloses is the
// "never cut mid-arguments" safety property applied to the case the plain
// per-call check misses: a trip detected at one tool call's End must not
// force a finish while a DIFFERENT, still-open parallel call (Started but
// not yet Ended -- real providers stream parallel tool calls with
// interleaved deltas across multiple call IDs) is in flight, or the forced
// finish would freeze that call's arguments mid-stream into the response.
func TestConsumeModelStream_LoopGuard_DefersUntilParallelCallCloses(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	pattern := cyclePattern()
	resp, _, err := runConsumeModelStream(t, sess, req, st, func() {
		// 14 calls of the cycle (not yet tripped: need=15).
		for i := range 14 {
			p := pattern[i%3]
			sendToolCall(st, callID(i), p.name, p.args)
		}
		// Open a distinct, parallel call and leave it mid-arguments.
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: "extra", Name: "long_running_call"}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: "extra", Arguments: []byte(`{"partial":`)}})
		// The 15th cycle call: this is where the trip would fire, but "extra" is
		// still open.
		p := pattern[14%3]
		sendToolCall(st, callID(14), p.name, p.args)
		// More time passes for the parallel call, still open.
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: "extra", Arguments: []byte(`true}`)}})
		// Now it closes -- this is the first legal boundary after the trip.
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: "extra", Arguments: []byte(`{"partial":true}`)}})
		st.CloseSend()
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	calls := resp.Response.ToolCalls()
	if len(calls) != 16 {
		t.Fatalf("got %d tool calls, want exactly 16 (15 cycle calls + the parallel call, all complete)", len(calls))
	}
	for _, c := range calls {
		if c.ID == "extra" {
			if string(c.Arguments) != `{"partial":true}` {
				t.Fatalf(`"extra" call arguments = %q, want the COMPLETE %q -- a forced finish while it was still open would have frozen it mid-argument`, c.Arguments, `{"partial":true}`)
			}
		}
	}
	sess.mu.Lock()
	nudge := sess.pendingStreamLoopNudge
	sess.mu.Unlock()
	if nudge == "" {
		t.Fatal("pendingStreamLoopNudge is empty, want the deferred trip to still fire once the parallel call closes")
	}
}

// TestConsumeModelStream_LoopGuard_SecondConsecutiveTrip_HardStops pins the
// two-tier action: "first trip aborts the stream and injects a nudge naming
// the repeated call; second trip hard-stops." The first response trips and
// gets a soft, error-free forced finish (tier 1); when the very next
// response ALSO trips, it must return a non-retryable error instead of
// another soft finish (tier 2) -- and that error must be excluded from the
// model-fallback walk, or the hard stop would simply re-run the runaway
// against every configured fallback model.
func TestConsumeModelStream_LoopGuard_SecondConsecutiveTrip_HardStops(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}

	pattern := cyclePattern()
	feedTrip := func(st *llm.ChanStream) func() {
		return func() {
			for i := range 15 {
				p := pattern[i%3]
				sendToolCall(st, callID(i), p.name, p.args)
			}
			st.CloseSend()
		}
	}

	// First response: tier 1, soft.
	st1 := llm.NewChanStream(nil)
	_, _, err1 := runConsumeModelStream(t, sess, req, st1, feedTrip(st1))
	if err1 != nil {
		t.Fatalf("first trip: err = %v, want nil", err1)
	}
	sess.mu.Lock()
	streak1 := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if streak1 != 1 {
		t.Fatalf("streak after first trip = %d, want 1", streak1)
	}

	// Second, consecutive response: tier 2, hard stop.
	st2 := llm.NewChanStream(nil)
	_, obs2, err2 := runConsumeModelStream(t, sess, req, st2, feedTrip(st2))
	if err2 == nil {
		t.Fatal("second consecutive trip: err = nil, want a hard-stop error")
	}
	var le llm.Error
	if !errors.As(err2, &le) {
		t.Fatalf("second trip error %v does not satisfy llm.Error", err2)
	}
	if le.Retryable() {
		t.Fatalf("second trip error is Retryable() == true, want false (retrying replays the same runaway pattern)")
	}
	if llm.Classify(err2) != llm.ErrorClassPermanent {
		t.Fatalf("Classify(err2) = %v, want ErrorClassPermanent", llm.Classify(err2))
	}
	var loopStop *streamLoopHardStopError
	if !errors.As(err2, &loopStop) {
		t.Fatalf("second trip error %v is not a *streamLoopHardStopError; without that marker the fallback chain re-runs the runaway on every fallback model", err2)
	}
	_ = obs2
	sess.mu.Lock()
	streak2 := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if streak2 != 2 {
		t.Fatalf("streak after second trip = %d, want 2", streak2)
	}
}

// TestConsumeModelStream_LoopGuard_CleanRoundResetsStreak proves the streak
// is about CONSECUTIVE trips, not a lifetime count: a clean round between two
// tripped ones must reset it, so a session that occasionally loops but
// always recovers is never hard-stopped.
func TestConsumeModelStream_LoopGuard_CleanRoundResetsStreak(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	pattern := cyclePattern()
	feedTrip := func(st *llm.ChanStream) func() {
		return func() {
			for i := range 15 {
				p := pattern[i%3]
				sendToolCall(st, callID(i), p.name, p.args)
			}
			st.CloseSend()
		}
	}
	feedClean := func(st *llm.ChanStream) func() {
		return func() {
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "done"})
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &llm.FinishReason{Reason: llm.FinishReasonStop}})
			st.CloseSend()
		}
	}

	st1 := llm.NewChanStream(nil)
	if _, _, err := runConsumeModelStream(t, sess, req, st1, feedTrip(st1)); err != nil {
		t.Fatalf("first trip: err = %v, want nil", err)
	}
	st2 := llm.NewChanStream(nil)
	if _, _, err := runConsumeModelStream(t, sess, req, st2, feedClean(st2)); err != nil {
		t.Fatalf("clean round: err = %v, want nil", err)
	}
	sess.mu.Lock()
	streak := sess.streamLoopTripStreak
	sess.mu.Unlock()
	if streak != 0 {
		t.Fatalf("streak after a clean round = %d, want 0 (streak resets)", streak)
	}

	// A trip AFTER the clean round must be treated as a first trip again,
	// not a second consecutive one.
	st3 := llm.NewChanStream(nil)
	if _, _, err := runConsumeModelStream(t, sess, req, st3, feedTrip(st3)); err != nil {
		t.Fatalf("trip after clean round: err = %v, want nil (tier 1, not tier 2)", err)
	}
}

func callID(i int) string {
	return "call_" + itoaTest(i)
}
