package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	openaiadapter "primeradiant.com/serf/llm/providers/openai"
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
		WorkingDir:      "/tmp/serf-continuation",
		Platform:        "darwin",
		OSVersion:       "15.5",
		Today:           today,
		Model:           "gpt-5.4",
		KnowledgeCutoff: "2025-06-01",
	}
}

func openAIResponsesContinuationClientForTest(t *testing.T) *llm.Client {
	t.Helper()
	adapter, err := openaiadapter.NewForInstance(openaiadapter.OpenAIInstanceParams{
		Name:      "openai",
		APIKey:    "sk-test",
		StateHome: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewForInstance: %v", err)
	}
	client := llm.NewClient()
	client.Register(adapter)
	return client
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
