package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestUnrepresentableHistoryKinds_UnknownProtocol covers the early return
// when unrepresentableContentKinds returns nil for a protocol with no
// restricted kinds (session_set_model.go:46-47).
func TestUnrepresentableHistoryKinds_UnknownProtocol(t *testing.T) {
	history := newSetModelHistoryWithContent(llm.ContentDocument)
	got := unrepresentableHistoryKinds(history, "unknown-protocol")
	if got != nil {
		t.Fatalf("expected nil for an unknown protocol, got %v", got)
	}
}

// TestValidateModelSwitchMembership_NilClient covers the nil-client/profile
// guard (session_set_model.go:113-114).
func TestValidateModelSwitchMembership_NilClient(t *testing.T) {
	if err := validateModelSwitchMembership(nil, nil, nil); err != nil {
		t.Fatalf("expected nil for nil client, got %v", err)
	}
}

// TestFormatModelAlternatives_DuplicateAndInvisible covers the skip
// branch (session_set_model.go:147-148) for duplicate and invisible models.
func TestFormatModelAlternatives_DuplicateAndInvisible(t *testing.T) {
	tag := "anthropic"
	cat := llm.EmbeddedModelCatalog()
	// Two models with the same ID — the second should be skipped.
	models := []llm.ModelInfo{
		{ID: "model-a"},
		{ID: "model-a"}, // duplicate
	}
	got := formatModelAlternatives(models, tag, cat)
	if !strings.Contains(got, "model-a") {
		t.Fatalf("expected model-a in output, got %q", got)
	}
	// Should only appear once.
	if strings.Count(got, "model-a") != 1 {
		t.Fatalf("expected model-a once, got %q", got)
	}
}

// TestFormatModelAlternatives_MoreThanMax covers the truncation branch
// (session_set_model.go:157-159) when there are more than maxModelAlternatives
// visible models.
func TestFormatModelAlternatives_MoreThanMax(t *testing.T) {
	tag := "anthropic"
	cat := llm.EmbeddedModelCatalog()
	// Generate more than maxModelAlternatives unique model IDs.
	models := make([]llm.ModelInfo, maxModelAlternatives+5)
	for i := range models {
		models[i] = llm.ModelInfo{ID: "model-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))}
	}
	got := formatModelAlternatives(models, tag, cat)
	if !strings.Contains(got, "+5 more") {
		t.Fatalf("expected '+5 more' suffix, got %q", got)
	}
}

// TestFormatModelAlternatives_NoneVisible covers the empty case
// (session_set_model.go:154-155).
func TestFormatModelAlternatives_NoneVisible(t *testing.T) {
	tag := "anthropic"
	cat := llm.EmbeddedModelCatalog()
	// Pass an empty model list.
	got := formatModelAlternatives(nil, tag, cat)
	if got != "none" {
		t.Fatalf("expected 'none', got %q", got)
	}
}

// TestValidateModelSwitchMembership_ProfileNil covers the case where
// only profile is nil (session_set_model.go:113-114).
func TestValidateModelSwitchMembership_ProfileNil(t *testing.T) {
	client := &llm.Client{}
	if err := validateModelSwitchMembership(client, nil, nil); err != nil {
		t.Fatalf("expected nil for nil profile, got %v", err)
	}
}

// Helper: create a history with a specific content kind.
func newSetModelHistoryWithContent(kind llm.ContentKind) []schema.Turn {
	return []schema.Turn{
		{
			Message: llm.Message{
				Role: llm.RoleUser,
				Content: []llm.ContentPart{
					{Kind: kind, Text: "test"},
				},
			},
		},
	}
}
