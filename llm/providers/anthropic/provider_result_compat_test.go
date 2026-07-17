package anthropic

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

func TestCompleteReadFailurePreservesProviderResultAndRecordsDecodeFailure(t *testing.T) {
	readErr := errors.New("response read failed")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: &providerResultBody{
				data: []byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`),
				err:  readErr,
			},
		}, nil
	})}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_read_error")),
		sink,
	)

	response, err := (&Adapter{APIKey: "test-key", BaseURL: "https://anthropic.test", Client: client}).Complete(
		ctx,
		llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hello")}},
	)
	if err != nil {
		t.Fatalf("Complete returned response read error: %v", err)
	}
	if response.Text() != "hello" {
		t.Fatalf("Complete text = %q, want hello", response.Text())
	}
	assertRecordedDecodeFailure(t, sink.attempts, false)
}

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
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_anthropic_count_decode")),
				sink,
			)

			got, err := (&Adapter{APIKey: "test-key", BaseURL: "https://anthropic.test", Client: client}).CountInputTokens(
				ctx,
				llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hello")}},
			)
			if err != nil {
				t.Fatalf("CountInputTokens returned decode condition: %v", err)
			}
			if got.Tokens != tt.wantToken || !got.Exact || got.Source != llm.TokenCountSourceProvider {
				t.Fatalf("CountInputTokens = %+v, want %d exact provider tokens", got, tt.wantToken)
			}
			assertRecordedDecodeFailure(t, sink.attempts, tt.wantExact)
		})
	}
}

func assertRecordedDecodeFailure(t *testing.T, attempts []apilog.APIAttemptRecord, wantBodyExact bool) {
	t.Helper()
	if len(attempts) != 1 || attempts[0].Outcome != apilog.AttemptDecodeFail {
		t.Fatalf("canonical attempts = %+v, want one response_decoding_failure", attempts)
	}
	if attempts[0].ErrorMessage == "" {
		t.Fatalf("canonical attempt = %+v, want observed decode error evidence", attempts[0])
	}
	if attempts[0].Response == nil || attempts[0].Response.Body.Exact != wantBodyExact {
		t.Fatalf("canonical response = %+v, want body exact=%t", attempts[0].Response, wantBodyExact)
	}
}
