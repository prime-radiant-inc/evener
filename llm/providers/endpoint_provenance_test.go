package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/kimi"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

type endpointProviderCase struct {
	name             string
	completeResponse string
	streamResponse   string
	newAdapter       func(string, *http.Client) llm.ProviderAdapter
}

func endpointProviderCases(t *testing.T) []endpointProviderCase {
	t.Helper()
	chatStream := "data: " + `{"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	return []endpointProviderCase{
		{
			name:             "anthropic",
			completeResponse: `{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`,
			streamResponse: "event: content_block_start\ndata: " + `{"content_block":{"type":"text"}}` + "\n\n" +
				"event: content_block_delta\ndata: " + `{"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
				"event: content_block_stop\ndata: {}\n\n" +
				"event: message_delta\ndata: " + `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}` + "\n\n" +
				"event: message_stop\ndata: {}\n\n",
			newAdapter: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &anthropic.Adapter{APIKey: "test-key", BaseURL: baseURL, Client: client}
			},
		},
		{
			name:             "google",
			completeResponse: `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
			streamResponse:   "data: " + `{"candidates":[{"content":{"parts":[{"text":"ok"}]} ,"finishReason":"STOP"}]}` + "\n\n",
			newAdapter: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				adapter, err := google.NewForInstance(google.GoogleInstanceParams{APIKey: "test-key", BaseURL: baseURL})
				if err != nil {
					t.Fatalf("new Google adapter: %v", err)
				}
				adapter.Client = client
				return adapter
			},
		},
		{
			name:             "openai",
			completeResponse: `{"id":"resp_1","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`,
			streamResponse: "event: response.completed\ndata: " +
				`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"status":"completed"}}` + "\n\n",
			newAdapter: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &openai.Adapter{APIKey: "test-key", BaseURL: baseURL, Client: client}
			},
		},
		{
			name:             "openai-compatible",
			completeResponse: `{"id":"chatcmpl-1","model":"compat-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			streamResponse:   chatStream,
			newAdapter: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				return &openaicompat.Adapter{APIKey: "test-key", BaseURL: baseURL, Client: client}
			},
		},
		{
			name:             "kimi",
			completeResponse: `{"id":"chatcmpl-1","model":"kimi-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
			streamResponse:   chatStream,
			newAdapter: func(baseURL string, client *http.Client) llm.ProviderAdapter {
				adapter := kimi.NewForInstance(kimi.InstanceParams{APIKey: "test-key", BaseURL: baseURL})
				adapter.Client = client
				return adapter
			},
		},
	}
}

func TestCompleteUsesSanitizedFinalResponseEndpoint(t *testing.T) {
	for _, test := range endpointProviderCases(t) {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/final/model" {
					http.Redirect(w, request, server.URL+"/final/model?redirect_token=secret#fragment", http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.completeResponse))
			}))
			t.Cleanup(server.Close)

			response, err := test.newAdapter(server.URL, server.Client()).Complete(context.Background(), llm.Request{
				Model:    test.name + "-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			got, _ := response.Raw["endpoint_url"].(string)
			if want := server.URL + "/final/model"; got != want {
				t.Fatalf("semantic endpoint = %q, want final sanitized endpoint %q", got, want)
			}
		})
	}
}

func TestStreamUsesSanitizedFinalResponseEndpoint(t *testing.T) {
	for _, test := range endpointProviderCases(t) {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/final/model" {
					http.Redirect(w, request, server.URL+"/final/model?redirect_token=secret#fragment", http.StatusTemporaryRedirect)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, test.streamResponse)
			}))
			t.Cleanup(server.Close)

			stream, err := test.newAdapter(server.URL, server.Client()).Stream(context.Background(), llm.Request{
				Model:    test.name + "-model",
				Messages: []llm.Message{llm.User("hello")},
			})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer stream.Close() //nolint:errcheck

			var response *llm.Response
			for event := range stream.Events() {
				if event.Err != nil {
					t.Fatalf("stream event: %v", event.Err)
				}
				if event.Type == llm.StreamEventFinish {
					response = event.Response
				}
			}
			if response == nil {
				t.Fatal("stream produced no semantic response")
			}
			got, _ := response.Raw["endpoint_url"].(string)
			if want := server.URL + "/final/model"; got != want {
				t.Fatalf("semantic endpoint = %q, want final sanitized endpoint %q", got, want)
			}
		})
	}
}
