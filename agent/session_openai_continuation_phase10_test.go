package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSession_OpenAIResponsesContinuationPhase10DeltaCarriesFullHistoryShadowEstimate(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.HistoryMode != llm.HistoryModeResponsesDelta {
					t.Fatalf("HistoryMode = %q, want responses_delta", req.HistoryMode)
				}
				if req.FullHistoryInputTokensEstimate != 777 {
					t.Fatalf("FullHistoryInputTokensEstimate = %d, want 777", req.FullHistoryInputTokensEstimate)
				}
				if req.InputTokensEstimate <= 0 {
					t.Fatalf("InputTokensEstimate = %d, want positive dispatched estimate", req.InputTokensEstimate)
				}
				if requestMessagesContainText(req.Messages, "phase10 prior user marker") {
					t.Fatalf("delta request included prior marker: %+v", req.Messages)
				}
				if !requestMessagesContainText(req.FullHistoryFallbackMessages, "phase10 prior user marker") {
					t.Fatalf("fallback sidecar missing prior marker: %+v", req.FullHistoryFallbackMessages)
				}
				return agenttest.FinalResponse("phase 10 delta")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			responsesContinuationShadowEstimateFunc: func(req llm.Request) (int, bool) {
				if requestMessagesContainText(req.Messages, "phase10 prior user marker") &&
					requestMessagesContainText(req.Messages, "phase10 current user marker") {
					return 777, true
				}
				return 0, false
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	anchor := phase9MatchingAnchor("resp_phase10_shadow")
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase10 prior user marker")),
		anchor,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase10 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
}
