package cheapmodel_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

func profileWithCheap(main string) *provider.Profile {
	return provider.WithCheapModel(provider.NewOpenAIProfile(main), "gpt-4.1-nano")
}

func TestCallerUsesSessionModelWhenCheapUnconfigured(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(400, "unexpected model"))
	caller := cheapmodel.New(clientWith(adapter))

	resp, err := complete(t, caller, provider.NewOpenAIProfile("main"))
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("Complete = (%q, %v), want answered", resp.Text(), err)
	}
	if got, want := adapter.Models(), []string{"main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
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
			profile := profileWithCheap("main")

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

func TestCallerDeduplicatesConcurrentCheapRefusalProbes(t *testing.T) {
	const callers = 16
	adapter := servesOnly("openai", "main", refusal(400, "The provided model identifier is invalid."))
	var cheapCalls atomic.Int32
	adapter.Respond = func(req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			cheapCalls.Add(1)
			runtime.Gosched()
			return llm.Response{}, refusal(400, "The provided model identifier is invalid.")("openai", req.Model)
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			<-start
			resp, err := complete(t, caller, profile)
			if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
				errs <- fmt.Errorf("Complete = (%q, %w), want answered", resp.Text(), err)
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := cheapCalls.Load(); got != 1 {
		t.Fatalf("cheap probe calls = %d, want 1 for %d concurrent callers; models = %v", got, callers, adapter.Models())
	}
	if got, want := len(adapter.Models()), callers+1; got != want {
		t.Fatalf("provider calls = %d, want %d (one cheap probe plus one fallback per caller)", got, want)
	}
}

// TestCallerRunsConcurrentSuccessfulCheapRequestsIndependently pins that a
// healthy cheap route is never serialized behind one shared probe: every
// caller issues its own cheap request, gets its own answer back, and none is
// pushed onto the session model. Only a refusal is a route property worth
// sharing (see TestCallerRunsConcurrentCheapRefusalsAsOneProbe).
func TestCallerRunsConcurrentSuccessfulCheapRequestsIndependently(t *testing.T) {
	const callers = 8
	// TRIPWIRE: in-process scripted adapter with no I/O; only fires on a
	// genuine deadlock in the probe machinery.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(_ context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant(req.Messages[0].Text())}, nil
	}

	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")
	start := make(chan struct{})
	ready := make(chan struct{}, callers)
	responses := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	expected := make(map[string]struct{}, callers)
	for i := range callers {
		prompt := fmt.Sprintf("request-%d", i)
		expected[prompt] = struct{}{}
		wg.Go(func() {
			ready <- struct{}{}
			<-start
			resp, err := caller.Complete(ctx, profile, llm.Request{Messages: []llm.Message{llm.User(prompt)}})
			if err != nil {
				errs <- err
				return
			}
			responses <- resp.Text()
		})
	}
	for range callers {
		<-ready
	}
	close(start)
	wg.Wait()
	close(errs)
	close(responses)

	for err := range errs {
		t.Errorf("concurrent Complete: %v", err)
	}
	models := adapter.Models()
	if len(models) != callers {
		t.Fatalf("provider calls = %d, want one cheap call per caller (%d): %v", len(models), callers, models)
	}
	for _, model := range models {
		if model != "gpt-4.1-nano" {
			t.Fatalf("provider calls = %v, want every one on the cheap model", models)
		}
	}
	for response := range responses {
		if _, ok := expected[response]; !ok {
			t.Errorf("unexpected response text = %q", response)
		}
		delete(expected, response)
	}
	if len(expected) != 0 {
		t.Errorf("missing responses for prompts: %v", expected)
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
			profile := profileWithCheap("main")

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
	profile := profileWithCheap("main")

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

// TestCallerSkipsARouteTheClientCannotServe pins the proactive half of the
// pair: a cheap route the client refuses outright (spec §7.3 — an id off the
// Codex allowlist is the only reference that fails to resolve) is never
// probed, and the request runs on the session model instead.
func TestCallerSkipsARouteTheClientCannotServe(t *testing.T) {
	tracker := servesOnly("openai", "main", refusal(400, "unexpected model"))
	caller := cheapmodel.New(clientWith(tracker))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("main"), "openai-codex/not-on-the-allowlist")

	if _, err := complete(t, caller, profile); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, want := tracker.Models(), []string{"main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v (the unservable cheap route is never probed)", got, want)
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

	_, err := complete(t, caller, profileWithCheap("main"))
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "model identifier") {
		t.Fatalf("Complete error = %v, want refusal and deadline", err)
	}
	if got := llm.Kind(err); got != llm.KindTimeout {
		t.Fatalf("error kind = %s, want fallback kind %s", got, llm.KindTimeout)
	}
}

// TestCompleteConfiguredReportsSessionModelWhenCheapUnset covers the entry
// point summarization uses: with no cheap model configured it runs the session
// model and reports that route so callers do not repeat it.
func TestCompleteConfiguredReportsSessionModelWhenCheapUnset(t *testing.T) {
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

	if _, err := complete(t, caller, profileWithCheap("main")); err != nil {
		t.Fatalf("openai Complete: %v", err)
	}
	if _, err := complete(t, caller, profileWithCheap("main-2")); err != nil {
		t.Fatalf("switched Complete: %v", err)
	}
	if got, want := openAI.Models(), []string{"gpt-4.1-nano", "main", "main-2"}; !slices.Equal(got, want) {
		t.Fatalf("openai models = %v, want %v", got, want)
	}
	bedrockProfile := provider.WithCheapModel(
		provider.WithProviderID(provider.NewOpenAIProfile("bedrock-main"), "bedrock"),
		"gpt-4.1-nano",
	)
	if _, err := complete(t, caller, bedrockProfile); err != nil {
		t.Fatalf("bedrock Complete: %v", err)
	}
	if got, want := bedrock.Models(), []string{"gpt-4.1-nano", "bedrock-main"}; !slices.Equal(got, want) {
		t.Fatalf("bedrock models = %v, want %v", got, want)
	}
}

type contextTrackingAdapter struct {
	Provider string
	Respond  func(context.Context, llm.Request) (llm.Response, error)

	mu     sync.Mutex
	models []string
}

func (a *contextTrackingAdapter) Name() string { return a.Provider }

func (a *contextTrackingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.models = append(a.models, req.Model)
	a.mu.Unlock()
	resp, err := a.Respond(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Provider = a.Provider
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *contextTrackingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *contextTrackingAdapter) Models() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.models)
}

func TestCallerWaiterHonorsItsOwnContext(t *testing.T) {
	cheapStarted := make(chan struct{})
	releaseCheap := make(chan struct{})
	var started sync.Once
	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			started.Do(func() { close(cheapStarted) })
			select {
			case <-releaseCheap:
				return llm.Response{}, refusal(400, "The provided model identifier is invalid.")("openai", req.Model)
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")
	leaderDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("leader")}})
		leaderDone <- err
	}()
	<-cheapStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(waiterCtx, profile, llm.Request{Messages: []llm.Message{llm.User("waiter")}})
		waiterDone <- err
	}()
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v, want context.Canceled", err)
	}

	close(releaseCheap)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error = %v", err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want one shared cheap probe and leader fallback", got)
	}
}

func TestCallerRetriesAfterCanceledProbe(t *testing.T) {
	cheapStarted := make(chan struct{})
	var started sync.Once
	var cheapCalls atomic.Int32
	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			if cheapCalls.Add(1) == 1 {
				started.Do(func() { close(cheapStarted) })
				<-ctx.Done()
				return llm.Response{}, ctx.Err()
			}
			return llm.Response{}, refusal(400, "The provided model identifier is invalid.")("openai", req.Model)
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(leaderCtx, profile, llm.Request{Messages: []llm.Message{llm.User("leader")}})
		leaderDone <- err
	}()
	<-cheapStarted
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled leader error = %v, want context.Canceled", err)
	}

	resp, err := complete(t, caller, profile)
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("retry Complete = (%q, %v), want answered", resp.Text(), err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "gpt-4.1-nano", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want canceled probe removed before retry", got)
	}
}

func TestCallerKeepsRefusalFlightUntilAllFallbacksSettle(t *testing.T) {
	cheapStarted := make(chan struct{})
	releaseCheap := make(chan struct{})
	leaderFallbackStarted := make(chan struct{})
	siblingFallbackStarted := make(chan struct{})
	thirdFallbackStarted := make(chan struct{})
	releaseFallbacks := make(chan struct{})
	var cheapOnce sync.Once
	var leaderOnce sync.Once
	var siblingOnce sync.Once
	var thirdOnce sync.Once
	var cheapCalls atomic.Int32
	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(ctx context.Context, req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			cheapCalls.Add(1)
			cheapOnce.Do(func() { close(cheapStarted) })
			select {
			case <-releaseCheap:
				return llm.Response{}, refusal(400, "The provided model identifier is invalid.")("openai", req.Model)
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		switch req.Messages[0].Text() {
		case "leader":
			leaderOnce.Do(func() { close(leaderFallbackStarted) })
			<-ctx.Done()
			return llm.Response{}, ctx.Err()
		case "sibling":
			siblingOnce.Do(func() { close(siblingFallbackStarted) })
			<-releaseFallbacks
			return llm.Response{Message: llm.Assistant("sibling answered")}, nil
		case "third":
			thirdOnce.Do(func() { close(thirdFallbackStarted) })
			<-releaseFallbacks
			return llm.Response{Message: llm.Assistant("third answered")}, nil
		default:
			return llm.Response{}, fmt.Errorf("unexpected prompt %q", req.Messages[0].Text())
		}
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(leaderCtx, profile, llm.Request{Messages: []llm.Message{llm.User("leader")}})
		leaderDone <- err
	}()
	<-cheapStarted
	close(releaseCheap)
	<-leaderFallbackStarted

	siblingDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("sibling")}})
		siblingDone <- err
	}()
	<-siblingFallbackStarted

	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context.Canceled", err)
	}

	thirdDone := make(chan error, 1)
	go func() {
		_, err := caller.Complete(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("third")}})
		thirdDone <- err
	}()
	<-thirdFallbackStarted
	if got := cheapCalls.Load(); got != 1 {
		t.Fatalf("cheap probe calls = %d, want one while sibling fallback remains active; models = %v", got, adapter.Models())
	}

	close(releaseFallbacks)
	if err := <-siblingDone; err != nil {
		t.Fatalf("sibling error = %v, want success", err)
	}
	if err := <-thirdDone; err != nil {
		t.Fatalf("third error = %v, want success", err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main", "main", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want one cheap probe and three fallbacks", got)
	}
}

func TestCallerRetriesAfterFallbackFailure(t *testing.T) {
	var fallbackCalls atomic.Int32
	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(_ context.Context, req llm.Request) (llm.Response, error) {
		if req.Model == "gpt-4.1-nano" {
			return llm.Response{}, refusal(400, "The provided model identifier is invalid.")("openai", req.Model)
		}
		if fallbackCalls.Add(1) == 1 {
			return llm.Response{}, errors.New("fallback unavailable")
		}
		return llm.Response{Message: llm.Assistant("answered")}, nil
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := profileWithCheap("main")
	if _, err := complete(t, caller, profile); err == nil || !strings.Contains(err.Error(), "fallback unavailable") {
		t.Fatalf("first Complete error = %v, want fallback failure", err)
	}
	resp, err := complete(t, caller, profile)
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("retry Complete = (%q, %v), want answered", resp.Text(), err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main", "gpt-4.1-nano", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want failed fallback not to latch", got)
	}
}

func TestCallerDoesNotShareSuccessfulResponsesAcrossRequests(t *testing.T) {
	adapter := &contextTrackingAdapter{Provider: "openai"}
	adapter.Respond = func(_ context.Context, req llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant(req.Messages[0].Text())}, nil
	}
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("main"), "cheap")
	first, err := caller.Complete(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("first")}})
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	second, err := caller.Complete(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("second")}})
	if err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if got, want := []string{first.Text(), second.Text()}, []string{"first", "second"}; !slices.Equal(got, want) {
		t.Fatalf("response texts = %v, want %v", got, want)
	}
	if got, want := adapter.Models(), []string{"cheap", "cheap"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want two independent successful requests", got)
	}
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

	resp, err := complete(t, caller, profileWithCheap("main"))
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

func TestCompleteRoutedUsesTheExplicitRoute(t *testing.T) {
	adapter := servesOnly("openai", "main", refusal(400, "The provided model identifier is invalid."))
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.NewOpenAIProfile("main")

	resp, err := caller.CompleteRouted(context.Background(), profile, "openai", "gpt-4.1-nano", llm.Request{
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil || strings.TrimSpace(resp.Text()) != "answered" {
		t.Fatalf("CompleteRouted = (%q, %v), want answered via fallback", resp.Text(), err)
	}
	// Refusal of the routed model is learned: the second call goes straight to
	// the session model instead of re-probing.
	if _, err := caller.CompleteRouted(context.Background(), profile, "openai", "gpt-4.1-nano", llm.Request{
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("second CompleteRouted: %v", err)
	}
	if got, want := adapter.Models(), []string{"gpt-4.1-nano", "main", "main"}; !slices.Equal(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}
