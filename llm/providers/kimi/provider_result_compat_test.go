package kimi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type providerResultBody struct {
	data []byte
	err  error
	done bool
}

func (b *providerResultBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.EOF
	}
	b.done = true
	return copy(p, b.data), b.err
}

func (*providerResultBody) Close() error { return nil }

type providerResultRoundTripFunc func(*http.Request) (*http.Response, error)

func (f providerResultRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	return f(request)
}

type providerResultSink struct {
	attempts []apilog.APIAttemptRecord
}

func (s *providerResultSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.attempts = append(s.attempts, record)
	return nil
}

func (*providerResultSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func TestCountInputTokensDecodeConditionsPreserveProviderResult(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		readErr   error
		wantToken int
		wantExact bool
	}{
		{name: "read failure after complete JSON", body: []byte(`{"data":{"total_tokens":41}}`), readErr: errors.New("response read failed"), wantToken: 41},
		{name: "malformed JSON", body: []byte(`{"data":{"total_tokens":`), wantToken: 0, wantExact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: providerResultRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &providerResultBody{data: tt.body, err: tt.readErr}}, nil
			})}
			sink := &providerResultSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_kimi_count_decode")),
				sink,
			)
			adapter := NewForInstance(InstanceParams{Name: "kimi", BaseURL: "https://kimi.test", APIKey: "test-key"})
			adapter.Client = client

			got, err := adapter.CountInputTokens(ctx, llm.Request{Model: "kimi-test", Messages: []llm.Message{llm.User("hello")}})
			if err != nil {
				t.Fatalf("CountInputTokens returned decode condition: %v", err)
			}
			if got.Tokens != tt.wantToken || !got.Exact || got.Source != llm.TokenCountSourceProvider {
				t.Fatalf("CountInputTokens = %+v, want %d exact provider tokens", got, tt.wantToken)
			}
			if len(sink.attempts) != 1 || sink.attempts[0].Outcome != apilog.AttemptDecodeFail {
				t.Fatalf("canonical attempts = %+v, want one response_decoding_failure", sink.attempts)
			}
			if sink.attempts[0].ErrorMessage == "" {
				t.Fatalf("canonical attempt = %+v, want observed decode error evidence", sink.attempts[0])
			}
			if sink.attempts[0].Response == nil || sink.attempts[0].Response.Body.Exact != tt.wantExact {
				t.Fatalf("canonical response = %+v, want body exact=%t", sink.attempts[0].Response, tt.wantExact)
			}
		})
	}
}
