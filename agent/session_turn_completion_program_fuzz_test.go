//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This program drives two adjacent session boundaries that ordinary tool and
// stream fuzzers intentionally do not own:
//
//   - a complete turn through non-streaming completion, continuation recovery,
//     same-provider model fallback, and terminal model errors; and
//   - the human-only sandbox escalation waiter lifecycle, including approval,
//     denial, cancellation, close, and stable pending-card ordering.
//
// Every model call is answered by tfpAdapter and every execution-environment
// operation is handled by agenttest.DenyEnv. The only filesystem writes are
// transcript and state files below t.TempDir.

// -----------------------------------------------------------------------------
// Turn completion, continuation recovery, and model fallbacks.

type tfpScenario uint8

const (
	tfpDirect tfpScenario = iota
	tfpModelFallback
	tfpFallbackExhausted
	tfpContinuationRecovery
	tfpContinuationThenModelFallback
	tfpRetryableFailure
	tfpContextLengthFailure
	tfpContentFilterRecovery
)

type tfpProgram struct {
	scenario           tfpScenario
	systemPromptAsUser bool
	effort             string
	text               string
	seed               uint64
}

type tfpReader struct {
	data []byte
	pos  int
}

func (r *tfpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *tfpReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *tfpReader) bool() bool { return r.next()&1 != 0 }

func decodeTFPProgram(data []byte) tfpProgram {
	r := &tfpReader{data: data}
	p := tfpProgram{
		scenario:           tfpScenario(r.intn(int(tfpContentFilterRecovery) + 1)),
		systemPromptAsUser: r.bool(),
		effort:             []string{"", "low", "high", "xhigh"}[r.intn(4)],
		text:               []string{"done", "completed", "reviewed", "finished"}[r.intn(4)],
	}
	for shift := uint(0); shift < 64; shift += 8 {
		p.seed |= uint64(r.next()) << shift
	}
	// A responses-delta request requires a system message, so keep that branch
	// eligible while separately exercising SystemPromptAsUser in the other paths.
	if p.usesContinuation() {
		p.systemPromptAsUser = false
	}
	return p
}

func (p tfpProgram) usesContinuation() bool {
	return p.scenario == tfpContinuationRecovery || p.scenario == tfpContinuationThenModelFallback
}

func (p tfpProgram) expectsSuccess() bool {
	switch p.scenario {
	case tfpDirect, tfpModelFallback, tfpContinuationRecovery, tfpContinuationThenModelFallback, tfpContentFilterRecovery:
		return true
	default:
		return false
	}
}

// tfpAdapter implements the provider boundary with a bounded local state
// machine. It deliberately advertises streaming as unsupported, exercising the
// Session's stream-to-complete fallback without any transport or network.
type tfpAdapter struct {
	program tfpProgram

	mu       sync.Mutex
	requests []llm.Request
}

func (a *tfpAdapter) Name() string { return "openai" }

func (a *tfpAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *tfpAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	call := len(a.requests)
	a.mu.Unlock()

	resp, err := a.outcome(req, call)
	if err != nil {
		return llm.Response{}, err
	}
	resp.Provider = "openai"
	if resp.Model == "" {
		resp.Model = req.Model
	}
	resp.ID = fmt.Sprintf("resp_tfp_%d", call)
	resp.Raw = map[string]any{
		"endpoint_url": "https://offline.invalid/v1/responses",
		"id_hash":      fmt.Sprintf("tfp-id-hash-%d", call),
	}
	return resp, nil
}

func (a *tfpAdapter) PlanResponsesContinuation(llm.Request) (llm.ResponsesContinuationPlan, error) {
	return llm.ResponsesContinuationPlan{
		EndpointFamily:             llm.ResponsesEndpointFamilyOpenAIPublic,
		RequestFingerprint:         "tfp-request-fingerprint",
		StorageScopeFingerprint:    "tfp-storage-scope",
		StoragePolicyLabel:         llm.ResponsesStoragePolicyPublicOpenAIStore,
		ContinuationStorageAllowed: true,
		CanFallbackToChat:          true,
	}, nil
}

func (a *tfpAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

func (a *tfpAdapter) outcome(req llm.Request, call int) (llm.Response, error) {
	final := func() llm.Response { return agenttest.FinalResponse(a.program.text) }
	switch a.program.scenario {
	case tfpDirect:
		return final(), nil
	case tfpModelFallback:
		if req.Model == "primary" {
			return llm.Response{}, tfpPermanentError("primary rejected the request")
		}
		return final(), nil
	case tfpFallbackExhausted:
		return llm.Response{}, tfpPermanentError("all models rejected the request")
	case tfpContinuationRecovery:
		if req.HistoryMode == llm.HistoryModeResponsesDelta {
			return llm.Response{}, tfpContinuationError()
		}
		return final(), nil
	case tfpContinuationThenModelFallback:
		if req.HistoryMode == llm.HistoryModeResponsesDelta {
			return llm.Response{}, tfpContinuationError()
		}
		if req.Model == "primary" {
			return llm.Response{}, tfpPermanentError("full-history primary rejected the request")
		}
		return final(), nil
	case tfpRetryableFailure:
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "offline transient failure", nil, nil)
	case tfpContextLengthFailure:
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 413, "offline context length exceeded", nil, nil)
	case tfpContentFilterRecovery:
		if call == 1 {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "offline content filter", nil, nil)
		}
		return final(), nil
	default:
		return final(), nil
	}
}

func tfpPermanentError(message string) error {
	return llm.ErrorFromHTTPStatus("openai", 403, message, nil, nil)
}

func tfpContinuationError() error {
	return llm.ErrorFromHTTPStatus("openai", 404, "previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "previous response not found",
		},
	}, nil)
}

func tfpNewSession(t *testing.T, program tfpProgram, adapter *tfpAdapter) *Session {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	client := llm.NewClient()
	client.Register(adapter)
	policy := llm.RetryPolicy{MaxRetries: 0}
	cfg := SessionConfig{
		StateDir:           stateDir,
		MaxSubagentDepth:   1,
		ModelFallbacks:     []string{"fallback-a", "fallback-b"},
		LLMRetryPolicy:     &policy,
		ReasoningEffort:    program.effort,
		SystemPromptAsUser: program.systemPromptAsUser,
		NoProjectPrompts:   true,
		clock:              agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}
	if program.usesContinuation() {
		cfg.OpenAIResponsesContinuation = "auto"
		cfg.testOnly.responsesContinuationSupportRegistry = map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
			llm.ResponsesEndpointFamilyOpenAIPublic: {
				EndpointFamily:       llm.ResponsesEndpointFamilyOpenAIPublic,
				StorageShapeProven:   true,
				ProductionPathProven: true,
				Enabled:              true,
				MaxAnchorAgeSeconds:  3600,
			},
		}
	}
	sess, err := NewSession(client, NewOpenAIProfile("primary"), &agenttest.DenyEnv{WorkDir: workspace, Seed: program.seed}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	tfpDrainSessionEvents(sess)
	if program.usesContinuation() {
		tfpSeedContinuationHistory(sess)
	}
	return sess
}

func tfpDrainSessionEvents(sess *Session) {
	go func() {
		for range sess.Events() {
		}
	}()
}

func tfpSeedContinuationHistory(sess *Session) {
	stamp := sess.sclock().Now()
	anchor := schema.Turn{
		Kind:                            schema.TurnAssistant,
		Message:                         llm.Assistant("prior assistant"),
		Timestamp:                       stamp,
		ResponseID:                      "resp_tfp_anchor",
		ResponseIDHash:                  "tfp-anchor-hash",
		ResponseEndpoint:                "https://offline.invalid/v1/responses",
		ResponseStorageScopeFingerprint: "tfp-storage-scope",
		ResponseRequestFingerprint:      "tfp-request-fingerprint",
		ResponseContextMarker:           responseContextMarkerV1,
	}
	sess.mu.Lock()
	sess.history = []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("tfp prior user input"), Timestamp: stamp},
		anchor,
	}
	sess.mu.Unlock()
}

// FuzzTurnFallbackLifecycleProgram covers a full accepted user turn across the
// response-continuation recovery and fallback paths. Its principal oracle is
// durable coherence: every adapter request has a matching API-call transcript
// record, requests remain provider/model complete, and each scenario reaches
// the documented terminal state without a live provider or subprocess.
func FuzzTurnFallbackLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{byte(tfpDirect), 0, 0, 0, 1},
		{byte(tfpModelFallback), 1, 1, 1, 2},
		{byte(tfpFallbackExhausted), 0, 2, 2, 3},
		{byte(tfpContinuationRecovery), 0, 3, 3, 4},
		{byte(tfpContinuationThenModelFallback), 0, 0, 1, 5},
		{byte(tfpRetryableFailure), 1, 1, 2, 6},
		{byte(tfpContextLengthFailure), 0, 2, 3, 7},
		{byte(tfpContentFilterRecovery), 1, 3, 0, 8},
		{},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := decodeTFPProgram(data)
		adapter := &tfpAdapter{program: program}
		sess := tfpNewSession(t, program, adapter)

		out, err := sess.ProcessInput(context.Background(), "tfp current user input", nil)
		requests := adapter.Requests()
		if len(requests) == 0 {
			t.Fatal("turn made no scripted provider completion request")
		}
		for i, req := range requests {
			if req.Provider != "openai" || strings.TrimSpace(req.Model) == "" {
				t.Fatalf("request %d lost provider/model identity: %+v", i, req)
			}
			if len(req.Messages) == 0 {
				t.Fatalf("request %d has no messages", i)
			}
		}

		if program.expectsSuccess() {
			if err != nil {
				t.Fatalf("successful scenario %d returned error: %v", program.scenario, err)
			}
			if !strings.Contains(out, program.text) {
				t.Fatalf("successful scenario %d output %q does not contain %q", program.scenario, out, program.text)
			}
			if got := sess.State(); got != SessionAwaiting {
				t.Fatalf("successful scenario %d ended in state %q, want awaiting", program.scenario, got)
			}
		} else {
			if err == nil {
				t.Fatalf("failure scenario %d returned nil error", program.scenario)
			}
			wantState := SessionIdle
			if program.scenario == tfpFallbackExhausted || program.scenario == tfpContextLengthFailure {
				wantState = SessionClosed
			}
			if got := sess.State(); got != wantState {
				t.Fatalf("failure scenario %d ended in state %q, want %q (err=%v)", program.scenario, got, wantState, err)
			}
		}

		tfpAssertScenarioRequests(t, program, requests)
	})
}

func tfpAssertScenarioRequests(t *testing.T, program tfpProgram, requests []llm.Request) {
	t.Helper()
	switch program.scenario {
	case tfpModelFallback:
		if len(requests) < 2 || requests[0].Model != "primary" || requests[len(requests)-1].Model == "primary" {
			t.Fatalf("model fallback requests do not leave primary: %+v", requests)
		}
	case tfpFallbackExhausted:
		if len(requests) < 3 || requests[0].Model != "primary" {
			t.Fatalf("exhausted fallback chain did not attempt primary and both fallbacks: %+v", requests)
		}
	case tfpContinuationRecovery, tfpContinuationThenModelFallback:
		if len(requests) < 2 {
			t.Fatalf("continuation scenario made %d requests, want delta plus full-history recovery", len(requests))
		}
		if requests[0].HistoryMode != llm.HistoryModeResponsesDelta || requests[0].PreviousResponseID == "" {
			t.Fatalf("first continuation request is not a delta request: %+v", requests[0])
		}
		if requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback || requests[1].PreviousResponseID != "" || requests[1].Continuation != nil {
			t.Fatalf("continuation recovery request is malformed: %+v", requests[1])
		}
		if program.scenario == tfpContinuationThenModelFallback {
			if len(requests) < 3 || requests[len(requests)-1].Model == "primary" || requests[len(requests)-1].HistoryMode != llm.HistoryModeFullHistory {
				t.Fatalf("continuation fallback request did not use full history on fallback model: %+v", requests)
			}
		}
	case tfpContentFilterRecovery:
		if len(requests) < 2 {
			t.Fatalf("content-filter recovery made %d requests, want retry", len(requests))
		}
	}
}

// -----------------------------------------------------------------------------
// Sandbox escalation lifecycle.

type sefProgram struct {
	callName          string
	reason            sandbox.DenialReason
	nonSandbox        bool
	nonInteractive    bool
	subagent          bool
	noSubscriberProbe bool
	subscribers       int
	count             int
	action            int // approve, deny, cancel, close
	closeBefore       bool
}

type sefReader struct {
	data []byte
	pos  int
}

func (r *sefReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *sefReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *sefReader) bool() bool { return r.next()&1 != 0 }

var sefCallNames = []string{"read_file", "write_file", "edit_file", "apply_patch", "shell", "glob"}

var sefReasons = []sandbox.DenialReason{
	sandbox.DenialOutsideReadRoots,
	sandbox.DenialOutsideWriteRoots,
	sandbox.DenialWritesDisabled,
	sandbox.DenialGitProtected,
	sandbox.DenialSymlink,
	sandbox.DenialMasked,
	sandbox.DenialUnspecified,
}

func decodeSEFProgram(data []byte) sefProgram {
	r := &sefReader{data: data}
	return sefProgram{
		callName:          sefCallNames[r.intn(len(sefCallNames))],
		reason:            sefReasons[r.intn(len(sefReasons))],
		nonSandbox:        r.bool(),
		nonInteractive:    r.bool(),
		subagent:          r.bool(),
		noSubscriberProbe: r.bool(),
		subscribers:       r.intn(2),
		count:             r.intn(3) + 1,
		action:            r.intn(4),
		closeBefore:       r.bool(),
	}
}

func (p sefProgram) mayEscalate() bool {
	return !p.nonSandbox && !p.nonInteractive && !p.subagent && !p.noSubscriberProbe && p.subscribers > 0 && escalatableTools[p.callName] && p.reason.Curable()
}

func sefNewSession(t *testing.T) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&tfpAdapter{program: tfpProgram{scenario: tfpDirect, text: "unused"}})
	sess, err := NewSession(client, NewOpenAIProfile("primary"), &agenttest.DenyEnv{WorkDir: t.TempDir()}, SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	for {
		select {
		case <-sess.Events():
		default:
			return sess
		}
	}
}

type sefRun struct {
	path string
	res  tool.ExecResult
	done chan tool.ExecResult
}

// FuzzSandboxEscalationLifecycleProgram drives the human-only escalation
// waiter without a shell, sandbox wrapper, or live UI. The event channel is the
// deterministic rendezvous: it proves registration happened before a resolver,
// cancellation, or Close is allowed to act on the waiter.
func FuzzSandboxEscalationLifecycleProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 0, 0, 0, 0, 1, 0, 0, 0}, // approved read-file containment denial
		{1, 1, 0, 0, 0, 0, 1, 1, 1, 0}, // denied write-file containment denial
		{2, 2, 0, 0, 0, 0, 1, 2, 2, 0}, // cancelled edit-file containment denial
		{0, 1, 0, 0, 0, 0, 1, 2, 3, 0}, // close with multiple waiters
		{3, 0, 0, 0, 0, 0, 1, 0, 0, 0}, // non-allowlisted tool stays final
		{0, 3, 0, 0, 0, 0, 1, 0, 0, 0}, // uncurable denial stays final
		{0, 0, 1, 0, 0, 0, 1, 0, 0, 0}, // non-sandbox error stays final
		{0, 0, 0, 1, 0, 0, 1, 0, 0, 0}, // non-interactive stays final
		{0, 0, 0, 0, 0, 1, 1, 0, 0, 0}, // nil subscriber probe stays final
		{0, 0, 0, 0, 0, 0, 1, 0, 0, 1}, // close before waiter registration
		{},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := decodeSEFProgram(data)
		sess := sefNewSession(t)
		sess.cfg.NonInteractive = program.nonInteractive
		sess.restoredMetaIsSubagent = program.subagent
		if !program.noSubscriberProbe {
			sess.SetSubscriberCountFunc(func() int { return program.subscribers })
		}
		if program.closeBefore {
			res := sefDeniedResult(program, "/tfp/closed")
			sess.Close()
			got := sess.escalateOnSandboxDenial(context.Background(), program.callName, res, func(context.Context) tool.ExecResult {
				t.Fatal("closed session unexpectedly reran an escalation")
				return tool.ExecResult{}
			})
			if !errors.Is(got.Err, res.Err) || !got.IsError || sess.HasPendingEscalations() {
				t.Fatalf("closed session changed escalation result: %+v", got)
			}
			return
		}

		if !program.mayEscalate() {
			res := sefDeniedResult(program, "/tfp/final")
			rerun := false
			got := sess.escalateOnSandboxDenial(context.Background(), program.callName, res, func(context.Context) tool.ExecResult {
				rerun = true
				return tool.ExecResult{}
			})
			if rerun {
				t.Fatalf("non-escalating program unexpectedly reran %q", program.callName)
			}
			if !errors.Is(got.Err, res.Err) || !got.IsError {
				t.Fatalf("non-escalating result changed: got=%+v want error=%v", got, res.Err)
			}
			if sess.HasPendingEscalations() || len(sess.PendingEscalations()) != 0 {
				t.Fatal("non-escalating result registered a pending escalation")
			}
			return
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runs := make([]sefRun, 0, program.count)
		requested := make([]events.SandboxEscalationRequestedData, 0, program.count)
		for i := 0; i < program.count; i++ {
			path := fmt.Sprintf("/tfp/outside/%d", i)
			run := sefRun{path: path, res: sefDeniedResult(program, path), done: make(chan tool.ExecResult, 1)}
			go func(run sefRun) {
				run.done <- sess.escalateOnSandboxDenial(ctx, program.callName, run.res, func(rerunCtx context.Context) tool.ExecResult {
					grant, ok := invocationGrant(rerunCtx)
					if !ok {
						return tool.ExecResult{ToolName: program.callName, IsError: true, Err: errors.New("missing invocation grant")}
					}
					return tool.ExecResult{ToolName: program.callName, Output: grant, FullOutput: grant}
				})
			}(run)
			runs = append(runs, run)
			requested = append(requested, sefAwaitRequestedEvent(t, sess))
		}

		pending := sess.PendingEscalations()
		if len(pending) != len(runs) {
			t.Fatalf("pending snapshot has %d entries, want %d", len(pending), len(runs))
		}
		for i := range pending {
			if pending[i].EscalationID != requested[i].EscalationID || pending[i].DeniedPath != runs[i].path {
				t.Fatalf("pending snapshot is not stable raise order at %d: got=%+v want id=%q path=%q", i, pending[i], requested[i].EscalationID, runs[i].path)
			}
		}

		switch program.action {
		case 0: // approve, deliberately resolve reverse order
			for i := len(requested) - 1; i >= 0; i-- {
				if err := sess.ResolveSandboxEscalation(requested[i].EscalationID, true); err != nil {
					t.Fatalf("approve %q: %v", requested[i].EscalationID, err)
				}
			}
		case 1: // deny
			for _, req := range requested {
				if err := sess.ResolveSandboxEscalation(req.EscalationID, false); err != nil {
					t.Fatalf("deny %q: %v", req.EscalationID, err)
				}
			}
		case 2: // turn interruption
			cancel()
		case 3: // session teardown
			sess.Close()
		}

		for _, run := range runs {
			got := <-run.done
			if program.action == 0 {
				if got.IsError || got.FullOutput != run.path {
					t.Fatalf("approved escalation result = %+v, want invocation grant %q", got, run.path)
				}
			} else if !errors.Is(got.Err, run.res.Err) || !got.IsError {
				t.Fatalf("non-approved escalation changed typed denial: got=%+v want=%v", got, run.res.Err)
			}
		}
		if sess.HasPendingEscalations() || len(sess.PendingEscalations()) != 0 {
			t.Fatal("resolved, cancelled, or closed escalations remained pending")
		}
		if err := sess.ResolveSandboxEscalation(requested[0].EscalationID, true); err == nil {
			t.Fatal("resolving an already-settled escalation unexpectedly succeeded")
		}
	})
}

func sefDeniedResult(program sefProgram, path string) tool.ExecResult {
	var err error
	if program.nonSandbox {
		err = errors.New("offline non-sandbox failure")
	} else {
		err = &sandbox.DeniedError{
			Mode:       sandbox.ModeReadOnly,
			Tool:       program.callName,
			Path:       path,
			Reason:     "offline sandbox denial",
			ReasonKind: program.reason,
		}
	}
	return tool.ExecResult{
		ToolName:   program.callName,
		CallID:     "tfp_call",
		Output:     err.Error(),
		FullOutput: err.Error(),
		IsError:    true,
		Err:        err,
	}
}

func sefAwaitRequestedEvent(t *testing.T, sess *Session) events.SandboxEscalationRequestedData {
	t.Helper()
	for event := range sess.Events() {
		if event.Kind != events.EventSandboxEscalationRequested {
			continue
		}
		data, ok := event.Data.(events.SandboxEscalationRequestedData)
		if !ok {
			t.Fatalf("escalation event data type = %T", event.Data)
		}
		if data.EscalationID == "" || data.DeniedPath == "" {
			t.Fatalf("escalation event omitted id/path: %+v", data)
		}
		return data
	}
	t.Fatal("session event stream closed before escalation request")
	return events.SandboxEscalationRequestedData{}
}

// -----------------------------------------------------------------------------
// Loop-detection completion decisions.

// FuzzSessionLoopRecoveryProgram keeps the loop-warning portion of the turn
// boundary deterministic and independent of providers. It verifies repeated
// signature detection, short-window rejection, question classification, and the
// only mutable recovery effect: the first stuck warning raises effort but later
// warnings do not silently reset it.
func FuzzSessionLoopRecoveryProgram(f *testing.F) {
	for _, seed := range [][]byte{{0, 0, 0, 0}, {1, 1, 1, 1}, {2, 2, 2, 2}, {3, 0, 3, 3}, {3, 3, 3, 3}, {4, 0, 4, 4}, {4, 4, 4, 4}, {255, 255, 255, 255}, {}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r := &tfpReader{data: data}
		efforts := []string{"", "low", "medium", "high", "xhigh", "max"}
		before := efforts[r.intn(len(efforts))]
		sess := &Session{cfg: SessionConfig{ReasoningEffort: before}}
		count := r.intn(5) + 1
		message := sess.stuckEscalation(count)
		if strings.TrimSpace(message) == "" {
			t.Fatal("stuck escalation returned an empty recovery message")
		}
		if count == 1 {
			want := before
			switch before {
			case "", "low", "medium":
				want = "high"
			case "high", "xhigh":
				want = "max"
			}
			if got := sess.cfg.ReasoningEffort; got != want {
				t.Fatalf("first stuck escalation effort = %q, want %q", got, want)
			}
		} else if got := sess.cfg.ReasoningEffort; got != before {
			t.Fatalf("later stuck escalation changed effort from %q to %q", before, got)
		}

		text := []string{"", "status", "need input?", "next:", "spaces ?  "}[r.intn(5)]
		wantQuestion := strings.HasSuffix(strings.TrimSpace(text), "?") || strings.HasSuffix(strings.TrimSpace(text), ":")
		if got := looksLikeQuestion(text); got != wantQuestion {
			t.Fatalf("looksLikeQuestion(%q) = %v, want %v", text, got, wantQuestion)
		}

		patLen := r.intn(3) + 1
		repeats := r.intn(4) + 1
		pattern := make([]string, patLen)
		for i := range pattern {
			pattern[i] = fmt.Sprintf("tool-%d", r.intn(4))
		}
		signatures := make([]string, 0, patLen*repeats)
		for i := 0; i < patLen*repeats; i++ {
			signatures = append(signatures, pattern[i%patLen])
		}
		if !detectLoop(signatures, len(signatures)) {
			t.Fatalf("repeating signature pattern %v was not detected", signatures)
		}
		if detectLoop(signatures[:len(signatures)-1], len(signatures)) {
			t.Fatalf("short signature window unexpectedly detected a loop: %v", signatures)
		}
		first := detectLoop(signatures, len(signatures))
		second := detectLoop(signatures, len(signatures))
		if first != second {
			t.Fatalf("loop detection was not deterministic for %v", signatures)
		}
		if detectLoop([]string{"read", "write", "read", "shell"}, 4) {
			t.Fatal("non-repeating signatures unexpectedly detected as a loop")
		}
	})
}
