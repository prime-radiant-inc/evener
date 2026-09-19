package research

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractAtifMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.atif.json")
	content := `{
  "schema_version": "1.7",
  "session_id": "s1",
  "steps": [
    {"step_id": 1, "source": "model", "metrics": {"prompt_tokens": 100, "completion_tokens": 20, "cached_tokens": 10}},
    {"step_id": 2, "source": "tool", "metrics": null},
    {"step_id": 3, "source": "model", "metrics": {"prompt_tokens": 200, "completion_tokens": 30, "cached_tokens": 0}}
  ],
  "final_metrics": {"total_prompt_tokens": 300, "total_completion_tokens": 50, "total_cached_tokens": 10, "total_steps": 3}
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ExtractAtifMetrics(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.PromptTokens != 300 || m.CompletionTokens != 50 || m.CachedTokens != 10 {
		t.Fatalf("totals wrong: %+v", m)
	}
	if m.Steps != 3 {
		t.Fatalf("steps = %d, want 3", m.Steps)
	}
	if m.ModelRequests != 2 {
		t.Fatalf("model requests = %d, want 2 (steps with metrics)", m.ModelRequests)
	}
}

func TestExtractAtifMetrics_MissingFileIsError(t *testing.T) {
	if _, err := ExtractAtifMetrics(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file must error")
	}
}
