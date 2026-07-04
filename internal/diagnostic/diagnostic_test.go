package diagnostic

import (
	"errors"
	"testing"

	"primeradiant.com/serf/llm"
)

// F3: each isSerfConfiguration keyword is tested independently so removing
// either one would leave a failing test.
func TestClassifyUnknownProviderAsSerfConfiguration(t *testing.T) {
	cases := []struct{ name, msg string }{
		{"unknown provider keyword only", "unknown provider: foo"},
		{"configuration error keyword only", "configuration error: bad value"},
	}
	for _, tc := range cases {
		info := Classify(tc.msg)
		if info.Source != SourceSerf {
			t.Errorf("%s: Source=%q, want %q", tc.name, info.Source, SourceSerf)
		}
		if info.Title != "Serf configuration error" {
			t.Errorf("%s: Title=%q, want Serf configuration error", tc.name, info.Title)
		}
		if info.Hint == "" {
			t.Errorf("%s: expected launch/config hint", tc.name)
		}
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

// F4: assert Title; test each isHubFailure keyword independently so removing
// any single keyword would leave a failing sub-case.
func TestClassifySpawnFailureAsHub(t *testing.T) {
	cases := []struct{ name, msg string }{
		{"daemon spawn keyword", "daemon spawn failed"},
		{"rendezvous keyword", "process timed out at rendezvous"},
		{"appwire keyword", "appwire connection refused"},
	}
	for _, tc := range cases {
		info := Classify(tc.msg)
		if info.Source != SourceHub {
			t.Errorf("%s: Source=%q, want %q", tc.name, info.Source, SourceHub)
		}
		if info.Title != "Hub error" {
			t.Errorf("%s: Title=%q, want Hub error", tc.name, info.Title)
		}
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

// F2+F5: replace the near-duplicate renamed-instance test with a case that
// exercises the Provider() arm of the OR condition in isolation (StatusCode==0),
// so the `||` cannot be mutated to `&&` without a test failing.
func TestFromError_ProviderOnlyNoStatusCode_IsProvider(t *testing.T) {
	// NewRequestTimeoutError produces a non-HTTP error: Provider() is non-empty
	// but StatusCode()==0 and ErrorCode()=="".  This exercises the first OR arm.
	err := llm.NewRequestTimeoutError("mymodel", "context deadline exceeded", nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(NewRequestTimeoutError): Source=%q, want %q", info.Source, SourceProvider)
	}
}

// F2: exercise the ErrorCode() arm of the OR condition in isolation
// (Provider()=="" and StatusCode()==0), so the `||` cannot be mutated to `&&`
// without this test failing.
func TestFromError_ErrorCodeOnlyNoProviderNoStatus_IsProvider(t *testing.T) {
	raw := map[string]any{"error": map[string]any{"code": "rate_limit_exceeded"}}
	err := llm.ErrorFromHTTPStatus("", 0, "some error", raw, nil)
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(llm.Error with only ErrorCode): Source=%q, want %q", info.Source, SourceProvider)
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

// --- FromError coverage (F1) ---

func TestFromError_Nil_IsSerfFailure(t *testing.T) {
	info := FromError(nil)
	if info.Source != SourceSerf {
		t.Fatalf("FromError(nil): Source=%q, want %q", info.Source, SourceSerf)
	}
	if info.Title != "Serf error" {
		t.Fatalf("FromError(nil): Title=%q, want Serf error", info.Title)
	}
}

func TestFromError_ConfigurationError_IsSerfConfiguration(t *testing.T) {
	err := &llm.ConfigurationError{Message: "bad provider"}
	info := FromError(err)
	if info.Source != SourceSerf {
		t.Fatalf("FromError(ConfigurationError): Source=%q, want %q", info.Source, SourceSerf)
	}
	if info.Title != "Serf configuration error" {
		t.Fatalf("FromError(ConfigurationError): Title=%q, want Serf configuration error", info.Title)
	}
}

func TestFromError_PlainError_FallsThroughToClassify(t *testing.T) {
	// A plain error (not llm.Error) falls through to Classify.
	// "api key" in the message triggers providerFailure via isProviderFailure.
	err := errors.New("openai: api key missing")
	info := FromError(err)
	if info.Source != SourceProvider {
		t.Fatalf("FromError(plain error): Source=%q, want %q", info.Source, SourceProvider)
	}
}

// --- FromFields coverage (F1) ---

// TestFromFields_SourceOverridesClassify verifies that a recognised source
// string forces the returned Info.Source regardless of what Classify would
// pick from the (empty) message.
func TestFromFields_SourceOverridesClassify(t *testing.T) {
	cases := []struct {
		source string
		want   Source
	}{
		{"provider", SourceProvider},
		{"hub", SourceHub},
		{"ui", SourceUI},
		{"serf", SourceSerf},
	}
	for _, tc := range cases {
		info := FromFields(tc.source, "", "", "")
		if info.Source != tc.want {
			t.Errorf("FromFields(%q,...): Source=%q, want %q", tc.source, info.Source, tc.want)
		}
	}
}

// TestFromFields_UnknownSourceFallsBackToClassify verifies that an unrecognised
// source string falls back to Classify on the message.
func TestFromFields_UnknownSourceFallsBackToClassify(t *testing.T) {
	// "api key" in the message triggers providerFailure via Classify.
	info := FromFields("unknown", "", "", "openai: api key missing")
	if info.Source != SourceProvider {
		t.Fatalf("FromFields(unknown source): Source=%q, want %q", info.Source, SourceProvider)
	}
}

// TestFromFields_TitleAndHintOverride verifies that non-empty title and hint
// arguments overwrite the default values produced by the source lookup.
func TestFromFields_TitleAndHintOverride(t *testing.T) {
	info := FromFields("provider", "My Title", "My Hint", "")
	if info.Source != SourceProvider {
		t.Fatalf("Source=%q, want %q", info.Source, SourceProvider)
	}
	if info.Title != "My Title" {
		t.Fatalf("Title=%q, want My Title", info.Title)
	}
	if info.Hint != "My Hint" {
		t.Fatalf("Hint=%q, want My Hint", info.Hint)
	}
}

// TestFromFields_SourceUI_DefaultTitle verifies the SourceUI branch of
// defaultForSource, which returns a distinct Info struct (not serfFailure).
func TestFromFields_SourceUI_DefaultTitle(t *testing.T) {
	info := FromFields("ui", "", "", "")
	if info.Source != SourceUI {
		t.Fatalf("Source=%q, want %q", info.Source, SourceUI)
	}
	if info.Title != "UI error" {
		t.Fatalf("Title=%q, want UI error", info.Title)
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
