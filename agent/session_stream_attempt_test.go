package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

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

func TestConsumeModelStream_Observation_SilentStall(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)
	wantErr := fmt.Errorf("read: %w", llm.ErrSSEReadTimeout)
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, llm.ErrSSEReadTimeout) {
		t.Fatalf("err = %v, want wrapping ErrSSEReadTimeout", err)
	}
	if obs.Phase != llm.PhaseSilentStall {
		t.Fatalf("Phase = %v, want PhaseSilentStall", obs.Phase)
	}
	if obs.SalvagedBytes != 0 {
		t.Fatalf("SalvagedBytes = %d, want 0", obs.SalvagedBytes)
	}
	if obs.Partial != nil {
		t.Fatalf("Partial = %+v, want nil (nothing accumulated)", obs.Partial)
	}
}

// TestConsumeModelStream_Observation_FastReject pins the remaining arm of the
// three-way error classification: zero content events and no SSE-timeout/30s
// signal resolves as a fast rejection, not a stall.
func TestConsumeModelStream_Observation_FastReject(t *testing.T) {
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)
	wantErr := errors.New("400 bad request")
	st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: wantErr})
	st.CloseSend()

	_, obs, err := sess.consumeModelStream(context.Background(), req, st)

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if obs.Phase != llm.PhaseFastReject {
		t.Fatalf("Phase = %v, want PhaseFastReject", obs.Phase)
	}
}
