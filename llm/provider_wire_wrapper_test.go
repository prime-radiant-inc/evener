package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providercfg"
	_ "primeradiant.com/serf/llm/providers/glm"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/ollama"
	_ "primeradiant.com/serf/llm/providers/openrouter"
	_ "primeradiant.com/serf/llm/providers/openrouter_anthropic"
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

func TestWrapperFactoriesInheritWireCaptureExactlyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"glm-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/v1/messages":
			_, _ = w.Write([]byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		name          string
		provider      string
		providerType  providercfg.Type
		model         string
		builtInHeader string
	}{
		{
			name:          "OpenAI-compatible wrapper",
			provider:      "glm-wire",
			providerType:  providercfg.Type("glm"),
			model:         "glm-test",
			builtInHeader: "Authorization",
		},
		{
			name:          "Anthropic wrapper",
			provider:      "openrouter-anthropic-wire",
			providerType:  providercfg.Type("openrouter-anthropic"),
			model:         "claude-test",
			builtInHeader: "x-api-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := providerInstance(tt.provider, string(tt.providerType), "", server.URL, tt.builtInHeader)
			client, err := llm.NewFromProviders(providercfg.Config{
				Default:   tt.provider,
				Instances: []providercfg.InstanceConfig{instance},
			})
			if err != nil {
				t.Fatalf("NewFromProviders: %v", err)
			}

			sink := &wrapperWireCaptureSink{}
			groupID := "ag_wrapper_" + tt.provider
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup(groupID)),
				sink,
			)
			response, err := client.Complete(ctx, providerRequest(tt.provider, tt.model))
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
			if attempt.ProviderInstance != tt.provider {
				t.Fatalf("provider instance = %q, want wrapper instance %q", attempt.ProviderInstance, tt.provider)
			}
		})
	}
}

func TestDefaultNameOpenAICompatibleWrapperFactoriesPreserveWireCaptureProvenance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"wrapper-test","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)

	for _, providerType := range []string{"glm", "kimi", "ollama", "openrouter"} {
		t.Run(providerType, func(t *testing.T) {
			client, err := llm.NewFromProviders(providercfg.Config{
				Instances: []providercfg.InstanceConfig{{
					Type:    providercfg.Type(providerType),
					BaseURL: server.URL,
					APIKey:  "provider-key",
				}},
			})
			if err != nil {
				t.Fatalf("NewFromProviders: %v", err)
			}
			sink := &wrapperWireCaptureSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_default_wrapper_"+providerType)),
				sink,
			)
			response, err := client.Complete(ctx, providerRequest(providerType, "wrapper-test"))
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
			if got := attempts[0].ProviderInstance; got != providerType {
				t.Fatalf("provider instance = %q, want default wrapper name %q", got, providerType)
			}
		})
	}
}
