package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

type wrapperWireCaptureSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *wrapperWireCaptureSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, record)
	return nil
}

func (*wrapperWireCaptureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func (s *wrapperWireCaptureSink) snapshot() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

// TestVendorInstancesInheritWireCaptureExactlyOnce is the wrapper-factory
// guarantee restated for the registry: a vendor id reached over a protocol —
// including one whose protocol is not the vendor's own (spec §14.1's
// OpenRouter-over-Anthropic recipe) — records exactly one core attempt, under
// the instance's own name.
func TestVendorInstancesInheritWireCaptureExactlyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"glm-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/messages":
			_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		name     string
		instance string
		base     string
		protocol string
		model    string
	}{
		{name: "chat-completions vendor", instance: "glm-wire", base: "zai", protocol: registry.ProtocolOpenAIChat, model: "glm-test"},
		{name: "anthropic-protocol vendor", instance: "openrouter-anthropic-wire", base: "openrouter", protocol: registry.ProtocolAnthropic, model: "claude-test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := wireProvider{name: tt.instance, protocol: tt.protocol, base: tt.base}
			client := provider.wireClient(t, server.URL, server.Client(), nil)

			sink := &wrapperWireCaptureSink{}
			groupID := "ag_wrapper_" + tt.instance
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			response, err := client.Complete(ctx, providerRequest(tt.instance, tt.model))
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if response.Text() != "hello" {
				t.Fatalf("response text = %q, want hello", response.Text())
			}

			attempts := sink.snapshot()
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want exactly 1 inherited core attempt", len(attempts))
			}
			attempt := attempts[0]
			if attempt.AttemptGroupID != groupID || attempt.AttemptIndex != 1 {
				t.Fatalf("attempt group/index = %q/%d, want %q/1", attempt.AttemptGroupID, attempt.AttemptIndex, groupID)
			}
			if attempt.ProviderInstance != tt.instance {
				t.Fatalf("provider instance = %q, want instance %q", attempt.ProviderInstance, tt.instance)
			}
		})
	}
}

// TestCuratedVendorInstancesPreserveWireCaptureProvenance is the default-name
// half: an instance that takes the curated vendor id as its own name still
// records its attempt under that name.
func TestCuratedVendorInstancesPreserveWireCaptureProvenance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"wrapper-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)

	for _, base := range []string{"zai", "moonshotai", "ollama", "openrouter"} {
		t.Run(base, func(t *testing.T) {
			provider := wireProvider{name: base, protocol: registry.ProtocolOpenAIChat, base: base}
			client := provider.wireClient(t, server.URL, server.Client(), nil)
			sink := &wrapperWireCaptureSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_default_wrapper_"+base)),
				sink,
			)
			response, err := client.Complete(ctx, providerRequest(base, "wrapper-test"))
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if response.Text() != "hello" {
				t.Fatalf("response text = %q, want hello", response.Text())
			}
			attempts := sink.snapshot()
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want exactly 1 inherited core attempt", len(attempts))
			}
			if got := attempts[0].ProviderInstance; got != base {
				t.Fatalf("provider instance = %q, want curated id %q", got, base)
			}
		})
	}
}
