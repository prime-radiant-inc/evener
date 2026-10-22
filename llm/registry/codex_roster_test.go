package registry

import (
	"slices"
	"testing"
)

// TestCodexValidRowIDsIncludesLiveRoster pins the data behind the Codex
// refusal's valid-model list: the bundled exact rows plus the ids this
// process listed live for that instance, and nothing from another instance.
func TestCodexValidRowIDsIncludesLiveRoster(t *testing.T) {
	r := embeddedLoad(t)
	r.ApplyLive("openai-codex", []Model{{ID: "gpt-reserve"}})

	rec, ok := r.recordFor("openai-codex")
	if !ok {
		t.Fatal("embedded registry has no openai-codex record")
	}
	got := r.codexValidRowIDs(rec)
	for _, want := range []string{"gpt-reserve", "gpt-5.6"} {
		if !slices.Contains(got, want) {
			t.Errorf("codexValidRowIDs = %v, want %q", got, want)
		}
	}

	r.ApplyLive("openai", []Model{{ID: "live-openai-only"}})
	if slices.Contains(r.codexValidRowIDs(rec), "live-openai-only") {
		t.Fatalf("codexValidRowIDs leaked another instance's live id: %v", r.codexValidRowIDs(rec))
	}
}
