package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

func TestResponsesContinuationRebuildsAnchorWhenReasoningReplayChanges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	req := llm.Request{Provider: "gateway", Model: "reasoner", Store: new(true)}
	var history []schema.Turn
	var defaultRaw llm.ContinuationMetadata
	for i, step := range []struct {
		replay                    *bool
		model, stream, wantAnchor string
		unqualifiedFingerprint    bool
	}{
		{},
		{replay: new(false), wantAnchor: "resp_0"},
		{replay: new(true)},
		{replay: new(true), wantAnchor: "resp_2"},
		{replay: new(false)},
		{wantAnchor: "resp_4"},
		{replay: new(true), model: "model-a", stream: "/stream/a"},
		{replay: new(true), model: "model-a", stream: "/stream/a", wantAnchor: "resp_6"},
		{replay: new(true), model: "model-b", stream: "/stream/a"},
		{replay: new(true), model: "model-b", stream: "/stream/a", wantAnchor: "resp_8"},
		{replay: new(true), model: "model-b", stream: "/stream/b"},
		{replay: new(true), model: "model-b", stream: "/stream/b", wantAnchor: "resp_10"},
		{replay: new(true), model: "model-a", unqualifiedFingerprint: true},
		{replay: new(true)},
		{replay: new(true), stream: "/stream/a", unqualifiedFingerprint: true},
		{replay: new(true)},
		{replay: new(true), wantAnchor: "resp_15"},
	} {
		instance := openaiInstance("https://gateway.example.invalid/v1")
		instance.Models = map[string]registry.Model{req.Model: {AliasOf: "gpt-5.4"}}
		instance.Transport.StreamEndpoint = step.stream
		if step.model != "" {
			instance.Transport.Body = map[string]any{"model": step.model}
		}
		instance.Caps.ReplayReasoningText = step.replay
		client := registryClientAt(t, dir, map[string]registry.Provider{"gateway": instance}, []string{"gateway"})
		profile := resolveClientProfile(t, client, req.Provider+"/"+req.Model)
		s := &Session{client: client, cfg: SessionConfig{OpenAIResponsesContinuation: "auto"}}
		user := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("u_sentinel_%d", i)))
		history = append(history, user)
		req.Messages = append(req.Messages, user.Message)
		got, _, _ := s.applyResponsesContinuationAnchorPlanning(t.Context(), profile, req, history, true, false)
		wantMode := llm.HistoryModeFullHistory
		wantMessages := len(req.Messages)
		if step.wantAnchor != "" {
			wantMode = llm.HistoryModeResponsesDelta
			wantMessages = 1
		}
		if got.Continuation == nil {
			t.Fatalf("round %d: missing continuation metadata, diagnostic %q", i, got.ContinuationDiagnostic)
		}
		if got.HistoryMode != wantMode || got.PreviousResponseID != step.wantAnchor || len(got.Messages) != wantMessages {
			t.Errorf("round %d: %q with anchor %q and %d messages, want %q with anchor %q and %d messages", i, got.HistoryMode, got.PreviousResponseID, len(got.Messages), wantMode, step.wantAnchor, wantMessages)
		}
		anchor := responsesContinuationEligibleAssistantTurn(fmt.Sprintf("resp_%d", i))
		row := profile.Resolved()
		anchor.ResponseProvider, anchor.ResponseRequestModel, anchor.ResponseProtocol = row.Instance, row.ModelID, row.Protocol
		anchor.ResponseWireModel, anchor.ResponseReasoningRoute = row.OutboundModelSelector(), profile.ReasoningReplayRoute()
		anchor.Message.Content = append(anchor.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: fmt.Sprintf("rs_%d", i), Text: "r1"}})
		anchor.ResponseRequestFingerprint = got.Continuation.RequestFingerprint
		anchor.ResponseStorageScopeFingerprint = got.Continuation.StorageScopeFingerprint
		if row.ReplaysRawReasoning() && step.model == "" && step.stream == "" {
			defaultRaw = *got.Continuation
		}
		if step.unqualifiedFingerprint {
			// This stored response records its actual issuer alongside fingerprints
			// calculated from only the alias and the completion endpoint.
			anchor.ResponseRequestFingerprint = defaultRaw.RequestFingerprint
			anchor.ResponseStorageScopeFingerprint = defaultRaw.StorageScopeFingerprint
		}
		if i == 0 {
			// A stored strict anchor carries the fingerprint of the wire controls alone.
			body, err := responses.DefaultProtocol.BuildBody(llm.ShapeRequest(req, row), row)
			if err != nil {
				t.Fatal(err)
			}
			anchor.ResponseRequestFingerprint, err = llm.ResponsesRequestFingerprint(llm.ResponsesEndpointFamilyOpenAIPublic, body)
			if err != nil {
				t.Fatal(err)
			}
		}
		history = append(history, anchor)
		req.Messages = append(req.Messages, anchor.Message)
	}
}

func TestOpenAIResponsesContinuationFingerprint_ProductionPromptStableWithFixedEnvironment(t *testing.T) {
	client := openAIResponsesContinuationClientForTest(t)
	first := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-24"))
	second := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-24"))

	if first == "" || !strings.HasPrefix(first, "cont-req-v2:") {
		t.Fatalf("RequestFingerprint = %q, want cont-req-v2 prefix", first)
	}
	if first != second {
		t.Fatalf("fingerprint changed for fixed production prompt environment:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func TestOpenAIResponsesContinuationFingerprint_ProductionPromptChangesWithToday(t *testing.T) {
	client := openAIResponsesContinuationClientForTest(t)
	first := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-24"))
	second := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-25"))

	if first == second {
		t.Fatalf("fingerprint did not change after Today changed: %s", first)
	}
}

func openAIContinuationPromptDataForTest(today string) promptData {
	return promptData{
		WorkingDir:      "/tmp/evener-continuation",
		Platform:        "darwin",
		OSVersion:       "15.5",
		Today:           today,
		Model:           "gpt-5.4",
		KnowledgeCutoff: "2025-06-01",
	}
}

func openAIResponsesContinuationClientForTest(t *testing.T) *llm.Client {
	t.Helper()
	r := mustTestRegistry(map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "sk-test"},
	})
	// The state dir keys the continuation hasher; a fresh one per test keeps
	// the fingerprints comparable within a test and unrelated across them.
	return llm.NewClient(llm.WithRegistry(r), llm.WithClientStateDir(t.TempDir()))
}

func openAIResponsesContinuationFingerprintForPromptTest(t *testing.T, client *llm.Client, data promptData) string {
	t.Helper()
	profile := NewOpenAIProfile(data.Model)
	systemPrompt := renderPromptForTest(t, profile, data)
	plan, err := client.PlanResponsesContinuation(context.Background(), llm.Request{
		Provider: "openai",
		Model:    data.Model,
		Messages: []llm.Message{
			llm.System(systemPrompt),
			llm.User("hi"),
		},
	})
	if err != nil {
		t.Fatalf("PlanResponsesContinuation: %v", err)
	}
	return plan.RequestFingerprint
}
