package llm

import "testing"

func TestGetLatestModel_CapabilityFilters(t *testing.T) {
	cat := &ModelCatalog{Models: []ModelInfo{
		{ID: "a", Provider: "test", ContextWindow: 1000, SupportsTools: true},
		{ID: "b", Provider: "test", ContextWindow: 2000, SupportsTools: true, SupportsVision: true},
		{ID: "c", Provider: "test", ContextWindow: 2000, SupportsReasoning: true},
		{ID: "z", Provider: "other", ContextWindow: 9000, SupportsTools: true},
	}}

	tests := []struct {
		name       string
		provider   string
		capability string
		wantID     string // "" means expect nil
	}{
		{"any capability picks largest window, lexical tie-break", "test", "", "c"},
		{"tools filter", "test", "tools", "b"},
		{"vision filter", "test", "vision", "b"},
		{"reasoning filter", "test", "reasoning", "c"},
		{"unknown capability yields nil", "test", "bogus", ""},
		{"unknown provider yields nil", "nope", "", ""},
		{"provider filter isolates other", "other", "tools", "z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cat.GetLatestModel(tt.provider, tt.capability)
			if tt.wantID == "" {
				if got != nil {
					t.Fatalf("GetLatestModel = %q, want nil", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("GetLatestModel = nil, want %q", tt.wantID)
			}
			if got.ID != tt.wantID {
				t.Fatalf("GetLatestModel = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestGetLatestModel_NilCatalog(t *testing.T) {
	var cat *ModelCatalog
	if got := cat.GetLatestModel("test", ""); got != nil {
		t.Fatalf("nil catalog GetLatestModel = %v, want nil", got)
	}
}
