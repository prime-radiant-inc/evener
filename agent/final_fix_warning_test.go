package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func captureSessionEvents(sess *Session) <-chan []events.SessionEvent {
	result := make(chan []events.SessionEvent, 1)
	go func() {
		var captured []events.SessionEvent
		for event := range sess.Events() {
			captured = append(captured, event)
		}
		result <- captured
	}()
	return result
}

func warningEvents(capturedEvents []events.SessionEvent) []events.WarningData {
	var warnings []events.WarningData
	for _, event := range capturedEvents {
		if event.Kind != events.EventWarning {
			continue
		}
		warning, ok := event.Data.(events.WarningData)
		if ok {
			warnings = append(warnings, warning)
		}
	}
	return warnings
}

// standingRequestTokens measures one input's standing request -- the system
// prompt, the tool schemas, and that input -- by running it through a throwaway
// session whose context window is wide enough that no warning or compaction
// interferes, then estimating the captured request's input tokens the way
// maybeWarnContextUsage does. Warning tests size their window or their history
// from this measurement, so a prompt rewrite cannot move the request across a
// threshold a hardcoded constant pinned.
func standingRequestTokens(t *testing.T, dir string, profile *provider.Profile, input string) int {
	t.Helper()
	adapter := &fakeAdapter{
		name: profile.ID(),
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, WithContextWindow(profile, 8_000_000), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("probe NewSession: %v", err)
	}
	muteNoteElicitation(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, input, nil); err != nil {
		t.Fatalf("probe ProcessInput: %v", err)
	}
	sess.Close()
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("probe captured no model request")
	}
	return llm.EstimateInputTokensForResolved(profile.Resolved(), reqs[0]).Tokens
}

func assertWarningPrecedesCompaction(t *testing.T, captured []events.SessionEvent) {
	t.Helper()
	warningIndex, compactionIndex := -1, -1
	for i, event := range captured {
		if event.Kind == events.EventWarning && warningIndex < 0 {
			warningIndex = i
		}
		if event.Kind == events.EventContextCompaction && compactionIndex < 0 {
			compactionIndex = i
		}
	}
	if warningIndex < 0 || compactionIndex < 0 || warningIndex >= compactionIndex {
		t.Fatalf("warning/compaction order = %d/%d, want warning before compaction", warningIndex, compactionIndex)
	}
}

func TestSessionTokenBudgetOutputReductionEmitsOneWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "budget-warning", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ok") },
	}})
	profile := testOpenAICompatProfile("budget-warning", "warning-model", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.history = []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("task"))}
	sess.contextMgr.RecordInputTokens(500_000, len(sess.history))
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if req.MaxTokens == nil || *req.MaxTokens >= 131_072 {
		t.Fatalf("prepared MaxTokens = %v, want a reduced positive allocation", req.MaxTokens)
	}
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	t.Logf("warning events (%d): %+v", len(warnings), warnings)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	message := warnings[0].Message
	for _, want := range []string{"Output allocation reduced", "budget-warning", "warning-model", "requested=131072", "admitted="} {
		if !strings.Contains(message, want) {
			t.Fatalf("warning message %q missing %q", message, want)
		}
	}
	// A routine output clamp is budget arithmetic, not a failure: the title and
	// hint must not classify it as an error the way an unexpected failure would
	// (diagnostic.FromError's evenerFailure), or the Web UI renders it as an
	// "Evener error" chip telling the reader to check the session log.
	if warnings[0].Title == "Evener error" || strings.Contains(strings.ToLower(warnings[0].Hint), "session log") {
		t.Fatalf("output-reduction warning classified as an error: title=%q hint=%q", warnings[0].Title, warnings[0].Hint)
	}
	// The quiet-notice contract: this warning is informational budget
	// arithmetic, and clients demote and verbosity-gate it by its stable code,
	// never by matching its prose (the projector shows a coded warning only
	// at Full; the Web UI renders it as a quiet one-liner there).
	if warnings[0].Code != events.WarningCodeContextBudget {
		t.Fatalf("output-reduction warning code = %q, want %q", warnings[0].Code, events.WarningCodeContextBudget)
	}
}

// A clamp that holds round after round is one fact, not one per round: a
// long conversation clamps every round, and a notice each time floods the
// transcript. Once input fills the window the admitted allocation is what the
// window leaves, so it shrinks every round; the clamp is still the same one.
// The session warns when a clamp first applies, again when the model or the
// requested allocation changes, and again when a clamp returns after an
// unclamped round.
func TestSessionOutputReductionWarnsOnlyWhenTheClampChanges(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	profile := testOpenAICompatProfile("budget-warning", "warning-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	clamp := func(requested, admitted int) llm.TokenBudget {
		return llm.TokenBudget{LimitedOutput: true, RequestedOutput: requested, AdmittedOutput: admitted}
	}
	for admitted := 20_000; admitted > 10_000; admitted -= 1_000 {
		sess.warnOutputReduction(profile, clamp(131_072, admitted))
	}
	sess.warnOutputReduction(profile, clamp(65_536, 9_000))
	sess.warnOutputReduction(profile, clamp(65_536, 8_000))
	sess.warnOutputReduction(profile, llm.TokenBudget{})
	sess.warnOutputReduction(profile, clamp(65_536, 7_000))
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	var allocations []string
	for _, warning := range warnings {
		_, allocation, found := strings.Cut(warning.Message, "requested=")
		if !found {
			t.Fatalf("output-reduction warning %q names no requested allocation", warning.Message)
		}
		allocations = append(allocations, "requested="+allocation)
	}
	want := []string{"requested=131072 admitted=20000", "requested=65536 admitted=9000", "requested=65536 admitted=7000"}
	if !slices.Equal(allocations, want) {
		t.Fatalf("warned %v, want %v: once per clamp, again when its request changes or it returns", allocations, want)
	}
}

func TestSessionContinuationOutputReductionsEmitOneFinalWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	})
	profile := NewOpenAIProfile("gpt-5.4")
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = withTestSessionNamer(client, profile.WithResolved(resolved))
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			responsesContinuationShadowEstimateFunc: func(llm.Request) (int, bool) {
				return 500_000, true
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("prior user marker")),
		phase9MatchingAnchor("resp_warning_shadow"),
		schema.NewTurn(schema.TurnUserInput, llm.User("current user marker")),
	)
	sess.contextMgr.RecordInputTokens(400_000, len(sess.history))
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(context.Background(), 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if req.MaxTokens == nil || *req.MaxTokens >= 100_000 {
		t.Fatalf("continuation MaxTokens = %v, want the shadow to force a second reduction", req.MaxTokens)
	}
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want one final reduction warning: %+v", len(warnings), warnings)
	}
	message := warnings[0].Message
	for _, want := range []string{"requested=131072", "admitted="} {
		if !strings.Contains(message, want) {
			t.Fatalf("warning message %q missing %q", message, want)
		}
	}
}

func TestSessionFallbackTokenBudgetReductionEmitsOneWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeErrAdapter{name: "warning-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("warning-primary", 403, "primary rejected", nil, nil)
		},
	}})
	fallbackAdapter := &fakeAdapter{name: "warning-fallback", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if req.MaxTokens == nil || *req.MaxTokens != 976 {
				return finalResponse("bad allocation")
			}
			return finalResponse("fallback")
		},
	}}
	client.Register(fallbackAdapter)
	primary := testOpenAICompatProfile("warning-primary", "primary-model", 0)
	fallback := testOpenAICompatProfile("warning-fallback", "fallback-model", 0)
	fallbackResolved := fallback.Resolved()
	fallbackResolved.Caps.ContextWindow = new(5_000)
	fallbackResolved.Caps.MaxOutputTokens = new(1_000)
	fallback = fallback.WithResolved(fallbackResolved)
	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"warning-fallback/fallback-model"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }
	// Model fallback intentionally clears the primary allocation, making the
	// fallback's 1,000-token cap the requested output. This history estimates to
	// 3,000 tokens; with the 1,024-token safety reserve, only 976 fit.
	history := []llm.Message{llm.User(strings.Repeat("x", 12_000))}
	if got := llm.EstimateInputTokens(llm.Request{Messages: history}).Tokens; got != 3_000 {
		t.Fatalf("history token estimate = %d, want fixture-pinned 3000", got)
	}
	req := llm.Request{Provider: primary.ID(), Model: primary.Model(), Messages: []llm.Message{llm.User("task")}, MaxTokens: new(1_000)}
	if _, _, _, _, err := sess.callModelWithFallback(context.Background(), primary, req, history, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	t.Logf("warning events (%d): %+v", len(warnings), warnings)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	message := warnings[0].Message
	for _, want := range []string{"Output allocation reduced", "warning-fallback", "fallback-model", "requested=1000", "admitted=976"} {
		if !strings.Contains(message, want) {
			t.Fatalf("warning message %q missing %q", message, want)
		}
	}
}

func TestSessionLocalAdmissionCompactionEmitsOneWarningBeforeCompaction(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	profile := testOpenAICompatProfile("local-warning", "local-model", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(100)
	resolved.Caps.MaxOutputTokens = new(8)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite unrecoverable local budget")
	}
	sess.Close()
	captured := <-eventsDone
	warnings := warningEvents(captured)
	t.Logf("warning events (%d): %+v", len(warnings), warnings)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Message, "context_window") {
		t.Fatalf("local warning = %q, want local context_window reason", warnings[0].Message)
	}
	assertWarningPrecedesCompaction(t, captured)
}

func TestSessionLocalAdmissionWithoutContextManagerEmitsNoRecoveryWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	profile := testOpenAICompatProfile("local-no-manager", "local-model", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(100)
	resolved.Caps.MaxOutputTokens = new(8)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.contextMgr = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite unrecoverable local budget")
	}
	sess.Close()
	captured := <-eventsDone
	if warnings := warningEvents(captured); len(warnings) != 0 {
		t.Fatalf("warnings = %d, want none when no recovery can run: %+v", len(warnings), warnings)
	}
	for _, event := range captured {
		if event.Kind == events.EventContextCompaction {
			t.Fatal("local admission without a context manager emitted a compaction event")
		}
	}
}

func TestSessionPostDispatchLocalBudgetWithoutContextManagerDoesNotRetry(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "local-post-dispatch", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, &llm.ContextBudgetError{Provider: "local-post-dispatch", Model: "local-model", Limit: "context_window", InputTokens: 101, Maximum: 100}
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, &llm.ContextBudgetError{Provider: "local-post-dispatch", Model: "local-model", Limit: "context_window", InputTokens: 101, Maximum: 100}
		},
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("local-post-dispatch", "local-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.contextMgr = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite post-dispatch local budget error")
	}
	sess.Close()
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests = %d, want one without a no-op recovery retry", got)
	}
	if warnings := warningEvents(<-eventsDone); len(warnings) != 0 {
		t.Fatalf("warnings = %d, want none when no local recovery can run: %+v", len(warnings), warnings)
	}
}

func TestSessionProviderContextWithoutContextManagerDoesNotRetry(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "provider-no-manager", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("provider-no-manager", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("provider-no-manager", 413, "context length exceeded", nil, nil)
		},
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("provider-no-manager", "provider-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.contextMgr = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite provider context error")
	}
	sess.Close()
	captured := <-eventsDone
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider requests = %d, want one without a no-op recovery retry", got)
	}
	for _, event := range captured {
		if event.Kind == events.EventContextCompaction {
			t.Fatal("provider context error without a context manager emitted a compaction event")
		}
	}
}

func TestSessionProviderContextRecoveryEmitsOneWarningBeforeCompaction(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	// The provider's body echoes the user's request, as real ones can.
	const body = "context length exceeded: messages[1].content began \"user-private-prompt\""
	adapter := &fakeErrAdapter{name: "provider-warning", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("provider-warning", 413, body, nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("provider-warning", 413, body, nil, nil)
		},
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("provider-warning", "provider-model", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(8_192)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded after second provider context error")
	}
	sess.Close()
	captured := <-eventsDone
	warnings := warningEvents(captured)
	t.Logf("warning events (%d): %+v", len(warnings), warnings)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	message := warnings[0].Message
	for _, want := range []string{"Provider context disagreement", "provider-warning", "provider-model"} {
		if !strings.Contains(message, want) {
			t.Fatalf("provider warning message %q missing %q", message, want)
		}
	}
	// The warning reaches Notification hooks, so the provider's body stays
	// out of it (#3386).
	if strings.Contains(message, "user-private-prompt") {
		t.Fatalf("provider warning message %q carries the provider's body", message)
	}
	assertWarningPrecedesCompaction(t, captured)
}

func TestSessionOutputOnlyBudgetErrorDoesNotCompact(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "output-budget", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, &llm.ContextBudgetError{
				Provider:     "output-budget",
				Model:        "output-model",
				Limit:        "max_output_tokens",
				OutputTokens: 2_000,
				Maximum:      1_000,
			}
		},
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("output-budget", "output-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite output-only budget error")
	}
	sess.Close()
	captured := <-eventsDone
	for _, event := range captured {
		if event.Kind == events.EventContextCompaction {
			t.Fatal("output-only budget error triggered context compaction")
		}
	}
}

func TestSessionFallbackBudgetAfterLocalRecoveryEmitsOneWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeErrAdapter{name: "local-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("local-primary", 403, "rejected", nil, nil)
		},
	}})
	primary := testOpenAICompatProfile("local-primary", "primary-model", 0)
	primaryResolved := primary.Resolved()
	primaryResolved.Caps.ContextWindow = new(524_288)
	primaryResolved.Caps.MaxOutputTokens = new(8_192)
	primary = primary.WithResolved(primaryResolved)
	fallback := testOpenAICompatProfile("local-fallback", "fallback-model", 0)
	fallbackResolved := fallback.Resolved()
	fallbackResolved.Caps.MaxInputTokens = new(1)
	fallback = fallback.WithResolved(fallbackResolved)
	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"local-fallback/fallback-model"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }
	sess.contextMgr.RecordInputTokens(524_000, 0)
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite fallback input budget error")
	}
	sess.Close()
	captured := <-eventsDone
	warnings := warningEvents(captured)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0].Message, "Local context admission failed") {
		t.Fatalf("warning = %q, want local admission recovery warning", warnings[0].Message)
	}
	assertWarningPrecedesCompaction(t, captured)
}

func TestSessionFallbackProviderContextWarningUsesFallbackIdentity(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeErrAdapter{name: "identity-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("identity-primary", 403, "rejected", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("identity-primary", 403, "rejected", nil, nil)
		},
	}})
	fallbackAdapter := &fakeErrAdapter{name: "identity-fallback", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("identity-fallback", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("identity-fallback", 413, "context length exceeded", nil, nil)
		},
	}}
	client.Register(fallbackAdapter)
	primary := testOpenAICompatProfile("identity-primary", "primary-model", 0)
	fallback := testOpenAICompatProfile("identity-fallback", "fallback-model", 0)
	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"identity-fallback/fallback-model"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded after repeated fallback context errors")
	}
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want exactly one: %+v", len(warnings), warnings)
	}
	message := warnings[0].Message
	if !strings.Contains(message, "identity-fallback/fallback-model") || strings.Contains(message, "identity-primary/primary-model") {
		t.Fatalf("provider context warning uses wrong identity: %q", message)
	}
	if got := len(fallbackAdapter.Requests()); got != 2 {
		t.Fatalf("fallback provider calls = %d, want 2", got)
	}
}

func TestSessionUnrelatedErrorEmitsNoTokenBudgetRecoveryWarning(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeErrAdapter{name: "unrelated-warning", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("unrelated-warning", 401, "unauthorized", nil, nil)
		},
	}})
	profile := testOpenAICompatProfile("unrelated-warning", "unrelated-model", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite authentication error")
	}
	sess.Close()
	for _, warning := range warningEvents(<-eventsDone) {
		if strings.Contains(warning.Message, "Output allocation reduced") ||
			strings.Contains(warning.Message, "context admission") ||
			strings.Contains(warning.Message, "Provider context disagreement") {
			t.Fatalf("unrelated error emitted token-budget recovery warning: %+v", warning)
		}
	}
}

// A fallback that answers owns the round's context warning: the window must
// describe the model that handled the request, not the primary the round
// started on. The primary's window here is wide enough that nothing warns; the
// fallback's is small enough that the same round must warn, so the reported
// window proves which model the warning was computed against.
func TestSessionFallbackContextWarningNamesTheWindowThatHandledTheRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	permErr := llm.ErrorFromHTTPStatus("ctxwarn-primary", 403, "primary rejected", nil, nil)
	client.Register(&agenttest.ModelTrackingAdapter{
		Provider: "ctxwarn-primary",
		Respond: func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, permErr
		},
	})
	client.Register(&agenttest.ModelTrackingAdapter{
		Provider: "ctxwarn-fallback",
		Respond: func(req llm.Request) (llm.Response, error) {
			return communicateResponse(true, "fallback"), nil
		},
	})

	policy := llm.RetryPolicy{MaxRetries: 0}
	primary := WithContextWindow(testOpenAICompatProfile("ctxwarn-primary", "primary-model", 0), 2_000_000)
	fallbackWindow := 120_000
	fallback := WithContextWindow(testOpenAICompatProfile("ctxwarn-fallback", "fallback-model", 0), fallbackWindow)
	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	eventsDone := captureSessionEvents(sess)
	// The synthetic fallback row is wired after construction, as the other
	// fallback tests do: NewSession's own validation resolves through the real
	// registry, which has no row for it.
	sess.cfg.ModelFallbacks = []string{"ctxwarn-fallback/fallback-model"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }
	// Size the history from this test's own standing request so the round lands
	// between the fallback's 80% threshold and its window: a hardcoded history
	// length drops under the threshold when the prompt shrinks. The midpoint of
	// the band leaves margin on both sides.
	standing := standingRequestTokens(t, dir, primary, "task")
	threshold := fallbackWindow * 8 / 10
	target := (threshold + fallbackWindow) / 2
	historyChars := (target - standing) * 4
	if historyChars <= 0 {
		t.Fatalf("standing request %d tokens leaves no room under the %d-token target for fallback window %d", standing, target, fallbackWindow)
	}
	sess.history = []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("x", historyChars)))}
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	warnings := warningEvents(<-eventsDone)
	var ctxWarn *events.WarningData
	for i := range warnings {
		if strings.Contains(warnings[i].Message, "Context usage at") {
			ctxWarn = &warnings[i]
			break
		}
	}
	if ctxWarn == nil {
		t.Fatalf("no context-usage warning: the fallback's window is near, not the primary's: %+v", warnings)
	}
	if ctxWarn.ContextWindowSize != fallbackWindow {
		t.Fatalf("warning window = %d, want the fallback's %d: %+v", ctxWarn.ContextWindowSize, fallbackWindow, *ctxWarn)
	}
}
