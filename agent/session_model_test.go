package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestSession_StreamOpenFailureHonorsRetryBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 500, "temporary upstream failure", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatal("expected stream error")
	}
	sess.Close()

	completeCalls, streamCalls := f.Counts()
	if got, want := streamCalls, 1; got != want {
		t.Fatalf("stream calls: got %d want %d", got, want)
	}
	if got, want := completeCalls, 0; got != want {
		t.Fatalf("complete calls: got %d want %d", got, want)
	}
}

// kata r6y9: after the LLM call returns a retryable stream error and the retry
// policy is exhausted, the session must NOT be left in PROCESSING. Otherwise
// the daemon's /status keeps reporting PROCESSING forever, the hub disables
// steer/send, and the user has no recovery path.
func TestSession_StreamErrorReturnsSessionToIdle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			// Send nothing and close: callModel sees the channel close
			// before any finish event and surfaces a retryable StreamError
			// ("stream ended without finish event").
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected stream-ended error")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after stream error: got %q want %q", got, SessionIdle)
	}
}

// TestSession_RetriesMidStreamFailure verifies that a retryable error surfaced
// DURING stream consumption (after the HTTP response already returned 200) is
// retried with the configured budget — not only open-time failures. A reasoning
// -model cutoff arrives as a retryable StreamError mid-stream; without
// consume-path retry, a single transient truncation fails the whole turn (the
// gpt-5.4-mini "stream closed without [DONE]" incident).
func TestSession_RetriesMidStreamFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			// Open succeeds (200), then the stream closes with no finish event:
			// a retryable "stream ended without finish event" mid-stream failure
			// on every attempt, with no partial output delivered.
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		},
	}
	c.Register(f)

	noSleep := func(context.Context, time.Duration) error { return nil }
	policy := llm.RetryPolicy{MaxRetries: 5, BaseDelay: time.Millisecond}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       noSleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected failure after exhausting the retry budget")
	}

	if _, streamCalls := f.Counts(); streamCalls != 6 {
		t.Fatalf("stream calls = %d, want 6 (1 initial + 5 retries of the open+consume cycle)", streamCalls)
	}
}

// TestSession_RetriesAfterPartialOutputAndResets verifies that a retryable
// mid-stream failure is retried even after partial output was streamed to the
// user, and that each retry first emits an assistant-text reset so the retry's
// output replaces the discarded partial rather than appending to it.
func TestSession_RetriesAfterPartialOutputAndResets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			// Stream a partial assistant delta, then close without a finish
			// event: a retryable failure with partial output already shown.
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "t0"})
			st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "partial"})
		},
	}
	c.Register(f)

	var mu sync.Mutex
	var kinds []events.EventKind
	done := make(chan struct{})

	noSleep := func(context.Context, time.Duration) error { return nil }
	policy := llm.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       noSleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			mu.Lock()
			kinds = append(kinds, ev.Kind)
			mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected failure after exhausting the retry budget")
	}
	sess.Close()
	<-done

	if _, streamCalls := f.Counts(); streamCalls != 4 {
		t.Fatalf("stream calls = %d, want 4 (partial output must not block retry)", streamCalls)
	}
	mu.Lock()
	gotKinds := append([]events.EventKind(nil), kinds...)
	mu.Unlock()

	// One reset before each of the 3 retries — the partial shown by each failed
	// attempt is discarded before the next attempt streams — and one more when
	// the round finally gives up and settlement re-emits the salvaged partial
	// as a completed assistant item.
	var got []events.EventKind
	for _, kind := range gotKinds {
		switch kind {
		case events.EventAssistantTextReset, events.EventAssistantTextStart,
			events.EventAssistantTextDelta, events.EventAssistantTextEnd:
			got = append(got, kind)
		}
	}
	want := []events.EventKind{events.EventAssistantTextStart, events.EventAssistantTextDelta}
	for range 3 {
		want = append(want, events.EventAssistantTextReset, events.EventAssistantTextStart, events.EventAssistantTextDelta)
	}
	want = append(want, events.EventAssistantTextReset, events.EventAssistantTextStart,
		events.EventAssistantTextDelta, events.EventAssistantTextEnd)
	if !slices.Equal(got, want) {
		t.Fatalf("assistant-text events =\n%v\nwant\n%v", got, want)
	}
}

// kata 3tgv: when the agent loop bails out of session.Input via a recoverable
// LLM error (retry policy exhausted, stream-ended, etc.), the in-memory
// turn_count was not being flushed to meta.json. The fix adds a maybeAutoSave
// call alongside the SessionIdle flip in the error exit path.
//
// This test exercises the gap directly: a pause_turn response bumps
// modelResponses without triggering the happy-path autosave (which fires
// only after a completed tool round), then the next LLM call streams empty
// and surfaces a retryable StreamError. Without the fix, meta.json still
// shows the initial-save turn_count of 0 even though modelResponses is 1.
func TestSession_StreamErrorFlushesMetaJSON_AfterPauseTurnGap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	var streamCallCount int
	var streamMu sync.Mutex
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			streamMu.Lock()
			n := streamCallCount
			streamCallCount++
			streamMu.Unlock()
			if n == 0 {
				// Call 1: pause_turn. Bumps modelResponses to 1 in-memory
				// but does NOT trigger happy-path autosave (autosave fires
				// only after the tool round at line ~2074).
				st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
				st.Send(llm.StreamEvent{
					Type:   llm.StreamEventTextStart,
					TextID: "t1",
				})
				st.Send(llm.StreamEvent{
					Type:   llm.StreamEventTextDelta,
					TextID: "t1",
					Delta:  "thinking...",
				})
				st.Send(llm.StreamEvent{
					Type:   llm.StreamEventTextEnd,
					TextID: "t1",
				})
				finish := llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
				return
			}
			// Call 2: send nothing → stream-ended error.
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected stream-ended error after pause_turn")
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	// Pre-fix: meta.json on disk still shows turn_count=0 (initial save
	// value), even though in-memory modelResponses is 1 (the pause_turn
	// bump).  Post-fix: error path flushes, so meta.json reflects 1.
	if meta.TurnCount != 1 {
		t.Fatalf("turn_count after pause_turn + stream error: got %d want 1 (the pause_turn round must be persisted)", meta.TurnCount)
	}
}

// TestSession_EmitsReasoningSummaryDelta verifies the evener harness no longer
// discards the model's reasoning: a REASONING_DELTA stream event is surfaced as
// an EventReasoningSummaryDelta so the web UI can render thinking live.
func TestSession_EmitsReasoningSummaryDelta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	commCall := communicateCall("c1", "the answer")
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{Type: llm.StreamEventReasoningDelta, ReasoningDelta: "let me think"})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: commCall.ID, Name: commCall.Name, Type: "function"}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: commCall.ID, Arguments: commCall.Arguments}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &commCall})
			finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, LLMRetryPolicy: &policy})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var mu sync.Mutex
	var reasoning []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind != events.EventReasoningSummaryDelta {
				continue
			}
			if data, ok := ev.Data.(events.ReasoningSummaryDeltaData); ok {
				mu.Lock()
				reasoning = append(reasoning, data.Delta)
				mu.Unlock()
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(reasoning) == 0 {
		t.Fatal("no EventReasoningSummaryDelta emitted; reasoning was discarded")
	}
	if reasoning[0] != "let me think" {
		t.Fatalf("reasoning delta = %q, want %q", reasoning[0], "let me think")
	}
}

// kata 3tgv: a session that errors out on the very first LLM call (before
// any assistant turn lands) must still leave a meta.json on disk with the
// correct (zero) turn_count — file must be current, not missing or stale.
// This is the simpler shape from the kata report: USER_INPUT in transcript,
// no completed assistant exchanges, turn_count=0 on disk.
func TestSession_StreamErrorFlushesMetaJSON_FirstTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			// Send nothing and close: surfaces a retryable StreamError.
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected stream-ended error")
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.TurnCount != 0 {
		t.Fatalf("turn_count after first-turn stream error: got %d want 0", meta.TurnCount)
	}
}

// kata 3tgv: after one successful turn (turn_count=1) followed by a turn that
// errors out, meta.json must reflect turn_count=1, not 0 and not a stale
// missing-file. This covers the case where modelResponses has already been
// bumped by happy-path autosaves and the error path must preserve that.
func TestSession_StreamErrorFlushesMetaJSON_AfterHappyTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	var streamCallCount int
	var streamMu sync.Mutex
	commCall := communicateCall("c1", "first reply")
	f := &streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			streamMu.Lock()
			n := streamCallCount
			streamCallCount++
			streamMu.Unlock()
			if n == 0 {
				// Turn 1: model calls communicate (end_turn=true) and finishes.
				st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
				st.Send(llm.StreamEvent{
					Type: llm.StreamEventToolCallStart,
					ToolCall: &llm.ToolCallData{
						ID:   commCall.ID,
						Name: commCall.Name,
						Type: "function",
					},
				})
				st.Send(llm.StreamEvent{
					Type: llm.StreamEventToolCallDelta,
					ToolCall: &llm.ToolCallData{
						ID:        commCall.ID,
						Arguments: commCall.Arguments,
					},
				})
				st.Send(llm.StreamEvent{
					Type:     llm.StreamEventToolCallEnd,
					ToolCall: &commCall,
				})
				finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
				return
			}
			// Turn 2: send nothing → stream-ended error.
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	// Turn 1: happy path → completed assistant turn, turn_count → 1.
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("turn 1 ProcessInput: %v", err)
	}
	sessID := sess.ID()
	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta after turn 1: %v", err)
	}
	if meta.TurnCount != 1 {
		t.Fatalf("turn_count after happy turn 1: got %d want 1", meta.TurnCount)
	}

	// Turn 2: stream-ended error. meta.json must remain at 1 (the prior
	// completed exchange), not stale-zero, not missing.
	if _, err := sess.ProcessInput(ctx, "and again", nil); err == nil {
		t.Fatal("turn 2: expected stream-ended error")
	}
	sess.Close()

	meta, err = schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta after error turn: %v", err)
	}
	if meta.TurnCount != 1 {
		t.Fatalf("turn_count after error turn: got %d want 1 (prior happy turn must be preserved)", meta.TurnCount)
	}
}

// kata ztne: when the model returns empty responses repeatedly until the
// retry budget exhausts, processOneInput returns an emptyResponseExhaustedError.
// The deferred flush at the top of processOneInput must flush meta.json so
// turn_count reflects the in-memory modelResponses bumps from each empty
// retry.
func TestSession_EmptyResponseExhaustedFlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// Each empty response increments modelResponses (the loop bumps it before
	// retrying). After maxEmptyRetries=3 consecutive empties, the loop returns
	// emptyResponseExhaustedError. modelResponses ends at 4 (1 initial + 3 retries).
	emptyStep := func(req llm.Request) llm.Response {
		return llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{}},
		}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			emptyStep, emptyStep, emptyStep, emptyStep, emptyStep, emptyStep,
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil || !errors.Is(err, errEmptyResponseExhausted) {
		t.Fatalf("expected emptyResponseExhaustedError, got %v", err)
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	// Each empty response (initial + 3 retries) bumps modelResponses, then
	// the loop hits the exhausted exit. meta.json must reflect exactly
	// maxEmptyRetries+1 rounds.
	if want := maxEmptyRetries + 1; meta.TurnCount != want {
		t.Fatalf("turn_count after empty-response exhaustion: got %d, want %d (1 initial + maxEmptyRetries retries)", meta.TurnCount, want)
	}
}

// kata ztne: when the model returns bare text (no tool call) repeatedly until
// the retry budget exhausts, processOneInput returns a
// bareTextWithoutResultToolError. The deferred flush must persist meta.json.
func TestSession_BareTextWithoutResultToolFlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	bareStep := func(req llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("bare text without tool")}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			bareStep, bareStep, bareStep, bareStep, bareStep,
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil || !errors.Is(err, errBareTextWithoutResultTool) {
		t.Fatalf("expected bareTextWithoutResultToolError, got %v", err)
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if want := maxBareTextRetries + 1; meta.TurnCount != want {
		t.Fatalf("turn_count after bare-text exhaustion: got %d, want %d (1 initial + maxBareTextRetries retries)", meta.TurnCount, want)
	}
}

// kata ztne: when MaxToolRoundsPerInput is reached, processOneInput returns
// typed exhaustion. The deferred flush must run.
// With MaxToolRoundsPerInput=1, after one round the loop exits without ever
// hitting the happy-path autosave at the end of the round (no communicate
// delivery). meta.json must still reflect the bumped modelResponses.
func TestSession_MaxToolRoundsExitFlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// Always return a non-communicate tool call — the model never delivers,
	// so the loop runs until MaxToolRoundsPerInput is hit.
	toolStep := func(req llm.Request) llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID:        "t1",
						Name:      "my_loop_tool",
						Arguments: json.RawMessage(`{}`),
						Type:      "function",
					}},
				},
			},
		}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			toolStep, toolStep, toolStep,
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:              dir,
		MaxToolRoundsPerInput: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.RegisterTool("my_loop_tool", "loops forever", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		return "ok", nil
	})
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	requireBudgetExhaustion(t, err, exhaustedBudgetToolRounds, 1, true)
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.TurnCount != 1 {
		t.Fatalf("turn_count after MaxToolRoundsPerInput=1 exit: got %d, want 1 (exactly one LLM round ran)", meta.TurnCount)
	}
}

// kata ztne: when ctx is cancelled mid-turn, processOneInput returns
// ctx.Err(). The deferred flush must persist meta.json. We trigger
// cancellation by passing a ctx that's already cancelled before the LLM
// call — the per-round ctx.Done() check on line ~1607 returns first.
// modelResponses stays at 0, but meta.json must still be written so a
// resumed-session reader sees a current snapshot, not a stale one.
func TestSession_CtxCancellationFlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// First turn: completes happily, bumping modelResponses to 1 and writing
	// meta.json. Second turn: ctx is cancelled before the LLM call, exit via
	// ctx.Done(). Without the deferred flush this exit path skips
	// maybeAutoSave entirely.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("hi back") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	// Happy turn 1.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel1()
	if _, err := sess.ProcessInput(ctx1, "hi", nil); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	sessID := sess.ID()

	// Delete meta.json so we can detect whether the cancellation path
	// re-writes it. (If it doesn't, LoadSessionMeta will fail.)
	metaPath := filepath.Join(dir, "sessions", sessID+".meta.json")
	if err := os.Remove(metaPath); err != nil {
		t.Fatalf("remove meta.json: %v", err)
	}

	// Turn 2: pre-cancelled ctx.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := sess.ProcessInput(ctx2, "again", nil); err == nil {
		t.Fatal("expected ctx.Canceled error")
	}
	sess.Close()

	// Deferred flush must have re-created meta.json.
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.json missing after ctx cancellation: %v", err)
	}
	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	// Must reflect turn 1's completed exchange (modelResponses=1), not be
	// missing.
	if meta.TurnCount != 1 {
		t.Fatalf("turn_count after ctx cancel: got %d want 1", meta.TurnCount)
	}
}

// kata ztne: when a tool handler panics, processOneInput must (a) flush
// meta.json via the deferred recover, and (b) let the panic propagate.
func TestSession_PanicFlushesMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// Model issues one tool call to a panicking tool. The panic propagates
	// up through execTool → processOneInput, where our recover catches it,
	// flushes meta.json, and re-panics.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "p1",
								Name:      "boom_tool",
								Arguments: json.RawMessage(`{}`),
								Type:      "function",
							}},
						},
					},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.RegisterTool("boom_tool", "panics on call", map[string]any{"type": "object"}, func(ctx context.Context, args any) (any, error) {
		panic("ztne kata test panic")
	})
	go func() {
		for range sess.Events() {
		}
	}()

	sessID := sess.ID()
	// Delete the initial meta.json so we can detect the panic-path flush.
	metaPath := filepath.Join(dir, "sessions", sessID+".meta.json")
	if err := os.Remove(metaPath); err != nil {
		t.Fatalf("remove meta.json: %v", err)
	}

	// Call ProcessInput inside a recover to catch the re-panic.
	var panicked any
	func() {
		defer func() {
			panicked = recover()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
		defer cancel()
		_, _ = sess.ProcessInput(ctx, "hi", nil)
	}()
	if panicked == nil {
		t.Fatal("expected panic to propagate through ProcessInput")
	}
	if !strings.Contains(fmt.Sprint(panicked), "ztne kata test panic") {
		t.Fatalf("unexpected panic value: %v", panicked)
	}
	sess.Close()

	// Deferred recover must have flushed meta.json before re-panicking.
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("meta.json missing after panic: %v", err)
	}
	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	// modelResponses bumps after the LLM response is processed (before
	// tool exec, which is where the panic happens). turn_count >= 1.
	if meta.TurnCount < 1 {
		t.Fatalf("turn_count after panic: got %d want >=1", meta.TurnCount)
	}
}

func TestStreamUnavailableIgnoresPlainTextUnsupportedMessages(t *testing.T) {
	t.Parallel()
	if !streamUnavailable(errStreamUnavailable) {
		t.Fatal("expected internal sentinel to mark stream unavailable")
	}
	if !streamUnavailable(llm.ErrStreamUnsupported) {
		t.Fatal("expected LLM sentinel to mark stream unavailable")
	}
	if streamUnavailable(errors.New("provider returned: streaming not supported for this request")) {
		t.Fatal("plain error text should not mark stream unavailable")
	}
}

func TestSession_ProviderAbortKeepsSessionIdle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.NewAbortError("user canceled", nil)
			},
		},
	})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	_, err = sess.ProcessInput(context.Background(), "cancel", nil)
	if err == nil {
		t.Fatal("expected abort error")
	}
	if _, ok := errors.AsType[*llm.AbortError](err); !ok {
		t.Fatalf("err=%T %v, want AbortError", err, err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state=%s, want %s", got, SessionIdle)
	}
}

func TestSession_WebSearch_FlagSetOnRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatalf("no requests captured")
	}
	if !reqs[0].WebSearch {
		t.Fatalf("WebSearch flag not set on request")
	}
}

// TestSession_WebSearch_FlagNotSetWhenBaseURLDiverges pins that an
// instance with its own base_url no longer inherits the vendor's
// provider-level web_search (llm/registry's endpoint gate), so the request
// this session builds must not carry the flag - a gateway that does not
// implement the hosted tool rejects it, ending the session on turn one.
// "gw" is the fixture's base=openai instance pointed at a different
// base_url (profile_testhelpers_test.go); it never sets web_search itself.
//
// This test only proves the session's own derivation (providerWebSearchEnabled
// -> profile.SupportsWebSearch -> registry.BoolValue) reads a gated profile
// correctly - BoolValue collapses nil and false alike, so it would pass
// unchanged whether the registry stripped WebSearch to nil or to false. It
// cannot, on its own, see a fail-open gap at the wire-building layer, where
// a caller that sets Request.WebSearch directly (bypassing
// providerWebSearchEnabled entirely - cmd/llmcall's --web-search flag, for
// one) would hit a protocol adapter's own gate (caps.WebSearch == nil ||
// *caps.WebSearch), which treats nil as permissive. The wire-layer leak is
// pinned separately and directly in llm/providers/responses
// (TestBuildBody_WebSearchNilCapsIsFailOpen); this test closes the gap at
// its own layer instead by also asserting the resolved Caps.WebSearch
// pointer itself is explicit false, not nil, so a registry regression back
// to nil'ing the cap fails here even though req.WebSearch would still read
// false.
func TestSession_WebSearch_FlagNotSetWhenBaseURLDiverges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "gw",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				})
			},
		},
	}
	c.Register(f)

	profile := resolveTestProfile("gw", nil, "gpt-5.2")
	if profile.SupportsWebSearch() {
		t.Fatal("pre-condition: gw must not resolve web_search (its base_url diverges from openai's default)")
	}
	if ws := profile.Resolved().Caps.WebSearch; ws == nil || *ws {
		t.Fatalf("pre-condition: gw's resolved Caps.WebSearch must be an explicit false, not nil (nil is fail-open at the adapter layer): %v", ws)
	}

	sess, err := NewSession(c, withTestSessionNamer(c, profile), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatalf("no requests captured")
	}
	if reqs[0].WebSearch {
		t.Fatalf("WebSearch flag set on request for a base_url-diverged instance")
	}
}

func TestSession_PauseTurn_ContinuesLoop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				// First call: return pause_turn (model needs more time for search)
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Searching..."}},
					},
					Finish: llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
					Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
			},
			func(req llm.Request) llm.Response {
				// Second call: return final answer
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Here is the answer."}},
					},
					Finish: llm.FinishReason{Reason: "stop"},
					Usage:  llm.Usage{InputTokens: 20, OutputTokens: 10, TotalTokens: 30},
				})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	result, err := sess.ProcessInput(ctx, "search for something", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(reqs))
	}
	if !strings.Contains(result, "Here is the answer.") {
		t.Fatalf("result: %q", result)
	}
}

func TestSession_PauseTurn_DoesNotCountAsToolRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Return 3 pause_turns, then a final stop. With MaxToolRoundsPerInput=2,
	// this should succeed because pause_turns are not counted.
	callNum := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("still searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("more searching..."),
					Finish:  llm.FinishReason{Reason: llm.FinishReasonPauseTurn, Raw: "pause_turn"},
				}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return wrapCommunicateResponse(llm.Response{
					Model:   "gpt-5.2",
					Message: llm.Assistant("here is the answer"),
					Finish:  llm.FinishReason{Reason: "stop"},
				})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2, // Only 2 real rounds allowed.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	result, err := sess.ProcessInput(ctx, "search for something", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if !strings.Contains(result, "here is the answer") {
		t.Fatalf("expected final answer in result, got: %q", result)
	}
	// All 4 LLM calls should have been made (3 pause_turns + 1 stop).
	reqs := f.Requests()
	if len(reqs) != 4 {
		t.Fatalf("expected 4 LLM calls (3 pause_turns + 1 stop), got %d", len(reqs))
	}
}

func TestSession_RecordInputTokens_SkipsWebSearchResponse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Response 1: contains ContentWebSearch — inflated tokens should NOT be recorded.
	// Response 2: no web search — tokens should be recorded normally.
	c.Register(&fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "test query"}},
							{Kind: llm.ContentText, Text: "Found results via web search."},
						},
					},
					Usage: llm.Usage{InputTokens: 200_000}, // inflated ~2x
				})
			},
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "done"}},
					},
					Usage: llm.Usage{InputTokens: 100_000}, // real count
				})
			},
		},
	})

	sess, err := NewSession(c, withTestSessionNamer(c, newAnthropicProfile("claude-opus-4-6")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()

	// First call: web search response with inflated tokens.
	if _, err := sess.ProcessInput(ctx, "search something", nil); err != nil {
		t.Fatal(err)
	}

	// The inflated 200K should NOT have been recorded as lastInputTokens;
	// the prior value (0 for a fresh session) must be preserved.
	lit := sess.contextMgr.LastInputTokens()
	if lit != 0 {
		t.Fatalf("lastInputTokens after web-search response = %d, want 0 (inflated tokens must not be recorded; prior value must be preserved)", lit)
	}

	// Second call: normal response.
	if _, err := sess.ProcessInput(ctx, "follow up", nil); err != nil {
		t.Fatal(err)
	}

	// Now the real 100K should be recorded.
	lit2 := sess.contextMgr.LastInputTokens()
	if lit2 != 100_000 {
		t.Fatalf("lastInputTokens after normal response = %d, want 100000", lit2)
	}
}

func TestSession_ContentFilterRecovery_CompactsAndRetries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Build a content filter error (HTTP 400 with invalid_prompt code).
	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)

	callCount := 0
	f := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			// First call succeeds with a tool call to build up history.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return llm.Response{Message: llm.Message{
					Role: "assistant",
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Let me read the file."},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "call_1", Name: "exec_command",
							Arguments: json.RawMessage(`{"command":"echo hello","workdir":"/tmp"}`),
						}},
					},
				}}, nil
			},
			// Second call: content filter error.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return llm.Response{}, contentFilterErr
			},
			// Third call (after compaction): succeeds.
			func(req llm.Request) (llm.Response, error) {
				callCount++
				return finalResponse("recovered after compaction"), nil
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		LLMRetryPolicy:        &llm.RetryPolicy{MaxRetries: 0}, // no transport retries
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Collect events in background.
	var compactionCount int
	var errorCount int
	evDone := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == events.EventContextCompaction {
				compactionCount++
			}
			if ev.Kind == events.EventError {
				errorCount++
			}
		}
		close(evDone)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	out, err := sess.ProcessInput(ctx, "trigger content filter", nil)
	if err != nil {
		t.Fatalf("ProcessInput should have recovered, got error: %v", err)
	}
	sess.Close()
	<-evDone

	if !strings.Contains(out, "recovered after compaction") {
		t.Errorf("expected recovery text, got: %q", out)
	}
	if callCount != 3 {
		t.Errorf("expected 3 LLM calls (initial + filter + recovery), got %d", callCount)
	}
	if compactionCount == 0 {
		t.Error("expected at least one compaction event from content filter recovery")
	}
	if errorCount != 0 {
		t.Errorf("recovered content filter should not emit terminal EventError; got %d", errorCount)
	}
}

func TestSession_ContentFilterRecovery_FailsOnSecondFilterHit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)

	f := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			// First call succeeds with a tool call.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Message{
					Role: "assistant",
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "working..."},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID: "call_1", Name: "exec_command",
							Arguments: json.RawMessage(`{"command":"echo hello","workdir":"/tmp"}`),
						}},
					},
				}}, nil
			},
			// Second call: content filter error.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, contentFilterErr
			},
			// Third call (after compaction): content filter AGAIN.
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, contentFilterErr
			},
		},
	}

	c := llm.NewClient()
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 20,
		LLMRetryPolicy:        &llm.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	_, err = sess.ProcessInput(ctx, "trigger content filter twice", nil)
	if err == nil {
		t.Fatal("expected error on second content filter hit, got nil")
	}

	if llm.Kind(err) != llm.KindContentFilter {
		t.Errorf("expected ContentFilterError, got: %T: %v", err, err)
	}
	// All 3 steps should have been called: success, filter, recovery-filter.
	reqs := f.Requests()
	if len(reqs) != 3 {
		t.Errorf("expected 3 LLM calls (success + filter + recovery-filter), got %d", len(reqs))
	}
	sess.Close()
}

// testContentFilterRecoveryAdjustsBaseline pins the N4 in-flight-turn
// boundary (turnHistoryBaseline) through a content-filter ForceCompact retry
// (issue #634). It ground-truths the expected baseline against the ACTUAL
// post-fold position of the turn that was at the baseline — found by its
// unique text in the resulting history — rather than a hand-derived formula
// for it: ForceCompact always runs both the checkpoint AND (given a real
// client, which every test session has) the summarize layer, and when
// withGoalSteering injects a turn between them, summarize's own
// preserve-last-PreserveRecentTurns cutoff shifts to include it, folding one
// MORE original turn than it would at S=0. A simple preLen-postLen turn-count
// delta can't see that reshuffle; only checking where the marked turn
// actually landed can.
//
// withGoalSteering activates a goal before the fold, so goalCompactionSteering
// (agent/session_goal.go) unconditionally injects one TurnSteering turn
// mid-fold — the same mechanism a pinned note or a PreCompact plugin hook uses
// (session_compaction.go's runPreCompactHook), all three of which
// compactionEmitFunc's emitFn fires synchronously through the same *history
// pointer the caller measures before/after length on. Without a case that
// injects at least one turn, the shrink amount always equals a plain turn
// count delta and any bug in how injected turns are handled is invisible.
//
// Drives handleModelError directly (the same direct-call pattern
// TestDelegateControllerModelRequestUsesOutgoingReplayScope uses for
// prepareModelRequestWithError) against history seeded past PreserveRecentTurns
// so ForceCompact's checkpoint layer actually folds turns away — a no-op fix
// would pass against a fold-free history, so this forces a real shrink.
func testContentFilterRecoveryAdjustsBaseline(t *testing.T, withGoalSteering bool) {
	t.Helper()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	if withGoalSteering {
		s.getOrCreateGoalStore().Set("Ship the feature", time.Now()) // goalCompactionSteering injects 1 turn, unconsumed, every fold
	}

	preHistory := currentHistory(t, s)
	baselineIdx := len(preHistory) - 3 // last 3 turns simulate the in-flight turn
	baselineText := preHistory[baselineIdx].Message.Text()
	s.mu.Lock()
	s.turnHistoryBaseline = baselineIdx
	s.mu.Unlock()

	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)
	retried := false
	retry, err := s.handleModelError(context.Background(), contentFilterErr, llm.Request{}, &retried, false)
	if err != nil {
		t.Fatalf("handleModelError: %v", err)
	}
	if !retry {
		t.Fatal("expected content-filter retry to request a retry")
	}

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}
	wantBaseline := indexOfTurnText(postHistory, baselineText)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold — test setup invalid", baselineText)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

func TestSession_ContentFilterRecovery_AdjustsTurnHistoryBaselineOnFold(t *testing.T) {
	t.Parallel()
	testContentFilterRecoveryAdjustsBaseline(t, false)
}

func TestSession_ContentFilterRecovery_AdjustsTurnHistoryBaselineOnFold_WithInjectedSteering(t *testing.T) {
	t.Parallel()
	testContentFilterRecoveryAdjustsBaseline(t, true)
}

// TestHandleModelError_ContentFilterRetry_PreservesConcurrentAppendDuringSlowFold
// pins merge-back for handleModelError's content-filter ForceCompact retry:
// it snapshots s.history and folds UNLOCKED (ForceCompact's Layer 2
// summarization can be slow), so republishing the snapshot unconditionally
// would silently drop a turn appended to s.history by another goroutine
// while the fold ran (e.g. a queued tool result), which sits past the
// snapshot's length — the same data-loss class Compact() guards against.
//
// No sleep-based synchronization: a scripted cheap-model adapter blocks on a
// channel inside Layer 2's LLM call, mirroring
// TestSessionCompact_PreservesConcurrentAppendDuringSlowFold's approach for
// this different entry point.
func TestHandleModelError_ContentFilterRetry_PreservesConcurrentAppendDuringSlowFold(t *testing.T) {
	t.Parallel()
	const blockingProvider = "cfr-blocking-cheap"
	entered := make(chan struct{})
	proceed := make(chan struct{})
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(&agenttest.ScriptedAdapter{
		Provider: blockingProvider,
		Responder: func(req llm.Request) llm.Response {
			close(entered)
			<-proceed
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		},
	})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), blockingProvider+"/model")

	s := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold, and ForceCompact always attempts Layer 2 given a real client

	contentFilterErr := llm.ErrorFromHTTPStatus(
		"openai", 400, "content filter triggered",
		map[string]any{"error": map[string]any{"code": "invalid_prompt"}},
		nil,
	)
	retried := false
	type result struct {
		retry bool
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		retry, err := s.handleModelError(context.Background(), contentFilterErr, llm.Request{}, &retried, false)
		resultCh <- result{retry, err}
	}()

	<-entered // the fold is now blocked inside Layer 2's LLM call, past its unlocked snapshot

	const concurrentText = "concurrent append during content-filter fold"
	s.mu.Lock()
	s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(concurrentText)))
	s.mu.Unlock()

	close(proceed) // let the fold finish

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("handleModelError: %v", res.err)
	}
	if !res.retry {
		t.Fatal("expected content-filter retry to request a retry")
	}

	if indexOfTurnText(currentHistory(t, s), concurrentText) < 0 {
		t.Fatal("turn appended to s.history while the content-filter fold ran unlocked did not survive publication")
	}
}

// testManageContextShrinksBaselineOnFold pins turnHistoryBaseline through
// prepareModelRequestWithError's pre-existing mid-turn ManageContext fold —
// the computation issue #634 asked the content-filter fix to mirror, which
// turned out to inherit the same injected-turns under-shrink (see
// testContentFilterRecoveryAdjustsBaseline). No prior test exercised this
// shrink branch at all (round 0 only sets the baseline fresh); this covers it
// at both S=0 (regression pin: must stay unchanged) and, via withGoalSteering,
// S=1 (correctness pin: must now track the fold exactly, ground-truthed the
// same way as the content-filter test).
//
// Drives prepareModelRequestWithError directly at round 1 (the same
// direct-call pattern TestDelegateControllerModelRequestUsesOutgoingReplayScope
// uses), with pressure forced above CheckpointThreshold so MaybeCompact's
// checkpoint layer actually folds. elicitNoteFn is stubbed to return no note:
// maybeElicitNoteBeforeCompaction is on by default and would otherwise pin a
// real note under this forced pressure, adding an uncontrolled extra injected
// turn on top of the one withGoalSteering deliberately adds.
func testManageContextShrinksBaselineOnFold(t *testing.T, withGoalSteering bool) {
	t.Helper()
	s := newTestSession(t)
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold
	if withGoalSteering {
		s.getOrCreateGoalStore().Set("Ship the feature", time.Now()) // goalCompactionSteering injects 1 turn, unconsumed, every fold
	}
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80), < SummarizeThreshold(0.95): checkpoint layer only

	preHistory := currentHistory(t, s)
	baselineIdx := len(preHistory) - 3 // last 3 turns simulate the in-flight turn
	baselineText := preHistory[baselineIdx].Message.Text()
	s.mu.Lock()
	s.turnHistoryBaseline = baselineIdx
	s.mu.Unlock()

	var timings events.RoundTimings
	_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}
	wantBaseline := indexOfTurnText(postHistory, baselineText)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold — test setup invalid", baselineText)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

func TestSession_ManageContext_ShrinksBaselineOnFold(t *testing.T) {
	t.Parallel()
	testManageContextShrinksBaselineOnFold(t, false)
}

func TestSession_ManageContext_ShrinksBaselineOnFold_WithInjectedSteering(t *testing.T) {
	t.Parallel()
	testManageContextShrinksBaselineOnFold(t, true)
}

// testManageContextShrinksBaselineWithStrategy pins turnHistoryBaseline
// through a memory-crystals-strategy ManageContext fold, parameterized over
// which injection sources fire on the same fold:
//
//   - withGoalSteering activates a goal so runPreCompactHook's
//     goalCompactionSteering injects a turn — the hook-side contribution,
//     counted via len(pendingSteering).
//   - withCrystal seeds one crystal via AfterAction so the strategy's own
//     post-fold append fires — the strategy-side contribution, counted via
//     contextmgr.WithPostFoldInjectionCallback (strategyInjected).
//
// (false, true) is the strategy-only pin
// (TestSession_ManageContext_ShrinksBaselineOnFold_WithStrategyInjectedSteering):
// a non-default context strategy appends its own steering turn AFTER
// MaybeCompact returns,
// not through runPreCompactHook, so compactionEmitFunc's injectedTurns()
// alone couldn't see it and managedLen (measured after the whole strategy
// call) silently absorbed the extra turn.
//
// (true, false) proves the callback no-op path composes: with a strategy
// capable of self-injection present but not triggered (no crystal seeded),
// only the hook's turn should count — nothing is double-counted or dropped
// just because a WithPostFoldInjectionCallback-aware strategy is in play.
//
// (true, true) is the composition that must hold explicitly: both
// sources fire on the SAME fold (a legal config — an active goal alongside a
// self-injecting strategy), S=2, proving compactionEmitFunc's injectedTurns()
// sums len(pendingSteering) and strategyInjected correctly rather than one
// clobbering, doubling, or masking the other.
//
// All three variants are ground-truthed against the actual post-fold
// position of a uniquely-marked turn, not a re-derived formula.
func testManageContextShrinksBaselineWithStrategy(t *testing.T, withGoalSteering, withCrystal bool) {
	t.Helper()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		ContextStrategy:  "memory-crystals",
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true},
	}))
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	seedNumberedSessionHistory(t, s, 12) // multiple of 3 (AfterAction's crystallize cadence) and > PreserveRecentTurns(6)
	if withGoalSteering {
		s.getOrCreateGoalStore().Set("Ship the feature", time.Now()) // goalCompactionSteering injects 1 turn, unconsumed, every fold
	}

	mc, ok := s.strategy.(*contextmgr.MemoryCrystalsStrategy)
	if !ok {
		t.Fatalf("expected *contextmgr.MemoryCrystalsStrategy, got %T", s.strategy)
	}
	if withCrystal {
		if err := mc.AfterAction(context.Background(), currentHistory(t, s), s.client); err != nil {
			t.Fatalf("AfterAction: %v", err)
		}
	}
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80), < SummarizeThreshold(0.95): checkpoint layer only

	preHistory := currentHistory(t, s)
	baselineIdx := len(preHistory) - 3 // last 3 turns simulate the in-flight turn
	baselineText := preHistory[baselineIdx].Message.Text()
	s.mu.Lock()
	s.turnHistoryBaseline = baselineIdx
	s.mu.Unlock()

	var timings events.RoundTimings
	_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}

	foundCrystalMarker := false
	foundGoalMarker := false
	for _, turn := range postHistory {
		if turn.Kind != schema.TurnSteering {
			continue
		}
		if strings.Contains(turn.Message.Text(), "[MEMORY CRYSTALS]") {
			foundCrystalMarker = true
		}
		if turn.SteeringKind == events.SteeringKindGoalObjective {
			foundGoalMarker = true
		}
	}
	if withCrystal != foundCrystalMarker {
		t.Fatalf("memory-crystals steering turn present=%v, want %v (withCrystal) — test setup doesn't exercise what it claims", foundCrystalMarker, withCrystal)
	}
	if withGoalSteering != foundGoalMarker {
		t.Fatalf("goal-objective steering turn present=%v, want %v (withGoalSteering) — test setup doesn't exercise what it claims", foundGoalMarker, withGoalSteering)
	}

	wantBaseline := indexOfTurnText(postHistory, baselineText)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold — test setup invalid", baselineText)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

func TestSession_ManageContext_ShrinksBaselineOnFold_WithStrategyInjectedSteering(t *testing.T) {
	t.Parallel()
	testManageContextShrinksBaselineWithStrategy(t, false, true)
}

// TestSession_ManageContext_ShrinksBaselineOnFold_WithGoalSteering_StrategyPresentButInactive
// proves the callback no-op path composes: a memory-crystals strategy is
// active (so WithPostFoldInjectionCallback is wired the same way the S=2 test
// below exercises it) but never triggers (no crystal seeded), while a goal
// injects through runPreCompactHook. The count must be exactly the hook's
// one turn — no double-count or drop just because a
// WithPostFoldInjectionCallback-capable strategy happens to be configured.
func TestSession_ManageContext_ShrinksBaselineOnFold_WithGoalSteering_StrategyPresentButInactive(t *testing.T) {
	t.Parallel()
	testManageContextShrinksBaselineWithStrategy(t, true, false)
}

// TestSession_ManageContext_ShrinksBaselineOnFold_WithGoalAndStrategyInjectedSteering
// is the S=2 composition: an active goal
// (hook-side injection) and a self-injecting strategy (strategy-side
// injection) both fire on the same fold — a legal config the two isolated
// S=1 variants above cannot see, since compactionEmitFunc's injectedTurns()
// claim is precisely that it SUMS the two sources rather than reporting
// whichever fired.
func TestSession_ManageContext_ShrinksBaselineOnFold_WithGoalAndStrategyInjectedSteering(t *testing.T) {
	t.Parallel()
	testManageContextShrinksBaselineWithStrategy(t, true, true)
}

// TestSession_ManageContext_ShrinksBaselineOnFold_MarkerBeforeBaseline pins
// marker-before-baseline correction: a strategy marker (memory-crystals here)
// planted in an EARLIER turn can end up sitting BEFORE turnHistoryBaseline by
// the time a LATER turn's fold refreshes it. Removing a pre-baseline marker
// shifts every in-flight turn left by one; the marker's own re-append (always
// at the end, after baseline) does not restore that. The net turn-count delta
// from that swap is 0 (one removed, one added), so
// WithPostFoldInjectionCallback's plain net-delta report can't see the shift
// -- it needs to know the removal specifically happened before the boundary,
// not just that a removal and an append happened somewhere.
//
// Sequence: round 0 seeds a crystal and plants its marker at the tail, which
// becomes part of "prior" history once baseline is captured at the end of
// round 0. Three more turns are appended directly (simulating tool-round
// activity), landing after baseline. Pressure is then forced high enough that
// round 1's checkpoint folds a prefix that preserves the marker as a distinct
// turn (not yet swallowed into the checkpoint) but still ahead of the new
// in-flight turns -- exactly the shape that requires the marker to be
// refreshed (removed and re-appended) while it is behind the boundary.
// Ground-truthed against the actual post-fold position of the first
// genuinely in-flight turn, not a re-derived formula.
func TestSession_ManageContext_ShrinksBaselineOnFold_MarkerBeforeBaseline(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		ContextStrategy:  "memory-crystals",
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true},
	}))
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	seedNumberedSessionHistory(t, s, 12) // multiple of 3 (AfterAction's crystallize cadence) and > PreserveRecentTurns(6)

	mc, ok := s.strategy.(*contextmgr.MemoryCrystalsStrategy)
	if !ok {
		t.Fatalf("expected *contextmgr.MemoryCrystalsStrategy, got %T", s.strategy)
	}
	if err := mc.AfterAction(context.Background(), currentHistory(t, s), s.client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}

	// Round 0: plants the crystal marker (first injection, no prior marker to
	// remove) and captures baseline immediately after -- the marker is
	// "prior" content, exactly like production's round==0 reset.
	var timings events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings); err != nil {
		t.Fatalf("prepareModelRequestWithError round 0: %v", err)
	}

	afterRoundZero := currentHistory(t, s)
	foundMarkerAfterRoundZero := false
	for _, turn := range afterRoundZero {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), "[MEMORY CRYSTALS]") {
			foundMarkerAfterRoundZero = true
		}
	}
	if !foundMarkerAfterRoundZero {
		t.Fatal("round 0 didn't plant a memory-crystals marker -- test setup invalid")
	}

	// Simulate tool-round activity after round 0: turns appended directly,
	// landing after baseline (which round 0 just set to len(history) at that
	// point) -- these, not the marker, are the true in-flight turns.
	const inFlight0, inFlight1, inFlight2 = "inflight 0", "inflight 1", "inflight 2"
	s.mu.Lock()
	s.history = append(s.history,
		schema.NewTurn(schema.TurnUserInput, llm.User(inFlight0)),
		schema.NewTurn(schema.TurnUserInput, llm.User(inFlight1)),
		schema.NewTurn(schema.TurnUserInput, llm.User(inFlight2)),
	)
	baselineIdx := s.turnHistoryBaseline
	s.mu.Unlock()

	preHistory := currentHistory(t, s)
	if baselineIdx < 0 || baselineIdx >= len(preHistory) || preHistory[baselineIdx].Message.Text() != inFlight0 {
		t.Fatalf("test setup invalid: turnHistoryBaseline=%d does not point at %q in %d-turn history", baselineIdx, inFlight0, len(preHistory))
	}
	markerIdx := indexOfSteeringMarker(preHistory, "[MEMORY CRYSTALS]")
	if markerIdx < 0 || markerIdx >= baselineIdx {
		t.Fatalf("test setup invalid: crystal marker at index %d is not before baseline %d", markerIdx, baselineIdx)
	}

	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80), < SummarizeThreshold(0.95): checkpoint layer only, small enough cutoff to preserve the marker as a distinct turn

	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &timings); err != nil {
		t.Fatalf("prepareModelRequestWithError round 1: %v", err)
	}

	postHistory := currentHistory(t, s)
	if len(postHistory) >= len(preHistory) {
		t.Fatalf("test setup didn't force an actual fold: history len %d -> %d", len(preHistory), len(postHistory))
	}
	wantBaseline := indexOfTurnText(postHistory, inFlight0)
	if wantBaseline < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold -- test setup invalid", inFlight0)
	}

	s.mu.Lock()
	gotBaseline := s.turnHistoryBaseline
	s.mu.Unlock()

	if gotBaseline != wantBaseline {
		t.Errorf("turnHistoryBaseline = %d after a %d-turn fold (history %d -> %d), want %d (the in-flight turn's actual post-fold index)",
			gotBaseline, len(preHistory)-len(postHistory), len(preHistory), len(postHistory), wantBaseline)
	}
}

// indexOfSteeringMarker returns the index of the first TurnSteering turn
// whose message text contains marker, or -1 if none matches.
func indexOfSteeringMarker(history []schema.Turn, marker string) int {
	for i, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), marker) {
			return i
		}
	}
	return -1
}

// TestPrepareModelRequestWithError_DoesNotClobberConcurrentPublish pins
// atomic snapshot capture in prepareModelRequestWithError's inline fold-retry
// loop: historyTurns and snapRevision must be captured under ONE lock (as
// foldWithForceCompact does), because capturing snapRevision separately --
// after maybeElicitNoteBeforeCompaction's potentially multi-second call --
// lets a competing publish landing in that window bump s.historyRevision
// BEFORE snapRevision is read, so the later equality check in
// publishFoldedHistory passes and
// the stale fold (built from the pre-competing-fold snapshot) clobbers the
// competing publish. This is the inline-loop counterpart of
// TestSessionCompact_DoesNotClobberConcurrentCompaction.
//
// No sleep-based synchronization: s.elicitNoteFn is a direct test seam that
// blocks on a channel, standing in for maybeElicitNoteBeforeCompaction's real
// (potentially slow) LLM call. The competing publish is simulated directly
// through publishFoldedHistory -- the same primitive any real competing
// publisher (Compact(), applyPendingForceCompact, the content-filter retry)
// goes through -- landing precisely in the vulnerable window.
func TestPrepareModelRequestWithError_DoesNotClobberConcurrentPublish(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	seedNumberedSessionHistory(t, s, 12) // > PreserveRecentTurns(6): forces an actual checkpoint fold once pressure is high

	entered := make(chan struct{})
	proceed := make(chan struct{})
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		close(entered)
		<-proceed
		return "", nil
	}

	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80): maybeElicitNoteBeforeCompaction's pressure guard passes, so it reaches (and blocks in) the seam above

	var timings events.RoundTimings
	prepErr := make(chan error, 1)
	go func() {
		_, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 1, &timings)
		prepErr <- err
	}()

	<-entered // blocked inside maybeElicitNoteBeforeCompaction, past historyTurns' snapshot but before snapRevision was (buggily) read separately

	// Simulate a competing fold's publish landing in exactly this window,
	// directly through publishFoldedHistory -- the same seam every real
	// ForceCompact/ManageContext caller now shares.
	const competingMarker = "competing fold's published summary"
	competingResult := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\n"+competingMarker+"\n[END SUMMARY]")),
	}
	s.mu.Lock()
	snapLen := len(s.history)
	snapRevision := s.historyRevision
	_, publishedCompeting := s.publishFoldedHistory(snapLen, snapRevision, competingResult)
	s.mu.Unlock()
	if !publishedCompeting {
		t.Fatal("test setup: the simulated competing publish itself unexpectedly conflicted")
	}

	close(proceed) // let the blocked elicit-note call return

	if err := <-prepErr; err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	found := false
	for _, turn := range currentHistory(t, s) {
		if strings.Contains(turn.Message.Text(), competingMarker) {
			found = true
		}
	}
	if !found {
		t.Fatal("the competing fold's published content is gone — a stale fold clobbered it instead of detecting the conflict and retrying")
	}
}

func TestSession_AssistantTextStart_IncludesModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Model:   "test-model-42",
					Message: llm.Assistant("hello"),
				})
			},
		},
	})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("test-model-42")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventAssistantTextStart {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no ASSISTANT_TEXT_START event found")
	}
	d, ok := found.Data.(events.AssistantTextStartData)
	if !ok || d.Model != "test-model-42" {
		t.Fatalf("expected model 'test-model-42' in ASSISTANT_TEXT_START, got: %v", found.Data)
	}
}

func TestSession_StreamsCommunicateToolArgumentsAsAssistantDeltas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	comm := communicateCall("c1", "Hello")
	f := &streamingAdapter{
		name:           "openai",
		completeResult: toolCallResponse(comm),
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallStart,
				ToolCall: &llm.ToolCallData{
					ID:   "c1",
					Name: "communicate",
					Type: "function",
				},
			})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallDelta,
				ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Arguments: json.RawMessage(`{"message":"Hel`),
				},
			})
			st.Send(llm.StreamEvent{
				Type: llm.StreamEventToolCallDelta,
				ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Arguments: json.RawMessage(`lo","end_turn":true,"output":{"message":"","data":{},"artifacts":[]}}`),
				},
			})
			st.Send(llm.StreamEvent{
				Type:     llm.StreamEventToolCallEnd,
				ToolCall: &comm,
			})
			finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	if out != "Hello" {
		t.Fatalf("ProcessInput output = %q, want Hello", out)
	}
	completeCalls, streamCalls := f.Counts()
	if completeCalls != 0 || streamCalls != 1 {
		t.Fatalf("model calls: complete=%d stream=%d, want complete=0 stream=1", completeCalls, streamCalls)
	}

	var deltas []string
	for _, ev := range evs {
		if ev.Kind != events.EventCommunicatePreviewDelta {
			continue
		}
		data, ok := ev.Data.(events.CommunicatePreviewDeltaData)
		if !ok {
			t.Fatalf("COMMUNICATE_PREVIEW_DELTA data type = %T", ev.Data)
		}
		if data.CallID != "c1" {
			t.Fatalf("preview call id = %q, want c1", data.CallID)
		}
		deltas = append(deltas, data.Delta)
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("assistant deltas = %q, want chunks composing Hello; events=%v", deltas, evs)
	}
	if len(deltas) < 2 {
		t.Fatalf("assistant deltas = %q, want multiple streamed chunks", deltas)
	}
}

func TestPartialJSONStringFieldDecodesUnicodeEscapes(t *testing.T) {
	t.Parallel()
	got, ok := partialJSONStringField(`{"message":"Hello \u263A"}`, "message")
	if !ok {
		t.Fatal("message field not found")
	}
	if got != "Hello \u263A" {
		t.Fatalf("message=%q, want decoded unicode escape", got)
	}

	got, ok = partialJSONStringField(`{"message":"Hello \ud83d\ude00"}`, "message")
	if !ok {
		t.Fatal("message field with surrogate pair not found")
	}
	if got != "Hello 😀" {
		t.Fatalf("message=%q, want decoded surrogate pair", got)
	}

	got, ok = partialJSONStringField(`{"message":"Hello \u26`, "message")
	if !ok {
		t.Fatal("partial message field not found")
	}
	if got != "Hello " {
		t.Fatalf("partial message=%q, want prefix before incomplete unicode escape", got)
	}
}

func TestAssistantTextEnd_EnrichedData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	reasoningTokens := 42
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					Model:  "gpt-5.2",
					Finish: llm.FinishReason{Reason: "stop"},
					Usage: llm.Usage{
						InputTokens:     100,
						OutputTokens:    50,
						TotalTokens:     150,
						ReasoningTokens: &reasoningTokens,
					},
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this"}},
							{Kind: llm.ContentText, Text: "here is my answer"},
						},
					},
				})
			},
		},
	})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Collect events.
	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()

	_, err = sess.ProcessInput(ctx, "test", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	// Find the ASSISTANT_TEXT_END event.
	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventAssistantTextEnd {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no ASSISTANT_TEXT_END event found; events: %v", evs)
	}

	// Type-assert to the typed payload struct.
	endData, ok := found.Data.(events.AssistantTextEndData)
	if !ok {
		t.Fatalf("expected AssistantTextEndData, got %T", found.Data)
	}

	// Verify text.
	if endData.Text != "here is my answer" {
		t.Fatalf("text: got %q want %q", endData.Text, "here is my answer")
	}

	// Verify reasoning.
	if endData.Reasoning != "let me think about this" {
		t.Fatalf("reasoning: got %q want %q", endData.Reasoning, "let me think about this")
	}

	// Verify finish_reason.
	if endData.FinishReason != "stop" {
		t.Fatalf("finish_reason: got %q want %q", endData.FinishReason, "stop")
	}

	// Verify model.
	if endData.Model != "gpt-5.2" {
		t.Fatalf("model: got %q want %q", endData.Model, "gpt-5.2")
	}

	// Verify provider: with Model it names the instance/model reference the
	// round's cost resolves from (spec §7.5).
	if endData.Provider != "openai" {
		t.Fatalf("provider: got %q want %q", endData.Provider, "openai")
	}

	// Verify usage is present and has expected values.
	usage := endData.Usage
	if usage.InputTokens != 100 {
		t.Fatalf("usage.input_tokens: got %d want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Fatalf("usage.output_tokens: got %d want 50", usage.OutputTokens)
	}
	if usage.ReasoningTokens == nil || *usage.ReasoningTokens != 42 {
		t.Fatalf("usage.reasoning_tokens: got %v want 42", usage.ReasoningTokens)
	}
}

// kata 3xbh: when the model API call fails mid-session and retries are
// exhausted, ProcessInput must return the provider error to the caller
// rather than swallow it and return ("", nil). Toil and other callers cannot
// distinguish "agent finished" from "provider died" otherwise.
func TestSession_ProvideErrorReturnsErrorToCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// Adapter that returns a non-retryable stream-open error mimicking the
	// gpt-5.4 "failed on both endpoints" production failure: the provider
	// stream cannot be opened at all.
	f := &streamingAdapter{
		name: "openai",
		streamErr: llm.NewStreamError("openai",
			`openai: model "gpt-5.4" failed on both endpoints — `+
				`/v1/responses: empty stream (model not supported); `+
				`/v1/chat/completions: openai error (status=403)`, nil),
	}
	c.Register(f)

	// Zero retries so the test runs fast: one attempt, fail, propagate.
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("ProcessInput: got nil error, want provider error (got output %q)", out)
	}
	// The caller needs enough context to identify the failure as a provider
	// error, not an agent-quiescence. The wrapper must mention "provider" so
	// callers (e.g. toil) can pattern-match without parsing adapter-specific
	// error message formats.
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "provider") {
		t.Fatalf("ProcessInput error message does not contain \"provider\": %q", msg)
	}
	// And the original adapter detail must still be reachable via Unwrap so
	// callers that want to log the underlying cause can still get it.
	if !strings.Contains(msg, "failed on both endpoints") && !strings.Contains(msg, "openai") {
		t.Fatalf("ProcessInput error message does not retain adapter detail: %q", msg)
	}
	// The underlying llm.Error must remain reachable via errors.As so callers
	// inspecting status codes / provider names continue to work.
	if _, ok := errors.AsType[llm.Error](err); !ok {
		t.Fatalf("ProcessInput error does not unwrap to llm.Error: %v", err)
	}
}

func TestSession_NonRetryableProviderErrorLeavesSessionIdle(t *testing.T) {
	t.Parallel()
	adapter := &fakeErrAdapter{
		name: "kimi-anthropic",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus(
					"kimi-anthropic", http.StatusForbidden,
					"billing-cycle quota exhausted", nil, nil,
				)
			},
			func(llm.Request) (llm.Response, error) {
				return toolCallResponse(communicateCall("after_failure", "recovered")), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(
		client,
		namedInstanceProfile("kimi-anthropic", "kimi-for-coding", "k3"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{LLMRetryPolicy: &policy},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.getOrCreateGoalStore().Set("recover from provider failure", sess.sclock().Now())

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	_, processErr := sess.ProcessInput(context.Background(), "trigger quota failure", nil)
	if processErr == nil || !strings.Contains(processErr.Error(), "billing-cycle quota exhausted") {
		t.Fatalf("ProcessInput error = %v, want quota detail", processErr)
	}
	if _, ok := errors.AsType[llm.Error](processErr); !ok {
		t.Fatalf("ProcessInput error does not unwrap to llm.Error: %v", processErr)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests after failure = %d, want 1", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after provider failure = %q, want %q", got, SessionIdle)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusBlocked {
		t.Fatalf("goal after provider failure = %+v, want blocked", snap)
	}

	out, secondErr := sess.ProcessInput(context.Background(), "try again", nil)
	if secondErr != nil || out != "recovered" {
		t.Fatalf("second ProcessInput = (%q, %v), want (%q, nil)", out, secondErr, "recovered")
	}
	sess.Close()
	<-done

	var errorEvents, turnEndedEvents, goalEndedEvents int
	var blockedGoalEnded int
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventError:
			errorEvents++
		case events.EventTurnEnded:
			turnEndedEvents++
		case events.EventGoalEnded:
			goalEndedEvents++
			if data, ok := ev.Data.(events.GoalEndedData); ok && data.Status == string(goal.StatusBlocked) {
				blockedGoalEnded++
			}
		}
	}
	if errorEvents == 0 {
		t.Fatal("events contain no error event")
	}
	if turnEndedEvents == 0 {
		t.Fatal("events contain no turn-ended event")
	}
	if goalEndedEvents != 1 || blockedGoalEnded != 1 {
		t.Fatalf("provider failure emitted goal-ended events = %d (blocked = %d), want exactly one blocked report", goalEndedEvents, blockedGoalEnded)
	}
}

// kata 3xbh: agent quiescence — the agent calls communicate and finishes
// its turn — must still return (output, nil). This is the today-success
// case; do not break it.
func TestSession_AgentQuiescenceReturnsNilError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	comm := communicateCall("c1", "done")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: got error %v (output %q), want nil error", err, out)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput output: got %q want %q", out, "done")
	}
}

func TestSession_ProviderErrorDoesNotRecordAssistantTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name: "openai",
		streamErr: llm.NewStreamError("openai",
			`openai: model "gpt-5.4" failed on both endpoints — `+
				`/v1/responses: empty stream (model not supported); `+
				`/v1/chat/completions: openai error (status=403)`, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatal("ProcessInput: got nil error, want provider error")
	}
	tpath := sess.TranscriptPath()
	sess.Close()
	if tpath == "" {
		t.Fatal("TranscriptPath is empty; session lacks state dir")
	}

	data, rerr := readTranscriptFull(tpath)
	if rerr != nil {
		t.Fatalf("readTranscriptFull: %v", rerr)
	}
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			t.Fatalf("terminal provider failure recorded assistant turn: %+v", entry.Turn)
		}
	}
}

func TestSession_SingleAttemptMetadataRecorded(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	comm := communicateCall("c1", "ok")
	client.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				resp := toolCallResponse(comm)
				resp.ID = "resp_1"
				resp.Provider = "openai"
				resp.Model = req.Model
				resp.Raw = map[string]any{"endpoint_url": "https://api.openai.com/v1/responses"}
				return resp
			},
		},
	})
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	if _, err := sess.ProcessInput(context.Background(), "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	tpath := sess.TranscriptPath()
	sess.Close()
	if tpath == "" {
		t.Fatal("TranscriptPath is empty")
	}
	data, err := readTranscriptFull(tpath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if len(data.Entries) == 0 {
		t.Fatalf("no transcript entries")
	}
	var assistant schema.Turn
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			assistant = entry.Turn
		}
	}
	if assistant.Kind == "" {
		t.Fatalf("no assistant transcript entry")
	}
	if assistant.ResponseID != "resp_1" {
		t.Fatalf("ResponseID = %q", assistant.ResponseID)
	}
	if assistant.ResponseProvider != "openai" ||
		assistant.ResponseModel != "gpt-5.2" ||
		assistant.ResponseRequestModel != "gpt-5.2" ||
		assistant.ResponseEndpoint != "https://api.openai.com/v1/responses" {
		t.Fatalf("assistant response metadata = %+v", assistant)
	}
	if !strings.HasPrefix(assistant.AttemptGroupID, "ag_") {
		t.Fatalf("AttemptGroupID = %q", assistant.AttemptGroupID)
	}
	if err := identifier.ValidateAgentCallID(assistant.AttemptGroupID); err != nil {
		t.Fatalf("AttemptGroupID %q: %v", assistant.AttemptGroupID, err)
	}
	if assistant.ResponseContextMarker != "" ||
		assistant.ResponseRequestFingerprint != "" ||
		assistant.ResponseStorageScopeFingerprint != "" {
		t.Fatalf("anchor eligibility metadata should stay empty in Phase 1A: %+v", assistant)
	}
	// An override serves this session, so no API attempt is begun and the
	// turn records no wire protocol.
	if assistant.ResponseProtocol != "" {
		t.Fatalf("ResponseProtocol = %q, want empty for an override-served turn", assistant.ResponseProtocol)
	}
}

func TestSession_SanitizesCustomAdapterEndpointMetadata(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{
			name:     "credential components",
			endpoint: "https://endpoint-user:endpoint-password@provider.test/v1/responses?credential=endpoint-query#endpoint-fragment",
			want:     "https://provider.test/v1/responses",
		},
		{
			name:     "invalid endpoint",
			endpoint: "://not-a-valid-endpoint?credential=endpoint-query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			client := llm.NewClient()
			client.Register(&fakeAdapter{
				name: "openai",
				steps: []func(req llm.Request) llm.Response{
					func(llm.Request) llm.Response {
						resp := toolCallResponse(communicateCall("c1", "ok"))
						resp.Raw = map[string]any{"endpoint_url": tt.endpoint}
						return resp
					},
				},
			})
			sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			go func() {
				for range sess.Events() {
				}
			}()
			if _, err := sess.ProcessInput(context.Background(), "hi", nil); err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			transcriptPath := sess.TranscriptPath()
			sess.Close()

			data, err := readTranscriptFull(transcriptPath)
			if err != nil {
				t.Fatalf("readTranscriptFull: %v", err)
			}
			var assistant schema.Turn
			for _, entry := range data.Entries {
				if entry.Turn.Kind == schema.TurnAssistant {
					assistant = entry.Turn
				}
			}
			if got := assistant.ResponseEndpoint; got != tt.want {
				t.Fatalf("ResponseEndpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSingleAttemptRequestMetadataCreatesSemanticGroup(t *testing.T) {
	req, attempt := singleAttemptRequestMetadata(llm.Request{
		Model:       "gpt-5.2",
		Provider:    "openai",
		HistoryMode: llm.HistoryModeResponsesDelta,
		Continuation: &llm.ContinuationMetadata{
			PreviousResponseIDHash: "cont-handle-v1:response_id:abc",
		},
	})

	if !strings.HasPrefix(attempt.AttemptGroupID, "ag_") {
		t.Fatalf("AttemptGroupID = %q", attempt.AttemptGroupID)
	}
	if req.Continuation == nil || req.Continuation.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("request continuation = %+v", req.Continuation)
	}
	if attempt.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("PreviousResponseIDHash = %q", attempt.PreviousResponseIDHash)
	}
}

// kata ts0x: when the agent loop terminates a turn because of a provider
// failure (HTTP error from an adapter), the EventError it emits must carry a
// structured Cause field. Downstream consumers (toil, hub, debug-run) today
// substring-match the error message; a typed Cause lets them dispatch
// reliably on provider/model/status.
func TestProviderErrorEmitsStructuredCause(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected provider error from ProcessInput")
	}
	sess.Close()
	<-done

	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventError {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no EventError emitted for provider failure")
	}
	d, ok := found.Data.(events.ErrorData)
	if !ok {
		t.Fatalf("EventError data: got %T, want ErrorData", found.Data)
	}
	if d.Cause == nil {
		t.Fatal("ErrorData.Cause is nil; expected structured provider cause")
	}
	if got, want := d.Cause.Kind, "provider"; got != want {
		t.Errorf("Cause.Kind: got %q want %q", got, want)
	}
	if got, want := d.Cause.Provider, "openai"; got != want {
		t.Errorf("Cause.Provider: got %q want %q", got, want)
	}
	if got, want := d.Cause.Model, "gpt-5.2"; got != want {
		t.Errorf("Cause.Model: got %q want %q", got, want)
	}
	if got, want := d.Cause.Status, 403; got != want {
		t.Errorf("Cause.Status: got %d want %d", got, want)
	}
}

func TestProviderErrorTranscriptRemainsSemanticOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected provider error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	sess.Close()
	if tpath == "" {
		t.Fatal("TranscriptPath is empty; session lacks state dir")
	}

	data, rerr := readTranscriptFull(tpath)
	if rerr != nil {
		t.Fatalf("readTranscriptFull: %v", rerr)
	}
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			t.Fatalf("provider error recorded assistant turn: %+v", entry.Turn)
		}
	}
}

// kata ts0x: non-provider errors (anything that does not unwrap to llm.Error
// with a non-empty Provider) must leave ErrorData.Cause nil. Back-compat: a
// nil Cause is the explicit signal that the failure source is unknown.
func TestNonProviderErrorOmitsCause(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: errors.New("opaque non-llm failure"),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.getOrCreateGoalStore().Set("preserve generic failure behavior", sess.sclock().Now())

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected non-nil error from ProcessInput")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after non-provider failure = %q, want %q", got, SessionIdle)
	}
	if snap, ok := sess.getOrCreateGoalStore().Snapshot(); !ok || snap.Status != goal.StatusBlocked {
		t.Fatalf("goal after non-provider failure = %+v, want blocked", snap)
	}
	sess.Close()
	<-done

	var found *events.SessionEvent
	for i, ev := range evs {
		if ev.Kind == events.EventError {
			found = &evs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no EventError emitted for non-provider failure")
	}
	d, ok := found.Data.(events.ErrorData)
	if !ok {
		t.Fatalf("EventError data: got %T, want ErrorData", found.Data)
	}
	if d.Cause != nil {
		t.Fatalf("ErrorData.Cause: got %+v, want nil for non-provider error", d.Cause)
	}
}

// kata 4zn8: a rate limit rejected at stream open produces no partial output,
// so the only retry-triggered event evener had (EventAssistantTextReset, gated on
// partial) never fires. The session went silent for the whole retry chain and a
// 429 storm was indistinguishable from a hang. Each retry must announce itself
// on the event bus with the attempt number, the wait, and why.
func TestSession_EmitsRetryEventWhenRateLimitedAtStreamOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 429, "rate limit exceeded", nil, nil),
	}
	c.Register(f)

	var mu sync.Mutex
	var retries []events.ModelRetryData
	done := make(chan struct{})

	noSleep := func(context.Context, time.Duration) error { return nil }
	policy := llm.RetryPolicy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Second}
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       noSleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventModelRetry {
				mu.Lock()
				retries = append(retries, ev.Data.(events.ModelRetryData))
				mu.Unlock()
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected failure after exhausting the retry budget")
	}
	sess.Close()
	<-done

	mu.Lock()
	got := append([]events.ModelRetryData(nil), retries...)
	mu.Unlock()

	// One event before each of the 2 retries; the initial attempt is not a retry.
	if len(got) != 2 {
		t.Fatalf("model-retry events = %d, want 2 (one before each retry)", len(got))
	}
	for i, ev := range got {
		wantAttempt := i + 1
		if ev.Attempt != wantAttempt {
			t.Errorf("event %d: Attempt = %d, want %d", i, ev.Attempt, wantAttempt)
		}
		if ev.MaxAttempts != 3 {
			t.Errorf("event %d: MaxAttempts = %d, want 3 (1 initial + 2 retries)", i, ev.MaxAttempts)
		}
		if ev.ErrorClass != "rate_limit" {
			t.Errorf("event %d: ErrorClass = %q, want %q", i, ev.ErrorClass, "rate_limit")
		}
		if ev.StatusCode != http.StatusTooManyRequests {
			t.Errorf("event %d: StatusCode = %d, want 429", i, ev.StatusCode)
		}
		// The wait is the whole point of the event: a reader has to be able to
		// tell "back in 32s" from "wedged".
		if ev.DelayMS <= 0 {
			t.Errorf("event %d: DelayMS = %d, want a positive wait", i, ev.DelayMS)
		}
	}
}
