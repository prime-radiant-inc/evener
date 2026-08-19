package llm

import "testing"

// ================== Opus 4.7/4.8 catalog request shape (issue #169) ==================
//
// Opus 4.7 and 4.8 reject temperature/top_p like Claude 5+ models, so their
// catalog entries must carry Claude5RequestShape=true (the Anthropic request
// builder keys request shaping on this flag whenever an entry resolves).
// 4.6 is old-shape — only 4.7+ changed — and stays false as a regression guard.

func TestEmbeddedModelCatalog_Opus47RequestShape(t *testing.T) {
	cat := EmbeddedModelCatalog()
	if cat == nil {
		t.Fatal("embedded catalog nil")
	}
	for _, id := range []string{"claude-opus-4-7", "claude-opus-4-8"} {
		mi := cat.GetModelInfo(id)
		if mi == nil {
			t.Fatalf("%s not found in embedded catalog", id)
		}
		if !mi.Claude5RequestShape {
			t.Errorf("%s Claude5RequestShape = false, want true", id)
		}
	}
	// Regression guard: 4.6 is old-shape.
	mi := cat.GetModelInfo("claude-opus-4-6")
	if mi == nil {
		t.Fatal("claude-opus-4-6 not found in embedded catalog")
	}
	if mi.Claude5RequestShape {
		t.Errorf("claude-opus-4-6 Claude5RequestShape = true, want false (old-shape)")
	}
}
