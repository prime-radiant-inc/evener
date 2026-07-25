package diagnostic

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func usageLimitError(t *testing.T) error {
	t.Helper()
	var raw map[string]any
	body := `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"pro","resets_at":1785258150}}`
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return llm.ErrorFromHTTPStatus("openai", 429, llm.ProviderFailureMessage("responses.create(stream)", []byte(body)), raw, nil)
}

// An exhausted allowance is not the same problem as a generic provider failure:
// retrying will not help, so the guidance must be to wait or switch models.
func TestFromErrorUsageLimit(t *testing.T) {
	info := FromError(usageLimitError(t))

	if info.Source != SourceProvider {
		t.Errorf("Source = %q, want %q", info.Source, SourceProvider)
	}
	if info.Title != usageLimitTitle {
		t.Errorf("Title = %q, want %q", info.Title, usageLimitTitle)
	}
	for _, want := range []string{"switch", "reset"} {
		if !strings.Contains(strings.ToLower(info.Hint), want) {
			t.Errorf("Hint %q does not mention %q", info.Hint, want)
		}
	}
	if strings.Contains(strings.ToLower(info.Hint), "retry") {
		t.Errorf("Hint tells the user to retry a spent allowance: %q", info.Hint)
	}
}

// The projection path re-derives Info from stored strings, so a usage limit must
// classify the same whether the error object or only its message is available.
func TestClassifyUsageLimitMessage(t *testing.T) {
	msg := usageLimitError(t).Error()
	if got := Classify(msg).Title; got != usageLimitTitle {
		t.Fatalf("Classify(%q).Title = %q, want %q", msg, got, usageLimitTitle)
	}
	if got := FromFields("provider", "", "", msg).Title; got != usageLimitTitle {
		t.Fatalf("FromFields Title = %q, want %q", got, usageLimitTitle)
	}
}

// An explicit title from the emitter still wins; the usage-limit default only
// fills a gap.
func TestFromFieldsUsageLimitRespectsExplicitTitle(t *testing.T) {
	msg := usageLimitError(t).Error()
	if got := FromFields("provider", "Custom title", "", msg).Title; got != "Custom title" {
		t.Fatalf("Title = %q, want the explicit title", got)
	}
}

// A transient rate limit keeps the generic provider guidance, which correctly
// tells the user that retrying may help.
func TestPlainRateLimitIsNotAUsageLimit(t *testing.T) {
	info := Classify("openai error (status=429): chat.completions(stream) failed: Slow down")
	if info.Title == usageLimitTitle {
		t.Fatalf("a transient rate limit was classified as an exhausted usage limit: %+v", info)
	}
	if info.Source != SourceProvider {
		t.Errorf("Source = %q, want %q", info.Source, SourceProvider)
	}
}
