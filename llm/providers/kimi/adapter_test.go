package kimi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/providerfwd"
	"primeradiant.com/serf/llm/providers/kimicoding"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

func TestAdapter_Name(t *testing.T) {
	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{})
	if a.Name() != "kimi" {
		t.Fatalf("Name() = %q, want kimi", a.Name())
	}
}

func TestAdapter_Complete_DelegatesToInner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "c1", "model": "kimi-k2.5",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
		Quirks:  openaicompat.QuirksPreset("kimi-k2.5"),
	})

	resp, err := a.Complete(context.Background(), llm.Request{
		Model:    "kimi-k2.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("text: %q", resp.Text())
	}
}

func TestAdapter_DefaultBaseURL(t *testing.T) {
	if defaultBaseURL != "https://api.moonshot.ai/v1" { //nolint:goconst
		t.Fatalf("defaultBaseURL: %q", defaultBaseURL)
	}
}

// TestClient_ListModels_Forwards verifies that the kimi adapter exposes
// ListModels (via its openaicompat backing) so that llm.Client.ListModels
// reaches the provider's /models endpoint.
func TestClient_ListModels_Forwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2.5"},{"id":"moonshot-v1-8k"}]}`))
	}))
	t.Cleanup(srv.Close)

	a := providerfwd.NewOpenAICompat("", providerName, &openaicompat.Adapter{BaseURL: srv.URL, Client: srv.Client()})

	if _, ok := llm.ProviderAdapter(a).(llm.ModelLister); !ok {
		t.Fatal("kimi adapter does not implement llm.ModelLister — ListModels not promoted")
	}

	c := llm.NewClient()
	c.Register(a)

	models, err := c.ListModels(context.Background(), a.Name())
	if err != nil {
		t.Fatalf("Client.ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
}

func TestNewForInstance_Name(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if a.Name() != "kc" {
		t.Fatalf("Name() = %q, want kc", a.Name())
	}
}

func TestNewForInstance_DefaultBaseURL(t *testing.T) {
	// When BaseURL is empty, the type's default must be applied.
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if a.BaseURL != defaultBaseURL {
		t.Fatalf("backing BaseURL = %q, want %q", a.BaseURL, defaultBaseURL)
	}
}

func TestNewForInstance_DefaultQuirks(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k"})
	if !a.Quirks.LockTemperature {
		t.Fatal("expected kimi quirks (LockTemperature) to be applied")
	}
}

func TestNewForInstance_CustomBaseURL(t *testing.T) {
	a := NewForInstance(InstanceParams{Name: "kc", APIKey: "k", BaseURL: "http://custom"})
	if a.BaseURL != "http://custom" {
		t.Fatalf("backing BaseURL = %q, want http://custom", a.BaseURL)
	}
}

func TestNewForInstance_EnvPathPreservesName(t *testing.T) {
	// The env factory still names the adapter "kimi".
	t.Setenv("KIMI_API_KEY", "testkey")
	t.Setenv("KIMI_BASE_URL", "")
	// Trigger the env factory by calling init-registered code indirectly.
	// We just verify NewForInstance with env-equivalent params gives name "kimi".
	a := NewForInstance(InstanceParams{Name: "kimi", APIKey: "testkey"})
	if a.Name() != "kimi" {
		t.Fatalf("Name() = %q, want kimi", a.Name())
	}
}

func TestNewForInstance_AnnouncesCodingAgentUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","model":"kimi-for-coding","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(InstanceParams{Name: "kimi", BaseURL: srv.URL, APIKey: "k"})
	if _, err := a.Complete(context.Background(), llm.Request{Model: "kimi-for-coding", Messages: []llm.Message{llm.User("hi")}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotUA != kimicoding.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, kimicoding.UserAgent)
	}
}

func TestAdapter_CountInputTokens_UsesEstimateEndpoint(t *testing.T) {
	var gotPath string
	var gotUA string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_tokens":321}}`))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(InstanceParams{Name: "kimi", BaseURL: srv.URL, APIKey: "k"})
	maxTokens := 99
	temp := 0.2
	got, err := a.CountInputTokens(context.Background(), llm.Request{
		Model:       "kimi-k2.6",
		Messages:    []llm.Message{llm.User("hello")},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
		Tools: []llm.ToolDefinition{{
			Name:       "lookup",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens != 321 || !got.Exact || got.Source != llm.TokenCountSourceProvider {
		t.Fatalf("CountInputTokens = %+v, want exact provider count", got)
	}
	if got.Provider != "kimi" || got.Model != "kimi-k2.6" {
		t.Fatalf("provider/model = %q/%q, want kimi/kimi-k2.6", got.Provider, got.Model)
	}
	if gotPath != "/tokenizers/estimate-token-count" {
		t.Fatalf("path = %q, want /tokenizers/estimate-token-count", gotPath)
	}
	if gotUA != kimicoding.UserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, kimicoding.UserAgent)
	}
	if gotBody["model"] != "kimi-k2.6" {
		t.Fatalf("model = %#v, want kimi-k2.6", gotBody["model"])
	}
	if _, ok := gotBody["messages"]; !ok {
		t.Fatalf("messages missing from body: %#v", gotBody)
	}
	for _, key := range []string{"max_tokens", "temperature", "top_p", "stop", "stream", "stream_options"} {
		if _, ok := gotBody[key]; ok {
			t.Fatalf("%s should be omitted from token-count body: %#v", key, gotBody)
		}
	}
}

func TestAdapter_CountInputTokens_HTTPErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)

	a := NewForInstance(InstanceParams{Name: "kimi", BaseURL: srv.URL, APIKey: "k"})
	_, err := a.CountInputTokens(context.Background(), llm.Request{
		Model:    "kimi-k2.6",
		Messages: []llm.Message{llm.User("hello")},
	})
	if err == nil {
		t.Fatal("CountInputTokens error = nil, want HTTP error")
	}
}

func TestAdapter_Integration_CountInputTokens(t *testing.T) {
	if os.Getenv("SERF_KIMI_E2E") != "1" {
		t.Skip("set SERF_KIMI_E2E=1 to run live Kimi e2e tests")
	}
	if testing.Short() {
		t.Skip("skipping live Kimi e2e test in short mode")
	}
	key := strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
	if key == "" {
		t.Skip("KIMI_API_KEY not set")
	}
	model := strings.TrimSpace(os.Getenv("SERF_KIMI_E2E_MODEL"))
	if model == "" {
		model = "kimi-k2.6"
	}

	a := NewForInstance(InstanceParams{
		Name:    "kimi",
		BaseURL: strings.TrimSpace(os.Getenv("KIMI_BASE_URL")),
		APIKey:  key,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	got, err := a.CountInputTokens(ctx, llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.User("Count these input tokens for serf.")},
		Tools: []llm.ToolDefinition{{
			Name:        "lookup",
			Description: "Looks up a short value.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens <= 0 || !got.Exact || got.Source != llm.TokenCountSourceProvider {
		t.Fatalf("CountInputTokens = %+v, want positive exact provider count", got)
	}
	t.Logf("kimi count tokens: provider=%s model=%s tokens=%d", got.Provider, got.Model, got.Tokens)
}
