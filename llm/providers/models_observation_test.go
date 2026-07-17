package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

type modelListRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelListRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type modelListAttemptSink struct {
	attempts []apilog.APIAttemptRecord
}

func (s *modelListAttemptSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.attempts = append(s.attempts, record)
	return nil
}

func (*modelListAttemptSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

type jsonThenErrorBody struct {
	data    []byte
	offset  int
	tailErr error
	closed  bool
}

func (b *jsonThenErrorBody) Read(p []byte) (int, error) {
	if b.offset < len(b.data) {
		n := copy(p, b.data[b.offset:])
		b.offset += n
		return n, nil
	}
	return 0, b.tailErr
}

func (b *jsonThenErrorBody) Close() error {
	b.closed = true
	return nil
}

type unreadModelListBody struct {
	reads  int
	closed bool
}

func (b *unreadModelListBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *unreadModelListBody) Close() error {
	b.closed = true
	return nil
}

func TestListModelsObservesOnlyProviderReads(t *testing.T) {
	tailErr := errors.New("unexpected trailing read")
	tests := []struct {
		name       string
		body       string
		wantModel  string
		newAdapter func(*http.Client) llm.ModelLister
	}{
		{
			name:      "anthropic",
			body:      `{"data":[{"id":"claude-test","display_name":"Claude Test"}],"has_more":false}`,
			wantModel: "claude-test",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &anthropic.Adapter{
					APIKey:           "test-key",
					ProviderInstance: "anthropic-test",
					BaseURL:          "https://example.test",
					Client:           client,
				}
			},
		},
		{
			name:      "google",
			body:      `{"models":[{"name":"models/gemini-test","displayName":"Gemini Test","supportedGenerationMethods":["generateContent"]}]}`,
			wantModel: "gemini-test",
			newAdapter: func(client *http.Client) llm.ModelLister {
				adapter, err := google.NewForInstance(google.GoogleInstanceParams{
					Name:    "google-test",
					APIKey:  "test-key",
					BaseURL: "https://example.test",
				})
				if err != nil {
					t.Fatalf("new Google adapter: %v", err)
				}
				adapter.Client = client
				return adapter
			},
		},
		{
			name:      "openai",
			body:      `{"data":[{"id":"gpt-test"}]}`,
			wantModel: "gpt-test",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &openai.Adapter{
					APIKey:           "test-key",
					ProviderInstance: "openai-test",
					BaseURL:          "https://example.test/v1",
					Client:           client,
				}
			},
		},
		{
			name:      "openai-compatible",
			body:      `{"data":[{"id":"compat-test"}]}`,
			wantModel: "compat-test",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &openaicompat.Adapter{
					APIKey:           "test-key",
					ProviderInstance: "compat-test",
					BaseURL:          "https://example.test/v1",
					Client:           client,
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &jsonThenErrorBody{data: []byte(test.body), tailErr: tailErr}
			client := &http.Client{Transport: modelListRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					Body:          body,
					ContentLength: -1,
					Request:       request,
				}, nil
			})}
			sink := &modelListAttemptSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_models_"+test.name)),
				sink,
			)

			models, err := test.newAdapter(client).ListModels(ctx)
			if err != nil {
				t.Fatalf("ListModels returned a trailing-read error: %v", err)
			}
			if len(models) != 1 || models[0].ID != test.wantModel {
				t.Fatalf("ListModels models = %+v, want only %q", models, test.wantModel)
			}
			if !body.closed {
				t.Fatal("ListModels did not close the response body")
			}
			if len(sink.attempts) != 1 {
				t.Fatalf("attempts = %d, want 1", len(sink.attempts))
			}
			record := sink.attempts[0]
			if record.Response == nil {
				t.Fatal("attempt response is nil")
			}
			observed, err := apilog.DecodeBody(record.Response.Body)
			if err != nil {
				t.Fatalf("decode observed response body: %v", err)
			}
			if !bytes.Equal(observed, []byte(test.body)) {
				t.Fatalf("observed response body = %q, want %q", observed, test.body)
			}
			if record.Response.Body.Exact {
				t.Fatal("response body marked exact without observing EOF")
			}
			if record.Outcome != apilog.AttemptSuccess {
				t.Fatalf("attempt outcome = %q, want success", record.Outcome)
			}
		})
	}
}

func TestListModelsDoesNotReadRejectedResponseForEvidence(t *testing.T) {
	tests := []struct {
		name       string
		newAdapter func(*http.Client) llm.ModelLister
	}{
		{
			name: "anthropic",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &anthropic.Adapter{APIKey: "test-key", BaseURL: "https://example.test", Client: client}
			},
		},
		{
			name: "google",
			newAdapter: func(client *http.Client) llm.ModelLister {
				adapter, err := google.NewForInstance(google.GoogleInstanceParams{
					Name:    "google-test",
					APIKey:  "test-key",
					BaseURL: "https://example.test",
				})
				if err != nil {
					t.Fatalf("new Google adapter: %v", err)
				}
				adapter.Client = client
				return adapter
			},
		},
		{
			name: "openai",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &openai.Adapter{APIKey: "test-key", BaseURL: "https://example.test/v1", Client: client}
			},
		},
		{
			name: "openai-compatible",
			newAdapter: func(client *http.Client) llm.ModelLister {
				return &openaicompat.Adapter{APIKey: "test-key", BaseURL: "https://example.test/v1", Client: client}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &unreadModelListBody{}
			client := &http.Client{Transport: modelListRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusServiceUnavailable,
					Header:        make(http.Header),
					Body:          body,
					ContentLength: -1,
					Request:       request,
				}, nil
			})}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_models_reject_"+test.name)),
				&modelListAttemptSink{},
			)

			_, err := test.newAdapter(client).ListModels(ctx)
			if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
				t.Fatalf("ListModels error = %v, want HTTP 503", err)
			}
			if body.reads != 0 {
				t.Fatalf("response reads = %d, want 0", body.reads)
			}
			if !body.closed {
				t.Fatal("ListModels did not close the rejected response body")
			}
		})
	}
}
