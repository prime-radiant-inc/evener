package providerfwd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
)

// Concrete embedding must promote the optional ModelLister capability from the
// backing adapter onto both forwarders. These assertions fail to compile if a
// forwarder ever loses that promotion (e.g. by switching to an interface
// embed).
var (
	_ llm.ProviderAdapter = (*OpenAICompat)(nil)
	_ llm.ModelLister     = (*OpenAICompat)(nil)
	_ llm.ProviderAdapter = (*Anthropic)(nil)
	_ llm.ModelLister     = (*Anthropic)(nil)
)

func TestOpenAICompat_Name(t *testing.T) {
	// Instance name wins when set.
	if got := NewOpenAICompat("inst", "deflt", nil).Name(); got != "inst" {
		t.Fatalf("Name() = %q, want inst", got)
	}
	// Falls back to the provider-type default when the instance name is empty.
	if got := NewOpenAICompat("", "deflt", nil).Name(); got != "deflt" {
		t.Fatalf("Name() = %q, want deflt", got)
	}
}

func TestAnthropic_Name(t *testing.T) {
	if got := NewAnthropic("inst", "deflt", nil).Name(); got != "inst" {
		t.Fatalf("Name() = %q, want inst", got)
	}
	if got := NewAnthropic("", "deflt", nil).Name(); got != "deflt" {
		t.Fatalf("Name() = %q, want deflt", got)
	}
}

func TestAnthropic_CountInputTokensFallsBackWhenForwarderDoesNotEnableIt(t *testing.T) {
	assertLocalEstimate := func(t *testing.T, got llm.InputTokenCount, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("CountInputTokens: %v", err)
		}
		if got.Exact {
			t.Fatalf("fallback estimate should not be exact: %+v", got)
		}
		if got.Source != llm.TokenCountSourceLocalEstimate {
			t.Fatalf("Source = %q, want %q", got.Source, llm.TokenCountSourceLocalEstimate)
		}
	}

	t.Run("nil adapter", func(t *testing.T) {
		c := llm.NewClient()
		c.Register(NewAnthropic("", "anthropic-proxy", nil))
		got, err := c.CountInputTokens(context.Background(), llm.Request{
			Provider: "anthropic-proxy",
			Model:    "claude-test",
			Messages: []llm.Message{llm.User("hello world")},
		})
		assertLocalEstimate(t, got, err)
	})

	// Pin the countInputTokens=false gate independently of the nil-adapter branch.
	// With a real adapter present, the flag must still suppress forwarding and
	// the backing server must receive zero requests.
	t.Run("non-nil adapter without opt-in", func(t *testing.T) {
		var reqs int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqs++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"input_tokens":99}`))
		}))
		t.Cleanup(srv.Close)

		c := llm.NewClient()
		c.Register(NewAnthropic("", "anthropic-proxy", &anthropic.Adapter{
			APIKey:  "k",
			BaseURL: srv.URL,
			Client:  srv.Client(),
		}))
		got, err := c.CountInputTokens(context.Background(), llm.Request{
			Provider: "anthropic-proxy",
			Model:    "claude-test",
			Messages: []llm.Message{llm.User("hello world")},
		})
		assertLocalEstimate(t, got, err)
		if reqs != 0 {
			t.Fatalf("server received %d requests, want 0: countInputTokens=false must block forwarding", reqs)
		}
	})
}

func TestAnthropic_CountInputTokensOptInForwardsAndPreservesName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Fatalf("path = %q, want /v1/messages/count_tokens", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	t.Cleanup(srv.Close)

	a := NewAnthropicWithInputTokenCounter("", "anthropic-proxy", &anthropic.Adapter{
		APIKey:  "k",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})
	got, err := a.CountInputTokens(context.Background(), llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hello")},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens != 42 || !got.Exact || got.Source != llm.TokenCountSourceProvider {
		t.Fatalf("CountInputTokens = %+v, want exact provider count", got)
	}
	if got.Provider != "anthropic-proxy" {
		t.Fatalf("Provider = %q, want anthropic-proxy", got.Provider)
	}
}
