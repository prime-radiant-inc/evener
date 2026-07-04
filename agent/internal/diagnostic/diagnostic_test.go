package diagnostic

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// D2 fix: use a message that contains only the "unknown provider" keyword so
// removing that single branch from isSerfConfiguration would flip the result.
func TestClassifyUnknownProviderAsSerfConfiguration(t *testing.T) {
	info := Classify("unknown provider: openrouter")
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

// D3 fix: per-keyword cases so deleting any single arm of isHubFailure breaks
// exactly the case that names it. D5 fix: assert Title as well.
func TestClassifySpawnFailureAsHub(t *testing.T) {
	cases := []string{
		"rendezvous failed",
		"daemon spawn timed out",
		"resume timed out after 30s",
		"appwire dropped",
		"websocket closed",
		"stream failed to connect",
		"source not found: xyz",
	}
	for _, msg := range cases {
		info := Classify(msg)
		if info.Source != SourceHub {
			t.Errorf("Classify(%q): Source=%q, want %q", msg, info.Source, SourceHub)
		}
		if info.Title != "Hub error" {
			t.Errorf("Classify(%q): Title=%q, want Hub error", msg, info.Title)
		}
	}
}

// --- Structured llm.Error classification tests (PRI-1880) ---

// TestFromError_StructuredLLMError_IsProvider verifies that FromError classifies
// a structured llm.Error with a non-empty provider as SourceProvider via the
// structured-error fast path, not keyword matching. The message "internal server
// error" matches no keyword in isProviderFailure, so the structured path is the
// only route to SourceProvider. D1 + D5 fix.
func TestFromError_StructuredLLMError_IsProvider(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("work", 500, "internal server error", nil, nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(llm.Error with provider='work'): Source=%q, want %q", info.Source, SourceProvider)
	}
	if info.Title != "Provider error" {
		t.Fatalf("FromError(llm.Error with provider='work'): Title=%q, want Provider error", info.Title)
	}
}

// TestFromError_StructuredLLMError_RenamedInstance_IsProvider verifies that an
// instance named "my-gpt" classifies as SourceProvider via FromError even though
// "my-gpt" matches no keyword and "server error" matches no isProviderFailure
// keyword. D5 fix: assert Title.
func TestFromError_StructuredLLMError_RenamedInstance_IsProvider(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("my-gpt", 500, "server error", nil, nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(llm.Error with provider='my-gpt'): Source=%q, want %q", info.Source, SourceProvider)
	}
	if info.Title != "Provider error" {
		t.Fatalf("FromError(llm.Error with provider='my-gpt'): Title=%q, want Provider error", info.Title)
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

// D5 fix: assert Title so a change to defaultForSource(SourceHook).Title is caught.
func TestFromFields_HookSourcePreserved(t *testing.T) {
	info := FromFields("hook", "", "", "rate limit exceeded")
	if info.Source != SourceHook {
		t.Fatalf("Source = %q, want hook (must not be reclassified by message content)", info.Source)
	}
	if info.Title != "Hook message" {
		t.Fatalf("Title = %q, want Hook message", info.Title)
	}
}

func TestFromFields_MCPSource_GetsMCPHints(t *testing.T) {
	// A connection-refused MCP failure classifies as MCP, not the generic serf hint.
	got := FromFields("mcp", "", "", "MCP server \"linear\" failed to connect: connection refused")
	if got.Source != SourceMCP {
		t.Fatalf("Source=%q, want %q", got.Source, SourceMCP)
	}
}

func TestFromFields_MCP401_DoesNotMatchProvider(t *testing.T) {
	// An MCP auth failure carrying "unauthorized" must NOT read as a provider-credential error.
	got := FromFields("mcp", "", "", "MCP server \"linear\" failed to connect: 401 unauthorized")
	if got.Source != SourceMCP {
		t.Fatalf("MCP 401 misclassified: Source=%q, want %q", got.Source, SourceMCP)
	}
}
