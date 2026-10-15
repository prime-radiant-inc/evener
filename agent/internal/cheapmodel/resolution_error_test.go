package cheapmodel_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// The aggregate keeps its existing classification, but diagnostics need the
// last route's own failure: an earlier refusal must not hide a terminal 503.
func TestResolutionErrorPreservesTerminalRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		cheap        string
		cheapStatus  int
		activeStatus int
		wantModel    string
		wantKind     llm.ErrorKind
		wantClass    llm.ErrorClass
		wantPrevious int
		wantSession  bool
		wantCalls    []string
	}{
		{"cheap rate limit", "cheap-fixture", 429, 0, "cheap-fixture", llm.KindRateLimit, llm.ErrorClassRetryable, 0, false, []string{"cheap-fixture"}},
		{"no configured cheap", "", 0, 503, "active-fixture", llm.KindServer, llm.ErrorClassRetryable, 0, true, []string{"active-fixture"}},
		{"unservable cheap", "absent/cheap-fixture", 0, 503, "active-fixture", llm.KindServer, llm.ErrorClassRetryable, 0, true, []string{"active-fixture"}},
		{"refusal then server failure", "cheap-fixture", 400, 503, "active-fixture", llm.KindServer, llm.ErrorClassRetryable, 1, true, []string{"cheap-fixture", "active-fixture"}},
		{"both refused", "cheap-fixture", 400, 400, "active-fixture", llm.KindInvalidRequest, llm.ErrorClassPermanent, 1, true, []string{"cheap-fixture", "active-fixture"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cheapErr := llm.ErrorFromHTTPStatus("openai", tc.cheapStatus, "The provided model identifier is invalid.", nil, nil)
			activeErr := llm.ErrorFromHTTPStatus("openai", tc.activeStatus, "The provided model identifier is invalid.", nil, nil)
			adapter := &agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(req llm.Request) (llm.Response, error) {
				if req.Model == "cheap-fixture" {
					return llm.Response{}, cheapErr
				}
				return llm.Response{}, activeErr
			}}
			profile := provider.WithCheapModel(provider.NewOpenAIProfile("active-fixture"), tc.cheap)
			_, ranSession, err := cheapmodel.New(clientWith(adapter)).CompleteConfigured(context.Background(), profile, llm.Request{Messages: []llm.Message{llm.User("route-fixture")}})
			if ranSession != tc.wantSession || !slices.Equal(adapter.Models(), tc.wantCalls) {
				t.Fatalf("session route = %t, calls = %v; want %t, %v", ranSession, adapter.Models(), tc.wantSession, tc.wantCalls)
			}
			var failure *cheapmodel.ResolutionError
			if !errors.As(err, &failure) {
				t.Fatalf("missing route evidence for %T", err)
			}
			if failure.Terminal.Provider != "openai" || failure.Terminal.Model != tc.wantModel || llm.Kind(failure.Terminal.Err) != tc.wantKind {
				t.Fatalf("terminal = %s/%s, kind %s; want openai/%s, %s", failure.Terminal.Provider, failure.Terminal.Model, llm.Kind(failure.Terminal.Err), tc.wantModel, tc.wantKind)
			}
			if len(failure.Previous) != tc.wantPrevious {
				t.Fatalf("previous routes = %d, want %d", len(failure.Previous), tc.wantPrevious)
			}
			if tc.wantPrevious != 0 {
				previous := failure.Previous[0]
				if previous.Provider != "openai" || previous.Model != "cheap-fixture" || !errors.Is(previous.Err, cheapErr) || !errors.Is(err, cheapErr) {
					t.Fatal("lost cheap route cause")
				}
				if llm.Kind(err) != llm.KindInvalidRequest {
					t.Fatalf("aggregate kind = %s, want invalid_request", llm.Kind(err))
				}
			}
			if llm.Classify(err) != tc.wantClass {
				t.Fatalf("aggregate class = %s, want %s", llm.Classify(err), tc.wantClass)
			}
			terminalErr := cheapErr
			if tc.wantSession {
				terminalErr = activeErr
			}
			if !errors.Is(err, terminalErr) || !errors.Is(failure.Terminal.Err, terminalErr) {
				t.Fatal("lost terminal cause")
			}
			if errors.Is(err, cheapmodel.ErrAllModelsRefused) != (tc.wantPrevious == 1 && tc.activeStatus == 400) {
				t.Fatal("changed all-models-refused marker")
			}
		})
	}
}

func TestResolutionErrorOmitsLatchedRoute(t *testing.T) {
	t.Parallel()
	activeErr := llm.ErrorFromHTTPStatus("openai", 503, "opaque-fixture", nil, nil)
	var failActive bool
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(req llm.Request) (llm.Response, error) {
		if req.Model == "cheap-fixture" {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "The provided model identifier is invalid.", nil, nil)
		}
		if failActive {
			return llm.Response{}, activeErr
		}
		return llm.Response{Message: llm.Assistant("success-fixture")}, nil
	}}
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("active-fixture"), "cheap-fixture")
	req := llm.Request{Messages: []llm.Message{llm.User("route-fixture")}}
	if _, ranSession, err := caller.CompleteConfigured(context.Background(), profile, req); err != nil || !ranSession {
		t.Fatalf("initial fallback: ranSession=%t, err=%v", ranSession, err)
	}
	failActive = true
	_, _, err := caller.CompleteConfigured(context.Background(), profile, req)
	var failure *cheapmodel.ResolutionError
	if !errors.As(err, &failure) || failure.Terminal.Model != "active-fixture" || len(failure.Previous) != 0 || !errors.Is(err, activeErr) {
		t.Fatal("latched route attributed as a new failure, or active failure lost")
	}
	if want := []string{"cheap-fixture", "active-fixture", "active-fixture"}; !slices.Equal(adapter.Models(), want) {
		t.Fatalf("calls = %v, want %v", adapter.Models(), want)
	}
}

func TestResolutionErrorProbeWaitCancellation(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(llm.Request) (llm.Response, error) {
		close(entered)
		<-release
		return llm.Response{Message: llm.Assistant("success-fixture")}, nil
	}}
	caller := cheapmodel.New(clientWith(adapter))
	profile := provider.WithCheapModel(provider.NewOpenAIProfile("active-fixture"), "cheap-fixture")
	req := llm.Request{Messages: []llm.Message{llm.User("route-fixture")}}
	done := make(chan error, 1)
	go func() {
		_, _, err := caller.CompleteConfigured(context.Background(), profile, req)
		done <- err
	}()
	<-entered
	t.Cleanup(func() {
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("probe leader failed: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ranSession, err := caller.CompleteConfigured(ctx, profile, req)
	var failure *cheapmodel.ResolutionError
	if !errors.As(err, &failure) || !errors.Is(failure.Terminal.Err, context.Canceled) || !errors.Is(err, context.Canceled) || len(failure.Previous) != 0 {
		t.Fatal("lost cancellation or attributed another request's result")
	}
	if ranSession || !slices.Equal(adapter.Models(), []string{"cheap-fixture"}) {
		t.Fatalf("cancelled waiter made a request: session=%t, calls=%v", ranSession, adapter.Models())
	}
}
