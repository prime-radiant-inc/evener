package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestOpenAIResponsesContinuationFingerprint_ProductionPromptStableWithFixedEnvironment(t *testing.T) {
	client := openAIResponsesContinuationClientForTest(t)
	first := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-24"))
	second := openAIResponsesContinuationFingerprintForPromptTest(t, client, openAIContinuationPromptDataForTest("2026-06-24"))

	if first == "" || !strings.HasPrefix(first, "cont-req-v1:") {
		t.Fatalf("RequestFingerprint = %q, want cont-req-v1 prefix", first)
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
