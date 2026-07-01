package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This file fuzzes the model-call/stream-consumption seam of a Session:
//
//   - consumeModelStream (session_stream.go) — driven by a SCRIPTED llm.Stream
//     whose event sequence (text/tool-call/reasoning/finish/usage + a fault
//     ERROR event, plus a truncated-without-finish tail) is decoded from the
//     fuzzer's bytes. Oracle: it never panics; a stream that carries an ERROR
//     event or that ends without FINISH surfaces an error (never a lost turn);
//     and on success the assembled response is internally consistent (provider
//     /model stamped, finish usage/reason preserved, the streamed-assistant
//     bool and the sessionModelResponse field agree).
//
//   - callModelWithFallback (session_model_call.go) — driven through a real
//     Session and a fuzzed STREAMING adapter that faults at open (retryable /
//     permanent / stream-unsupported) or mid-stream, exercising RetryStream,
//     the responses-continuation full-history retry, and the model-fallback
//     chain. Oracle: it always terminates without panic, and a nil error
//     implies a well-formed response (stamped model/provider) and attempt
//     metadata.
//
//   - applyResponsesContinuationAnchorPlanning (session_model_call.go) — driven
//     with a fuzzed request/config/plan against a matching anchor turn. Oracle:
//     it never panics; the returned request always carries a non-empty
//     HistoryMode; and whenever it selects the responses-delta continuation the
//     request is internally consistent (a previous-response id and continuation
//     metadata are both present). It is also re-run to assert determinism.
//
// Lane anti-collision: every new top-level identifier is prefixed msfz_ /
// msfz; the shared newSession/agenttest helpers are reused, never redefined.

// --- byte cursor -----------------------------------------------------------

// msfzReader is a stable cursor over the fuzzer's bytes (out of bytes -> 0), so
// a short input decodes deterministically and a longer one is a strict
// superset. It mirrors the lifecycle harness's seqReader but is a distinct type
// to avoid colliding with that file's identifiers.
type msfzReader struct {
	data []byte
	pos  int
}

func (r *msfzReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *msfzReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *msfzReader) boolean() bool { return r.next()&1 == 1 }

// --- scripted stream -------------------------------------------------------

// msfzStream is a scripted llm.Stream: it exposes a fixed, pre-filled, closed
// event channel — the SAME channel on every Events() call, as every real
// adapter stream does. A fresh-channel-per-call implementation would deadlock
// the client's providerStampStream.pump, which re-evaluates inner.Events() on
// every select iteration and would re-read the first event forever. The channel
// is buffered to hold the whole (bounded) program plus a slot, so the producer
// never blocks and no goroutine leaks even when the consumer stops early. Close
// is a no-op (nothing to release).
type msfzStream struct {
	ch chan llm.StreamEvent
}

func newMsfzStream(events []llm.StreamEvent) *msfzStream {
	ch := make(chan llm.StreamEvent, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return &msfzStream{ch: ch}
}

func (s *msfzStream) Events() <-chan llm.StreamEvent { return s.ch }

func (s *msfzStream) Close() error { return nil }

// msfzStreamPlan is the decoded event program plus the terminal outcome the
// oracle expects consumeModelStream to reach for it.
type msfzStreamPlan struct {
	events      []llm.StreamEvent
	hasError    bool // an ERROR event appears anywhere in the stream
	hasFinish   bool // a FINISH event appears anywhere in the stream
	lastUsage   llm.Usage
	lastReason  llm.FinishReason
	sawUsage    bool
	sawFinishFR bool
}

var msfzToolIDs = []string{"call_a", "call_b", ""}
var msfzToolNames = []string{"communicate", "read_file", "shell", ""}
var msfzTextDeltas = []string{"", "hi", "hello world", "x"}
var msfzToolArgs = []string{
	``,
	`{"message":"hel`,
	`{"message":"hello"}`,
	`{"file_path":"a.txt"}`,
	`{"message":"hé`,
	`not json`,
}

// decodeMsfzStreamPlan maps fuzzer bytes to a scripted stream event program and
// records what the consumer's terminal outcome must be. Bounded to a small
// number of events so the fuzzer explores interleavings rather than plateauing
// on pathological sizes.
func decodeMsfzStreamPlan(data []byte) msfzStreamPlan {
	r := &msfzReader{data: data}
	nEvents := r.intn(12) + 1
	plan := msfzStreamPlan{events: make([]llm.StreamEvent, 0, nEvents)}
	for i := 0; i < nEvents; i++ {
		switch r.intn(8) {
		case 0:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:   llm.StreamEventTextStart,
				TextID: msfzTextID(r),
			})
		case 1:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:   llm.StreamEventTextDelta,
				TextID: msfzTextID(r),
				Delta:  msfzTextDeltas[r.intn(len(msfzTextDeltas))],
			})
		case 2:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:           llm.StreamEventReasoningDelta,
				ReasoningDelta: msfzTextDeltas[r.intn(len(msfzTextDeltas))],
			})
		case 3:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:     llm.StreamEventToolCallStart,
				ToolCall: msfzToolCall(r),
			})
		case 4:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:     llm.StreamEventToolCallDelta,
				ToolCall: msfzToolCall(r),
			})
		case 5:
			plan.events = append(plan.events, llm.StreamEvent{
				Type:     llm.StreamEventToolCallEnd,
				ToolCall: msfzToolCall(r),
			})
		case 6:
			reason := msfzFinishReason(r)
			usage := msfzUsage(r)
			plan.events = append(plan.events, llm.StreamEvent{
				Type:         llm.StreamEventFinish,
				FinishReason: &reason,
				Usage:        &usage,
			})
			plan.hasFinish = true
			plan.lastUsage = usage
			plan.sawUsage = true
			plan.lastReason = reason
			plan.sawFinishFR = true
		case 7:
			var err error
			if r.boolean() {
				err = llm.NewStreamError("openai", "fuzzed mid-stream error", nil)
			}
			plan.events = append(plan.events, llm.StreamEvent{
				Type: llm.StreamEventError,
				Err:  err,
			})
			plan.hasError = true
		}
	}
	return plan
}

func msfzTextID(r *msfzReader) string {
	switch r.intn(3) {
	case 0:
		return ""
	case 1:
		return "text_0"
	default:
		return "text_1"
	}
}

func msfzToolCall(r *msfzReader) *llm.ToolCallData {
	if r.intn(6) == 0 {
		return nil // exercises the nil-guard branches
	}
	return &llm.ToolCallData{
		ID:        msfzToolIDs[r.intn(len(msfzToolIDs))],
		Name:      msfzToolNames[r.intn(len(msfzToolNames))],
		Arguments: []byte(msfzToolArgs[r.intn(len(msfzToolArgs))]),
		Type:      "function",
	}
}

func msfzFinishReason(r *msfzReader) llm.FinishReason {
	switch r.intn(4) {
	case 0:
		return llm.FinishReason{Reason: llm.FinishReasonStop}
	case 1:
		return llm.FinishReason{Reason: llm.FinishReasonToolCalls}
	case 2:
		return llm.FinishReason{Reason: llm.FinishReasonLength}
	default:
		return llm.FinishReason{}
	}
}

func msfzUsage(r *msfzReader) llm.Usage {
	return llm.Usage{
		InputTokens:  r.intn(5000),
		OutputTokens: r.intn(5000),
		TotalTokens:  r.intn(10000),
	}
}

// --- consumeModelStream fuzz ----------------------------------------------

// FuzzMsfzConsumeModelStream drives consumeModelStream with a scripted,
// fuzzer-decoded event stream and asserts the terminal-outcome + assembled-
// response invariants.
func FuzzMsfzConsumeModelStream(f *testing.F) {
	f.Add([]byte{0, 1, 1, 6, 0})             // text start, delta, finish
	f.Add([]byte{6})                         // bare finish
	f.Add([]byte{7, 1})                      // error event (with Err)
	f.Add([]byte{7, 0})                      // error event (nil Err -> synthesized)
	f.Add([]byte{1, 1})                      // truncated: no finish
	f.Add([]byte{3, 1, 0, 4, 1, 0, 5, 1, 0}) // tool-call start/delta/end (communicate)
	f.Add([]byte{4, 1, 0, 1, 4, 1, 0, 2, 6}) // communicate preview deltas then finish
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 6}) // finish then error -> error wins
	f.Add([]byte{2, 2, 2, 6})                // reasoning then finish
	f.Add([]byte{3, 0, 0, 4, 0, 0, 5, 0, 0}) // nil tool-call guards
	f.Add([]byte{4, 1, 4, 4, 1, 4, 4, 1, 4}) // repeated partial-json communicate

	f.Fuzz(func(t *testing.T, data []byte) {
		plan := decodeMsfzStreamPlan(data)
		sess := newSession(t)

		req := llm.Request{
			Provider: "openai",
			Model:    "gpt-5.2",
			Messages: []llm.Message{llm.User("hi")},
		}
		st := newMsfzStream(plan.events)

		ctx := context.Background()
		modelResp, partial, err := sess.consumeModelStream(ctx, req, st)

		expectErr := plan.hasError || !plan.hasFinish
		if expectErr {
			if err == nil {
				t.Fatalf("consumeModelStream: want error (hasError=%v hasFinish=%v), got nil", plan.hasError, plan.hasFinish)
			}
			// Every error return path yields the zero sessionModelResponse.
			if modelResp.StreamedAssistant {
				t.Fatalf("consumeModelStream: error path leaked StreamedAssistant=true")
			}
			if modelResp.Response.Provider != "" || modelResp.Response.Model != "" {
				t.Fatalf("consumeModelStream: error path leaked a non-zero response: %+v", modelResp.Response)
			}
			return
		}

		if err != nil {
			t.Fatalf("consumeModelStream: unexpected error (hasFinish=%v): %v", plan.hasFinish, err)
		}
		// Success invariants: provider/model are always stamped (from req when
		// the stream did not carry them).
		if modelResp.Response.Provider == "" {
			t.Fatalf("consumeModelStream: success response missing Provider: %+v", modelResp.Response)
		}
		if modelResp.Response.Model == "" {
			t.Fatalf("consumeModelStream: success response missing Model: %+v", modelResp.Response)
		}
		// The partial bool and the response field must agree.
		if partial != modelResp.StreamedAssistant {
			t.Fatalf("consumeModelStream: partial=%v disagrees with StreamedAssistant=%v", partial, modelResp.StreamedAssistant)
		}
		// The finish event's usage and reason are preserved into the assembled
		// response (the last finish wins; no error means all events processed).
		if plan.sawUsage {
			got := modelResp.Response.Usage
			if got.InputTokens != plan.lastUsage.InputTokens ||
				got.OutputTokens != plan.lastUsage.OutputTokens ||
				got.TotalTokens != plan.lastUsage.TotalTokens {
				t.Fatalf("consumeModelStream: usage not preserved\n got  = %+v\n want = %+v", got, plan.lastUsage)
			}
		}
		if plan.sawFinishFR && modelResp.Response.Finish != plan.lastReason {
			t.Fatalf("consumeModelStream: finish reason not preserved: got %+v want %+v", modelResp.Response.Finish, plan.lastReason)
		}
	})
}

// --- callModelWithFallback fuzz -------------------------------------------

// msfzCallBehavior is the fuzz-derived per-attempt behavior of the streaming
// adapter driving callModelWithFallback.
type msfzCallBehavior struct {
	openErr  int // 0 none, 1 retryable, 2 permanent, 3 stream-unsupported
	events   []llm.StreamEvent
	complete int // Complete-path outcome: 0 ok, 1 retryable err, 2 permanent err
}

func decodeMsfzCallBehavior(data []byte) (msfzCallBehavior, *msfzReader) {
	r := &msfzReader{data: data}
	b := msfzCallBehavior{
		openErr:  r.intn(4),
		complete: r.intn(3),
	}
	// Reuse the stream decoder for the mid-stream events when the open succeeds.
	if b.openErr == 0 {
		b.events = decodeMsfzStreamPlan(data[msfzMin(len(data), r.pos):]).events
		if len(b.events) == 0 {
			reason := llm.FinishReason{Reason: llm.FinishReasonStop}
			b.events = []llm.StreamEvent{{Type: llm.StreamEventFinish, FinishReason: &reason, Usage: &llm.Usage{}}}
		}
	}
	return b, r
}

func msfzMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// msfzStreamAdapter is a fuzz-driven streaming llm.ProviderAdapter. It is
// constructed fresh per fuzz iteration (no shared mutable state across parallel
// fuzz executions). It never touches the network: Stream returns a scripted
// msfzStream or a synthetic error, and Complete returns a synthetic response or
// error.
type msfzStreamAdapter struct {
	provider string
	behavior msfzCallBehavior

	mu      sync.Mutex
	attempt int
}

func (a *msfzStreamAdapter) Name() string { return a.provider }

func (a *msfzStreamAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	switch a.behavior.complete {
	case 1:
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.provider, 503, "fuzzed retryable complete", nil, nil)
	case 2:
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.provider, 404, "fuzzed permanent complete", nil, nil)
	default:
		return llm.Response{
			Provider: a.provider,
			Model:    req.Model,
			Message:  llm.Assistant("done"),
			Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		}, nil
	}
}

func (a *msfzStreamAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	a.mu.Lock()
	a.attempt++
	a.mu.Unlock()
	switch a.behavior.openErr {
	case 1:
		return nil, llm.ErrorFromHTTPStatus(a.provider, 503, "fuzzed retryable open", nil, nil)
	case 2:
		return nil, llm.ErrorFromHTTPStatus(a.provider, 404, "fuzzed permanent open", nil, nil)
	case 3:
		return nil, llm.ErrStreamUnsupported
	default:
		return newMsfzStream(a.behavior.events), nil
	}
}

// FuzzMsfzCallModelWithFallback drives callModelWithFallback end to end against
// a fuzzed streaming adapter, with a bounded retry policy and a same-provider
// fallback configured. It asserts the call always terminates without panic and
// that a nil error implies a well-formed response and attempt metadata.
func FuzzMsfzCallModelWithFallback(f *testing.F) {
	f.Add([]byte{0, 0, 6})    // stream open ok, finish
	f.Add([]byte{1, 0})       // retryable open error
	f.Add([]byte{2, 0})       // permanent open error -> fallback chain
	f.Add([]byte{3, 0})       // stream unsupported -> Complete path (ok)
	f.Add([]byte{3, 2})       // stream unsupported -> Complete permanent error
	f.Add([]byte{0, 0, 7, 1}) // stream open ok, mid-stream error
	f.Add([]byte{2, 1})       // permanent open, retryable complete (fallback)
	f.Add([]byte{0, 0, 1, 1, 6})

	f.Fuzz(func(t *testing.T, data []byte) {
		behavior, _ := decodeMsfzCallBehavior(data)

		adapter := &msfzStreamAdapter{provider: "openai", behavior: behavior}
		client := llm.NewClient()
		client.Register(adapter)

		policy := llm.RetryPolicy{MaxRetries: 2, BaseDelay: 0}
		cfg := SessionConfig{
			MaxSubagentDepth: 1,
			ModelFallbacks:   []string{"openai/gpt-5.4-mini"},
			LLMRetryPolicy:   &policy,
			LLMSleep: func(ctx context.Context, _ time.Duration) error {
				return ctx.Err()
			},
		}
		sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		t.Cleanup(sess.Close)

		profile := sess.currentProfile()
		req := sess.buildModelRequest(profile, "system", []llm.Message{llm.User("hi")}, nil, "")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		modelResp, usedReq, attempt, callErr := sess.callModelWithFallback(ctx, profile, req, "", 1)

		if callErr == nil {
			if usedReq.Model == "" {
				t.Fatalf("callModelWithFallback: success used-request has empty Model")
			}
			if modelResp.Response.Provider == "" || modelResp.Response.Model == "" {
				t.Fatalf("callModelWithFallback: success response missing provider/model: %+v", modelResp.Response)
			}
			if attempt.RequestModel == "" || attempt.AttemptGroupID == "" {
				t.Fatalf("callModelWithFallback: success attempt metadata incomplete: %+v", attempt)
			}
		} else if usedReq.Model == "" {
			// A failure must still return the request actually used (non-empty
			// model) so downstream logging is coherent — never a lost turn.
			t.Fatalf("callModelWithFallback: error path returned empty used-request Model: %v", callErr)
		}
	})
}

// --- applyResponsesContinuationAnchorPlanning fuzz -------------------------

// msfzPlanConfig is the fuzz-derived configuration for the continuation-planning
// target: whether continuation is enabled, whether the plan permits storage,
// whether the shadow estimate is available, and the request's stream flag.
type msfzPlanConfig struct {
	auto            bool
	registryEnabled bool
	storageAllowed  bool
	shadowAvailable bool
	stream          bool
	withAnchor      bool
}

func decodeMsfzPlanConfig(data []byte) msfzPlanConfig {
	r := &msfzReader{data: data}
	return msfzPlanConfig{
		auto:            r.boolean(),
		registryEnabled: r.boolean(),
		storageAllowed:  r.boolean(),
		shadowAvailable: r.boolean(),
		stream:          r.boolean(),
		withAnchor:      r.boolean(),
	}
}

// FuzzMsfzContinuationAnchorPlanning drives applyResponsesContinuationAnchorPlanning
// across the enabled/disabled, storage-allowed/denied, shadow-available/missing,
// and anchor-present/absent branches. Oracle: never panics; the returned request
// always carries a non-empty HistoryMode; a responses-delta selection is
// internally consistent (previous-response id and continuation metadata both
// present); and the decision is deterministic across two runs.
func FuzzMsfzContinuationAnchorPlanning(f *testing.F) {
	f.Add([]byte{0})           // all-false: non-auto early return
	f.Add([]byte{0xFF})        // all-true: full delta path
	f.Add([]byte{0b0000_0001}) // auto only, no registry -> early return
	f.Add([]byte{0b0000_0011}) // auto + registry, storage denied
	f.Add([]byte{0b0001_0111}) // auto + registry + storage, no shadow
	f.Add([]byte{0b0011_1111}) // auto + registry + storage + shadow + stream
	f.Add([]byte{0b0010_1111}) // ... without anchor
	f.Add([]byte{0x2A, 0x00, 0x11, 0x7F})

	f.Fuzz(func(t *testing.T, data []byte) {
		cfg := decodeMsfzPlanConfig(data)
		sess := msfzNewPlanningSession(t, cfg)

		history := []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User("prior user marker")),
		}
		if cfg.withAnchor {
			history = append(history, phase9MatchingAnchor("resp_msfz_anchor"))
		}

		req := llm.Request{
			Provider: "openai",
			Model:    "gpt-5.4",
			Messages: []llm.Message{llm.System("system"), llm.User("current user marker")},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		out := sess.applyResponsesContinuationAnchorPlanning(ctx, req, history, cfg.stream)

		// Invariant: every return path leaves a concrete history mode.
		if out.HistoryMode == "" {
			t.Fatalf("applyResponsesContinuationAnchorPlanning: returned empty HistoryMode: %+v", out)
		}
		// Invariant: a delta selection is internally consistent.
		if out.HistoryMode == llm.HistoryModeResponsesDelta {
			if out.PreviousResponseID == "" {
				t.Fatalf("delta selection has empty PreviousResponseID: %+v", out)
			}
			if out.Continuation == nil {
				t.Fatalf("delta selection has nil Continuation metadata: %+v", out)
			}
		}

		// Determinism: the same session state + request yields the same decision.
		again := sess.applyResponsesContinuationAnchorPlanning(ctx, req, history, cfg.stream)
		if again.HistoryMode != out.HistoryMode || again.PreviousResponseID != out.PreviousResponseID {
			t.Fatalf("planning not deterministic:\n first  = mode=%q prev=%q\n second = mode=%q prev=%q",
				out.HistoryMode, out.PreviousResponseID, again.HistoryMode, again.PreviousResponseID)
		}
	})
}

func msfzNewPlanningSession(t *testing.T, cfg msfzPlanConfig) *Session {
	t.Helper()
	dir := t.TempDir()

	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			plan := phase4DIContinuationPlan(req)
			plan.ContinuationStorageAllowed = cfg.storageAllowed
			return plan, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sc := SessionConfig{
		StateDir:         dir,
		MaxSubagentDepth: 1,
	}
	if cfg.auto {
		sc.OpenAIResponsesContinuation = "auto"
	}
	if cfg.registryEnabled {
		sc.testOnly.responsesContinuationSupportRegistry = map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
			llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
		}
	}
	if !cfg.shadowAvailable {
		sc.testOnly.responsesContinuationShadowEstimateFunc = func(llm.Request) (int, bool) {
			return 0, false
		}
	}

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), sc)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}
