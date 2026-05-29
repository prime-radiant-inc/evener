package diagnostic

import (
	"testing"

	"primeradiant.com/serf/llm"
)

func TestClassifyUnknownProviderAsSerfConfiguration(t *testing.T) {
	info := Classify("configuration error: unknown provider: openrouter")
	if info.Source != SourceSerf {
		t.Fatalf("Source=%q, want %q", info.Source, SourceSerf)
	}
	if info.Title != "Serf configuration error" {
		t.Fatalf("Title=%q", info.Title)
	}
	if info.Hint == "" {
		t.Fatal("expected launch/config hint")
	}
}

func TestClassifyProviderHTTPFailureAsProvider(t *testing.T) {
	info := Classify("openai error (status=401): invalid API key")
	if info.Source != SourceProvider {
		t.Fatalf("Source=%q, want %q", info.Source, SourceProvider)
	}
	if info.Title != "Provider error" {
		t.Fatalf("Title=%q", info.Title)
	}
}

func TestClassifySpawnFailureAsHub(t *testing.T) {
	info := Classify("daemon spawn timed out: process exited before rendezvous")
	if info.Source != SourceHub {
		t.Fatalf("Source=%q, want %q", info.Source, SourceHub)
	}
}

// --- Structured llm.Error classification tests (PRI-1880) ---

// TestFromError_StructuredLLMError_IsProvider verifies that FromError classifies
// a structured llm.Error with a non-empty provider as SourceProvider, regardless
// of whether the provider name appears in the hardcoded list.
func TestFromError_StructuredLLMError_IsProvider(t *testing.T) {
	// "work" is not in any hardcoded list, but it is a structured llm.Error.
	err := llm.ErrorFromHTTPStatus("work", 429, "rate limited", nil, nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(llm.Error with provider='work'): Source=%q, want %q", info.Source, SourceProvider)
	}
}

// TestFromError_StructuredLLMError_RenamedInstance_IsProvider verifies that
// an instance named "my-gpt" still classifies as a provider failure via
// FromError even though the string "my-gpt" would not match any keyword.
func TestFromError_StructuredLLMError_RenamedInstance_IsProvider(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("my-gpt", 500, "server error", nil, nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(llm.Error with provider='my-gpt'): Source=%q, want %q", info.Source, SourceProvider)
	}
}

func TestClassifyStreamTruncationAsProvider(t *testing.T) {
	cases := []string{
		"stream ended without finish event",
		"stream ended without response",
		"stream error",
		"missing response in finish event",
	}
	for _, msg := range cases {
		info := Classify(msg)
		if info.Source != SourceProvider {
			t.Errorf("Classify(%q): Source=%q, want %q", msg, info.Source, SourceProvider)
		}
		if info.Title != "Provider error" {
			t.Errorf("Classify(%q): Title=%q, want Provider error", msg, info.Title)
		}
	}
}
