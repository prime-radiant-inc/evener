package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       noSleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	resets := 0
	done := make(chan struct{})

	noSleep := func(context.Context, time.Duration) error { return nil }
	policy := llm.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       noSleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			if ev.Kind == events.EventAssistantTextReset {
				mu.Lock()
				resets++
				mu.Unlock()
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	gotResets := resets
	mu.Unlock()
	// One reset before each of the 3 retries: the partial shown by each failed
	// attempt is discarded before the next attempt streams.
	if gotResets != 3 {
		t.Fatalf("assistant-text resets = %d, want 3 (one before each retry after partial)", gotResets)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// TestSession_EmitsReasoningSummaryDelta verifies the serf harness no longer
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, LLMRetryPolicy: &policy})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	// the loop hits the exhausted exit. meta.json must reflect that — non-zero.
	if meta.TurnCount == 0 {
		t.Fatalf("turn_count after empty-response exhaustion: got 0, want >0 (deferred flush should persist modelResponses)")
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	if meta.TurnCount == 0 {
		t.Fatalf("turn_count after bare-text exhaustion: got 0, want >0 (deferred flush should persist modelResponses)")
	}
}

// kata ztne: when MaxToolRoundsPerInput is reached, processOneInput falls
// out of the run loop and returns nil error. The deferred flush must run.
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput (max-rounds exit): %v", err)
	}
	sessID := sess.ID()
	sess.Close()

	meta, err := schema.LoadSessionMeta(dir, sessID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta.TurnCount == 0 {
		t.Fatalf("turn_count after MaxToolRoundsPerInput exit: got 0, want >0 (deferred flush should persist modelResponses)")
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	_, err = sess.ProcessInput(context.Background(), "cancel", nil)
	if err == nil {
		t.Fatal("expected abort error")
	}
	var abort *llm.AbortError
	if !errors.As(err, &abort) {
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2, // Only 2 real rounds allowed.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx := context.Background()

	// First call: web search response with inflated tokens.
	if _, err := sess.ProcessInput(ctx, "search something", nil); err != nil {
		t.Fatal(err)
	}

	// The inflated 200K should NOT have been recorded as lastInputTokens.
	lit := sess.contextMgr.LastInputTokens()
	if lit == 200_000 {
		t.Fatalf("lastInputTokens = %d; inflated web search tokens should not be recorded", lit)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("test-model-42"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
		if ev.Kind != events.EventAssistantTextDelta {
			continue
		}
		data, ok := ev.Data.(events.AssistantTextDeltaData)
		if !ok {
			t.Fatalf("ASSISTANT_TEXT_DELTA data type = %T", ev.Data)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	var le llm.Error
	if !errors.As(err, &le) {
		t.Fatalf("ProcessInput error does not unwrap to llm.Error: %v", err)
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: got error %v (output %q), want nil error", err, out)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput output: got %q want %q", out, "done")
	}
}

// kata 3xbh: when ProcessInput returns the provider error to the caller, the
// transcript MUST still record the api_call error entry. This preserves the
// existing observability surface — debug-run, dashboards, the session viewer
// all rely on the per-round api_call records.
func TestSession_ProviderErrorStillRecordsTranscriptEntry(t *testing.T) {
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	// Find the api_call entry that recorded the provider failure.
	var found *transcript.APICall
	for i := range data.APICalls {
		if data.APICalls[i].Error != "" {
			found = &data.APICalls[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("transcript has no api_call entry with Error set; got %d api_calls", len(data.APICalls))
	}
	if !strings.Contains(found.Error, "failed on both endpoints") &&
		!strings.Contains(found.Error, "openai") {
		t.Fatalf("api_call.Error does not contain provider failure text: %q", found.Error)
	}
}

func TestSession_TranscriptAPICallRecordsFullToolDefinitions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 500, "boom", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatalf("ProcessInput: got nil error, want provider error")
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
	if len(data.APICalls) == 0 {
		t.Fatalf("no api calls recorded")
	}
	req := data.APICalls[0].Request
	if req.ToolCount == 0 || len(req.Tools) != req.ToolCount {
		t.Fatalf("request tools not fully recorded: count=%d tools=%+v", req.ToolCount, req.Tools)
	}
	var readFile *llm.ToolDefinition
	for i := range req.Tools {
		if req.Tools[i].Name == "read_file" {
			readFile = &req.Tools[i]
			break
		}
	}
	if readFile == nil {
		t.Fatalf("read_file tool missing from transcript tools: %+v", req.Tools)
	}
	if strings.TrimSpace(readFile.Description) == "" || readFile.Parameters["type"] != "object" {
		t.Fatalf("read_file definition incomplete: %+v", *readFile)
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
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
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
	if len(data.APICalls) != 1 {
		t.Fatalf("api_calls = %d", len(data.APICalls))
	}
	call := data.APICalls[0]
	if call.AttemptIndex != 1 || call.AttemptCount != 1 {
		t.Fatalf("attempt fields = %+v", call)
	}
	if !strings.HasPrefix(call.AttemptGroupID, "ag_") {
		t.Fatalf("AttemptGroupID = %q", call.AttemptGroupID)
	}
	if call.FinalAttemptCount == nil || *call.FinalAttemptCount != 1 {
		t.Fatalf("FinalAttemptCount = %v", call.FinalAttemptCount)
	}
	if call.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("HistoryMode = %q", call.HistoryMode)
	}
	if call.Request.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("request HistoryMode = %q", call.Request.HistoryMode)
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
	if assistant.ResponseContextMarker != "" ||
		assistant.ResponseRequestFingerprint != "" ||
		assistant.ResponseStorageScopeFingerprint != "" {
		t.Fatalf("anchor eligibility metadata should stay empty in Phase 1A: %+v", assistant)
	}
}

func TestSingleAttemptRequestMetadataKeepsAttemptCountersOffRequest(t *testing.T) {
	req, attempt := singleAttemptRequestMetadata(llm.Request{
		Model:       "gpt-5.2",
		Provider:    "openai",
		HistoryMode: llm.HistoryModeResponsesDelta,
		Continuation: &llm.ContinuationMetadata{
			PreviousResponseIDHash: "cont-handle-v1:response_id:abc",
		},
	})

	if attempt.AttemptIndex != 1 || attempt.AttemptCount != 1 {
		t.Fatalf("attempt counters = %+v", attempt)
	}
	if !strings.HasPrefix(attempt.AttemptGroupID, "ag_") {
		t.Fatalf("AttemptGroupID = %q", attempt.AttemptGroupID)
	}
	if req.Continuation == nil || req.Continuation.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("request continuation = %+v", req.Continuation)
	}
	if got := llm.BuildAPILogRequest(req); got.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("APILogRequest.PreviousResponseIDHash = %q", got.PreviousResponseIDHash)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

// kata ts0x (regression lock): the structured Cause on EventError must NOT
// suppress the existing transcript api_call entry. Consumers that already
// rely on the api_call.Error field (debug-run, dashboards, session viewer)
// must continue to see the raw adapter error text.
func TestProviderErrorTranscriptEntryStillRecorded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil),
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	var found *transcript.APICall
	for i := range data.APICalls {
		if data.APICalls[i].Error != "" {
			found = &data.APICalls[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("transcript has no api_call entry with Error set; got %d api_calls", len(data.APICalls))
	}
	if !strings.Contains(found.Error, "openai") {
		t.Fatalf("api_call.Error does not retain raw adapter detail: %q", found.Error)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected non-nil error from ProcessInput")
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
