package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
// guard.
func TestValidateModelSwitchMembership_NilClient(t *testing.T) {
	if err := validateModelSwitchMembership(nil, nil, llm.ModelListing{}); err != nil {
		t.Fatalf("expected nil for nil client, got %v", err)
	}
}

// TestFormatModelAlternatives_Duplicate covers the dedupe branch: a listing
// that names the same id twice reports it once.
func TestFormatModelAlternatives_Duplicate(t *testing.T) {
	models := []registry.Resolved{
		{ModelID: "model-a"},
		{ModelID: "model-a"}, // duplicate
	}
	got := formatModelAlternatives(models)
	if !strings.Contains(got, "model-a") {
		t.Fatalf("expected model-a in output, got %q", got)
	}
	// Should only appear once.
	if strings.Count(got, "model-a") != 1 {
		t.Fatalf("expected model-a once, got %q", got)
	}
}

// TestFormatModelAlternatives_MoreThanMax covers the truncation branch when
// there are more than maxModelAlternatives rows.
func TestFormatModelAlternatives_MoreThanMax(t *testing.T) {
	// Generate more than maxModelAlternatives unique model IDs.
	models := make([]registry.Resolved, maxModelAlternatives+5)
	for i := range models {
		models[i] = registry.Resolved{ModelID: "model-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))}
	}
	got := formatModelAlternatives(models)
	if !strings.Contains(got, "+5 more") {
		t.Fatalf("expected '+5 more' suffix, got %q", got)
	}
}

// TestFormatModelAlternatives_Empty covers the empty case.
func TestFormatModelAlternatives_Empty(t *testing.T) {
	got := formatModelAlternatives(nil)
	if got != "none" {
		t.Fatalf("expected 'none', got %q", got)
	}
}

// TestValidateModelSwitchMembership_ProfileNil covers the case where
// only profile is nil.
func TestValidateModelSwitchMembership_ProfileNil(t *testing.T) {
	client := &llm.Client{}
	if err := validateModelSwitchMembership(client, nil, llm.ModelListing{}); err != nil {
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
