package cheapmodel_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
)

func refusal(status int, message string) func(string, string) error {
	return func(prov, _ string) error {
		return llm.ErrorFromHTTPStatus(prov, status, message, nil, nil)
	}
}

func servesOnly(name, served string, reject func(string, string) error) *agenttest.ModelTrackingAdapter {
	a := &agenttest.ModelTrackingAdapter{Provider: name}
	a.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model != served {
			return llm.Response{}, reject(name, req.Model)
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	return a
}

func clientWith(adapters ...llm.ProviderAdapter) *llm.Client {
	client := llm.NewClient()
	for _, adapter := range adapters {
		client.Register(adapter)
	}
	return client
}

func complete(t *testing.T, caller *cheapmodel.Caller, profile *provider.Profile) (llm.Response, error) {
	t.Helper()
	return caller.Complete(context.Background(), profile, llm.Request{
		Provider: "ignored",
		Model:    "ignored",
		Messages: []llm.Message{llm.User("hi")},
	})
}

func TestCallerFallsBackAndRemembersObservedRefusals(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"codex", "The 'gpt-4.1-nano' model is not supported when using Codex with a ChatGPT account"},
		{"bedrock", "The provided model identifier is invalid."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := servesOnly("openai", "main", refusal(400, test.message))
			caller := cheapmodel.New(clientWith(adapter))
			profile := provider.NewOpenAIProfile("main")

			for range 2 {
				resp, err := complete(t, caller, profile)
				if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
					t.Fatalf("Complete = (%q, %v), want answered", resp.Text(), err)
				}
			}
			if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main", "main"}; !slices.Equal(got, want) {
				t.Fatalf("models = %v, want %v", got, want)
			}
		})
	}
}

func TestCallerDoesNotGeneralizeRequestErrorsIntoModelRefusals(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
	}{
		{"invalid request", 400, "invalid field: response_format"},
		{"access denied", 403, "request forbidden by organization policy"},
		{"not found", 404, "requested resource not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := servesOnly("openai", "main", refusal(test.status, test.message))
			caller := cheapmodel.New(clientWith(adapter))
			profile := provider.NewOpenAIProfile("main")

			for range 2 {
				if _, err := complete(t, caller, profile); err == nil {
					t.Fatal("Complete succeeded, want request error")
				}
			}
			if got, want := adapter.Models(), []string{"gpt-4.1-nano", "gpt-4.1-nano"}; !slices.Equal(got, want) {
				t.Fatalf("models = %v, want %v", got, want)
			}
		})
	}
}

// TestCallerIgnoresRefusalWordingOnARetryableError pins the permanence gate in
// refusesModel: a transient failure that happens to carry refusal wording (a 429
// whose body echoes the model-identifier complaint) is a "try again later", not
// "this provider will never serve this model". Falling back on it would abandon
// the cheap model over a blip, and latching it would make that abandonment
// permanent for the session.
func TestCallerIgnoresRefusalWordingOnARetryableError(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(429, "The provided model identifier is invalid."))
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.NewOpenAIProfile("main")

	for range 2 {
		_, err := complete(t, caller, profile)
		if err == nil {
			t.Fatal("Complete succeeded, want the rate-limit error")
		}
		if llm.Classify(err) != llm.ErrorClassRetryable {
			t.Fatalf("error class = %v, want retryable", llm.Classify(err))
		}
	}
	// Two cheap attempts and no session-model attempt: no fallback, no latch.
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "gpt-4.1-nano"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

// TestCallerDoesNotRetryWhenTheCheapModelIsTheSessionModel pins the cheap ==
// active guard: with the cheap ref pointing at the session's own model there is
// nothing to fall back to, so a refusal must surface after a single request
// rather than repeating the identical call.
func TestCallerDoesNotRetryWhenTheCheapModelIsTheSessionModel(t *testing.T) {
	adapter := servesOnly("openai", "unreachable", refusal(400, "The provided model identifier is invalid."))
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("main"), "main")

	_, err := complete(t, caller, profile)
	if err == nil || !strings.Contains(err.Error(), "model identifier") {
		t.Fatalf("Complete error = %v, want the refusal", err)
	}
	if got, want := adapter.Models(), []string{"main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v (no identical retry)", got, want)
	}
}

func TestCallerUsesStaticCompatibilityWithoutAProbe(t *testing.T) {
	tracker := servesOnly("openai", "main", refusal(400, "unexpected model"))
	adapter := &declaredModelAdapter{ModelTrackingAdapter: tracker, served: "main"}
	caller := cheapmodel.New(clientWith(adapter))

	if _, err := complete(t, caller, provider.NewOpenAIProfile("main")); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, want := tracker.Models(), []string{"main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

func TestCallerReturnsBothErrorsWhenFallbackFails(t *testing.T) {
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400,
				"The provided model identifier is invalid.", nil, nil)
		}
		return llm.Response{}, context.DeadlineExceeded
	}
	caller := cheapmodel.New(clientWith(adapter))

	_, err := complete(t, caller, provider.NewOpenAIProfile("main"))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "model identifier") {
		t.Fatalf("Complete error = %v, want refusal and deadline", err)
	}
	if got := llm.Kind(err); got != llm.KindTimeout {
		t.Fatalf("error kind = %s, want fallback kind %s", got, llm.KindTimeout)
	}
}

// TestCompleteConfiguredPrefersTheSessionModelOverAProviderDefault covers the
// entry point summarization uses: with no cheap model configured it runs the
// session's own model rather than the provider's default cheap one, and it says
// so, so a caller layering its own routes does not repeat the session model.
func TestCompleteConfiguredPrefersTheSessionModelOverAProviderDefault(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(400, "unexpected model"))
	caller := cheapmodel.New(clientWith(adapter))

	resp, ranSessionModel, err := caller.CompleteConfigured(context.Background(),
		provider.NewOpenAIProfile("main"), llm.Request{Messages: []llm.Message{llm.User("hi")}})
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("CompleteConfigured = (%q, %v), want answered", resp.Text(), err)
	}
	if !ranSessionModel {
		t.Fatal("ranSessionModel = false, want true")
	}
	if got, want := adapter.Models(), []string{"main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

// TestCompleteConfiguredReportsTheRoutesItRan covers the configured-cheap
// branch: the cheap model runs first and the session model is reported only
// once the caller has actually fallen back onto it.
func TestCompleteConfiguredReportsTheRoutesItRan(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(400, "The provided model identifier is invalid."))
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		switch req.Model {
		case "cheap":
			return llm.Response{Message: llm.Assistant("cheap answered")}, nil
		case "main":
			return llm.Response{Message: llm.Assistant("main answered")}, nil
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "The provided model identifier is invalid.", nil, nil)
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("main"), "cheap")

	resp, ranSessionModel, err := caller.CompleteConfigured(context.Background(), profile,
		llm.Request{Messages: []llm.Message{llm.User("hi")}})
	if err != nil || strings.TrimSpace(resp.Text()) != "cheap answered" {
		t.Fatalf("CompleteConfigured = (%q, %v), want the cheap model's answer", resp.Text(), err)
	}
	if ranSessionModel {
		t.Fatal("ranSessionModel = true after a cheap success, want false")
	}

	refusedProfile := provider.WithCheapModel(provider.NewOpenAIProfile("main"), "refused")
	resp, ranSessionModel, err = caller.CompleteConfigured(context.Background(), refusedProfile,
		llm.Request{Messages: []llm.Message{llm.User("hi")}})
	if err != nil || strings.TrimSpace(resp.Text()) != "main answered" {
		t.Fatalf("CompleteConfigured = (%q, %v), want the session model's answer", resp.Text(), err)
	}
	if !ranSessionModel {
		t.Fatal("ranSessionModel = false after falling back, want true")
	}
}

func TestCallerKeepsRefusalsAcrossModelSwitchButNotProviderSwitch(t *testing.T) {
	openAI := &agenttest.ModelTrackingAdapter{Provider: "openai"}
	openAI.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "The provided model identifier is invalid.", nil, nil)
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	bedrock := servesOnly("bedrock", "bedrock-main", refusal(400, "The provided model identifier is invalid."))
	caller := cheapmodel.New(clientWith(openAI, bedrock))

	if _, err := complete(t, caller, provider.NewOpenAIProfile("main")); err != nil {
		t.Fatalf("openai Complete: %v", err)
	}
	if _, err := complete(t, caller, provider.NewOpenAIProfile("main-2")); err != nil {
		t.Fatalf("switched Complete: %v", err)
	}
	if got, want := openAI.Models(), []string{"gpt-4.1-nano", "main", "main-2"}; !slices.Equal(got, want) {
		t.Fatalf("openai models = %v, want %v", got, want)
	}
	bedrockProfile := provider.WithProviderID(provider.NewOpenAIProfile("bedrock-main"), "bedrock")
	if _, err := complete(t, caller, bedrockProfile); err != nil {
		t.Fatalf("bedrock Complete: %v", err)
	}
	if got, want := bedrock.Models(), []string{"gpt-4.1-nano", "bedrock-main"}; !slices.Equal(got, want) {
		t.Fatalf("bedrock models = %v, want %v", got, want)
	}
}

type declaredModelAdapter struct {
	*agenttest.ModelTrackingAdapter
	served string
}

func (a *declaredModelAdapter) ValidateModel(model string) error {
	if model == a.served {
		return nil
	}
	return fmt.Errorf("model %s is not supported (served: %s)", model, a.served)
}

// recordingSink is an APIAttemptSink that also satisfies Middleware so it can be
// registered on a client. It records attempts and settlements without durable
// storage so tests can assert group/attempt/settlement counts.
type recordingSink struct {
	mu          sync.Mutex
	attempts    []apilog.APIAttemptRecord
	settlements []apilog.APIAttemptGroupSettlement
}

func (s *recordingSink) AppendAttempt(_ context.Context, rec apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, rec)
	return nil
}

func (s *recordingSink) AppendSettlement(_ context.Context, rec apilog.APIAttemptGroupSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlements = append(s.settlements, rec)
	return nil
}

func (s *recordingSink) WrapComplete(next llm.CompleteFunc) llm.CompleteFunc {
	return func(ctx context.Context, req llm.Request) (llm.Response, error) {
		return next(ctx, req)
	}
}

func (s *recordingSink) WrapStream(next llm.StreamFunc) llm.StreamFunc {
	return func(ctx context.Context, req llm.Request) (llm.Stream, error) {
		return next(ctx, req)
	}
}

func (s *recordingSink) snapshot() ([]apilog.APIAttemptRecord, []apilog.APIAttemptGroupSettlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.attempts), slices.Clone(s.settlements)
}

// attemptRecordingAdapter wraps ModelTrackingAdapter and emits a canonical
// API attempt for each Complete call so a recordingSink can observe attempts.
// Real HTTP transports do this in the transport layer; fake adapters must do
// it explicitly.
type attemptRecordingAdapter struct {
	*agenttest.ModelTrackingAdapter
}

func (a *attemptRecordingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := a.ModelTrackingAdapter.Complete(ctx, req)
	startedAt := time.Now()
	meta := llm.APIAttemptMeta{
		ProviderInstance: a.Provider,
		RequestModel:     req.Model,
		Method:           "POST",
		Endpoint:         "https://" + a.Provider + ".invalid/v1/complete",
		RequestBody:      []byte(`{"input":"test"}`),
		StartedAt:        startedAt,
	}
	attempt := llm.BeginAPIAttempt(ctx, meta)
	result := llm.APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"ok"}`),
		Response:     &resp,
		Outcome:      apilog.AttemptSuccess,
		FinishedAt:   startedAt.Add(time.Millisecond),
	}
	if err != nil {
		result.StatusCode = http.StatusBadRequest
		result.Response = nil
		result.Outcome = apilog.AttemptProviderReject
		result.Err = err
	}
	attempt.Complete(result)
	return resp, err
}

// TestCallerEmitsOneAttemptGroupForCheapAndFallback asserts that a fallback
// resolution produces one shared API-attempt group: two attempts (cheap then
// fallback) with the same group ID, and exactly one settlement.
func TestCallerEmitsOneAttemptGroupForCheapAndFallback(t *testing.T) {
	inner := servesOnly("openai", "main", refusal(400, "The provided model identifier is invalid."))
	adapter := &attemptRecordingAdapter{ModelTrackingAdapter: inner}
	sink := &recordingSink{}
	client := llm.NewClient()
	client.Register(adapter)
	client.Use(sink)
	caller := cheapmodel.New(client)

	resp, err := complete(t, caller, provider.NewOpenAIProfile("main"))
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("Complete = (%q, %v), want answered", resp.Text(), err)
	}

	attempts, settlements := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2 (cheap + fallback)", len(attempts))
	}
	if len(settlements) != 1 {
		t.Fatalf("settlement count = %d, want 1", len(settlements))
	}
	groupID := attempts[0].AttemptGroupID
	for i, a := range attempts {
		if a.AttemptGroupID != groupID {
			t.Fatalf("attempt %d group = %q, want %q (one shared group)", i+1, a.AttemptGroupID, groupID)
		}
	}
	if settlements[0].AttemptGroupID != groupID {
		t.Fatalf("settlement group = %q, want %q", settlements[0].AttemptGroupID, groupID)
	}
	if settlements[0].FinalAttemptCount != 2 {
		t.Fatalf("settlement final attempt count = %d, want 2", settlements[0].FinalAttemptCount)
	}
}
