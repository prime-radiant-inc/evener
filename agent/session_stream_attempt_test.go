package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// This file drives consumeModelStream's phase/stats classification directly,
// via the newSessionStreamAccumulator seam and a llm.NewChanStream-fed fake
// stream — the same seam job_supervision_test.go uses. Every scenario here
// pre-loads its events and closes the stream before calling
// consumeModelStream, so no goroutine/synchronization is needed.

func TestConsumeModelStream_Observation_ReasoningThenError(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)
	st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "thinking hard"})
	wantErr := errors.New("boom")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if obs.Phase != llm.PhaseConsume {
		t.Fatalf("Phase = %v, want PhaseConsume", obs.Phase)
	}
	if obs.SalvagedBytes != 0 {
		t.Fatalf("SalvagedBytes = %d, want 0 (reasoning is never salvaged)", obs.SalvagedBytes)
	}
	if obs.Partial == nil {
		t.Fatalf("Partial = nil, want a snapshot (reasoning is a content event)")
	}
	if got := obs.Partial.Text(); got != "" {
		t.Fatalf("Partial.Text() = %q, want empty", got)
	}
}

func TestConsumeModelStream_Observation_TextThenError(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)
	const text = "hello world"
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: text})
	wantErr := errors.New("boom")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if obs.Phase != llm.PhaseConsume {
		t.Fatalf("Phase = %v, want PhaseConsume", obs.Phase)
	}
	if obs.SalvagedBytes != len(text) {
		t.Fatalf("SalvagedBytes = %d, want %d", obs.SalvagedBytes, len(text))
	}
	if obs.Partial == nil || obs.Partial.Text() != text {
		t.Fatalf("Partial = %+v, want text %q", obs.Partial, text)
	}
}

// TestConsumeModelStream_Observation_ToolArgsThenError pins the other half of
// "text + tool-arg bytes" salvage (spec: reasoning is never salvaged, but
// tool-call argument deltas are, exactly like text).
func TestConsumeModelStream_Observation_ToolArgsThenError(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)
	const args = `{"path":"/tmp/x"}`
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file"}})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: "call_1", Arguments: []byte(args)}})
	wantErr := errors.New("boom")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if obs.Phase != llm.PhaseConsume {
		t.Fatalf("Phase = %v, want PhaseConsume", obs.Phase)
	}
	if obs.SalvagedBytes != len(args) {
		t.Fatalf("SalvagedBytes = %d, want %d", obs.SalvagedBytes, len(args))
	}
	if obs.Partial == nil {
		t.Fatalf("Partial = nil, want a snapshot")
	}
	calls := obs.Partial.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != args {
		t.Fatalf("ToolCalls = %+v, want args %q", calls, args)
	}
}

// TestConsumeModelStream_Observation_ToolCallEndOnlyThenError pins the
// google adapter's emission shape (llm/providers/google/adapter.go:558-566):
// a complete functionCall arrives on ToolCallEnd with no preceding
// ToolCallDelta at all. Those bytes must count as a content event, not just
// as salvage — otherwise a dropped connection right after a full tool call
// classifies as PhaseFastReject/PhaseSilentStall (zero content seen) while
// still reporting nonzero SalvagedBytes, which is self-contradictory and
// makes the early-stop streak transparent to exactly the failure shape this
// component exists to catch.
func TestConsumeModelStream_Observation_ToolCallEndOnlyThenError(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "google", Model: "gemini-3-pro"}
	st := llm.NewChanStream(nil)
	const args = `{"path":"/tmp/x"}`
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "read_file"}})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: "call_1", Arguments: []byte(args)}})
	wantErr := errors.New("boom")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if obs.SalvagedBytes != len(args) {
		t.Fatalf("SalvagedBytes = %d, want %d", obs.SalvagedBytes, len(args))
	}
	if obs.Phase != llm.PhaseConsume {
		t.Fatalf("Phase = %v, want PhaseConsume (SalvagedBytes=%d implies a content event was seen)", obs.Phase, obs.SalvagedBytes)
	}
}

// TestConsumeModelStream_Observation_ZeroContentPhases covers the two
// zero-content arms of the three-way error classification: a stall signal
// (ErrSSEReadTimeout) resolves as PhaseSilentStall, and any other zero-content
// failure resolves fast as PhaseFastReject. Table-driven so every case gets
// the full Phase/SalvagedBytes/Partial assertion set — a scenario can't be
// added here without also covering all three.
func TestConsumeModelStream_Observation_ZeroContentPhases(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantPhase      llm.AttemptPhase
		wantBytes      int
		wantPartialNil bool
	}{
		{
			name:           "SilentStall",
			err:            fmt.Errorf("read: %w", llm.ErrSSEReadTimeout),
			wantPhase:      llm.PhaseSilentStall,
			wantBytes:      0,
			wantPartialNil: true,
		},
		{
			name:           "FastReject",
			err:            errors.New("400 bad request"),
			wantPhase:      llm.PhaseFastReject,
			wantBytes:      0,
			wantPartialNil: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t)
			req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
			st := llm.NewChanStream(nil)
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: tc.err})
			st.CloseSend()

			_, obs, err := sess.consumeModelStream(context.Background(), req, st)

			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if obs.Phase != tc.wantPhase {
				t.Fatalf("Phase = %v, want %v", obs.Phase, tc.wantPhase)
			}
			if obs.SalvagedBytes != tc.wantBytes {
				t.Fatalf("SalvagedBytes = %d, want %d", obs.SalvagedBytes, tc.wantBytes)
			}
			if (obs.Partial == nil) != tc.wantPartialNil {
				t.Fatalf("Partial = %+v, want nil=%v", obs.Partial, tc.wantPartialNil)
			}
		})
	}
}

// TestConsumeModelStream_Observation_ContentWindowExcludesPrefixGap pins
// ContentWindow to the span between content events, not wall-clock attempt
// duration — an implementation that instead reports time.Since(attemptStart)
// would pass every other test in this file (each has zero or one content
// event, where the two spans coincide) but fails here: the deltas are
// separated by a real gap, preceded by a real, larger padding gap during
// which nothing is sent. A wall-clock implementation reports pad+gap
// (>= padDelay); the correct one reports only gap (< padDelay). Runs
// consumeModelStream concurrently with the sends (unlike this file's other
// tests) because the gaps must be observed as real elapsed time by the
// consumer, not just queued ahead of it in the buffered channel.
func TestConsumeModelStream_Observation_ContentWindowExcludesPrefixGap(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	type outcome struct {
		obs attemptObservation
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, obs, err := sess.consumeModelStream(context.Background(), req, st)
		done <- outcome{obs, err}
	}()

	const padDelay = 200 * time.Millisecond // before the first content event
	const gapDelay = 40 * time.Millisecond  // between the two content events
	time.Sleep(padDelay)
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "a"})
	time.Sleep(gapDelay)
	st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "b"})
	wantErr := errors.New("boom")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumeModelStream did not finish")
	}

	if !errors.Is(got.err, wantErr) {
		t.Fatalf("err = %v, want %v", got.err, wantErr)
	}
	if got.obs.ContentWindow <= 0 {
		t.Fatalf("ContentWindow = %v, want > 0", got.obs.ContentWindow)
	}
	if got.obs.ContentWindow >= padDelay {
		t.Fatalf("ContentWindow = %v, want < padDelay (%v): a wall-clock-since-attempt-start "+
			"implementation would include the pre-first-content padding and fail this bound",
			got.obs.ContentWindow, padDelay)
	}
}
