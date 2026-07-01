package agent

import (
	"strings"
	"testing"
)

func TestS3Cov_NormalizeNodeOutput(t *testing.T) {
	t.Parallel()

	t.Run("nil yields empty envelope", func(t *testing.T) {
		t.Parallel()
		out := normalizeNodeOutput(nil)
		if out.Message != "" || out.Data == nil || out.Artifacts == nil {
			t.Fatalf("unexpected: %+v", out)
		}
	})

	t.Run("typed passthrough fills nils", func(t *testing.T) {
		t.Parallel()
		out := normalizeNodeOutput(nodeOutput{Decision: "go"})
		if out.Decision != "go" || out.Data == nil || out.Artifacts == nil {
			t.Fatalf("unexpected: %+v", out)
		}
	})

	t.Run("map with all fields", func(t *testing.T) {
		t.Parallel()
		out := normalizeNodeOutput(map[string]any{
			"decision":  "approve",
			"message":   "looks good",
			"data":      map[string]any{"k": "v"},
			"artifacts": []any{"a.txt", 42},
		})
		if out.Decision != "approve" || out.Message != "looks good" {
			t.Fatalf("decision/message wrong: %+v", out)
		}
		if len(out.Data) != 1 || len(out.Artifacts) != 2 || out.Artifacts[1] != "42" {
			t.Fatalf("data/artifacts wrong: %+v", out)
		}
	})

	t.Run("message non-string coerced", func(t *testing.T) {
		t.Parallel()
		out := normalizeNodeOutput(map[string]any{"message": 99, "artifacts": []string{"x"}})
		if out.Message != "99" || len(out.Artifacts) != 1 {
			t.Fatalf("unexpected: %+v", out)
		}
	})

	t.Run("non-map non-typed yields default", func(t *testing.T) {
		t.Parallel()
		out := normalizeNodeOutput("plain string")
		if out.Message != "" || len(out.Data) != 0 {
			t.Fatalf("unexpected: %+v", out)
		}
	})
}

func TestS3Cov_HasMeaningfulNodeOutput(t *testing.T) {
	t.Parallel()
	if hasMeaningfulNodeOutput(nodeOutput{}) {
		t.Fatal("empty output should not be meaningful")
	}
	if !hasMeaningfulNodeOutput(nodeOutput{Message: "hi"}) {
		t.Fatal("message should be meaningful")
	}
	if !hasMeaningfulNodeOutput(nodeOutput{Data: map[string]any{"k": 1}}) {
		t.Fatal("data should be meaningful")
	}
}

func TestS3Cov_HasMeaningfulRawOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  any
		want bool
	}{
		{"nil", nil, false},
		{"non-map is meaningful", "hello", true},
		{"empty map", map[string]any{}, false},
		{"decision set", map[string]any{"decision": "go"}, true},
		{"blank message ignored", map[string]any{"message": "   "}, false},
		{"empty data ignored", map[string]any{"data": map[string]any{}}, false},
		{"non-empty data", map[string]any{"data": map[string]any{"k": 1}}, true},
		{"empty artifacts ignored", map[string]any{"artifacts": []any{}}, false},
		{"non-empty artifacts", map[string]any{"artifacts": []string{"x"}}, true},
		{"unknown key", map[string]any{"weird": 1}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasMeaningfulRawOutput(tc.raw); got != tc.want {
				t.Fatalf("hasMeaningfulRawOutput(%v) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestS3Cov_CommunicateSchemaStringSlice(t *testing.T) {
	t.Parallel()
	if got := communicateSchemaStringSlice([]string{"a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("[]string case: %v", got)
	}
	if got := communicateSchemaStringSlice([]any{"a", 1, "b"}); len(got) != 2 || got[1] != "b" {
		t.Fatalf("[]any case: %v", got)
	}
	if got := communicateSchemaStringSlice(42); got != nil {
		t.Fatalf("default case: %v", got)
	}
}

func TestS3Cov_CommunicateSchemaContains(t *testing.T) {
	t.Parallel()
	if !communicateSchemaContains([]string{"x", "y"}, "y") {
		t.Fatal("expected contains")
	}
	if communicateSchemaContains([]string{"x"}, "z") {
		t.Fatal("expected not contains")
	}
}

func TestS3Cov_CanonicalNodeOutputText(t *testing.T) {
	t.Parallel()
	got := canonicalNodeOutputText(map[string]any{"message": "hi"})
	if !strings.Contains(got, `"message":"hi"`) {
		t.Fatalf("canonical text = %q", got)
	}
	// nil normalizes to the empty envelope, still valid JSON.
	if got := canonicalNodeOutputText(nil); !strings.HasPrefix(got, "{") {
		t.Fatalf("nil canonical text = %q", got)
	}
}
