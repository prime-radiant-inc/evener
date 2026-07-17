package openai

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

func TestCountInputTokensDecodeConditionsPreserveProviderResult(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		readErr   error
		wantToken int
		wantExact bool
	}{
		{name: "read failure after complete JSON", body: []byte(`{"input_tokens":41}`), readErr: errors.New("response read failed"), wantToken: 41},
		{name: "malformed JSON", body: []byte(`{"input_tokens":`), wantToken: 0, wantExact: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &providerResultBody{data: tt.body, err: tt.readErr}}, nil
			})}
			sink := &wireCaptureSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_openai_count_decode")),
				sink,
			)

			got, err := (&Adapter{APIKey: "test-key", BaseURL: "https://openai.test", Client: client}).CountInputTokens(
				ctx,
				llm.Request{Model: "gpt-test", Messages: []llm.Message{llm.User("hello")}},
			)
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
