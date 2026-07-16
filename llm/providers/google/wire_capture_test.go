package google

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

type wireCaptureSink struct {
	attempts []apilog.APIAttemptRecord
}

func TestStreamWireCaptureRecordsExactAttemptBeforeFinish(t *testing.T) {
	responseBody := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
		``,
		``,
	}, "\n")
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{APIKey: "secret-api-key", BaseURL: server.URL, Client: server.Client()}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_google_stream_wire")),
		sink,
	)
	stream, err := adapter.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var finish *llm.Response
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", event.Err)
		}
		if event.Type == llm.StreamEventFinish {
			finish = event.Response
			if len(sink.attempts) != 1 {
				t.Fatalf("attempts visible at finish = %d, want 1", len(sink.attempts))
			}
		}
	}
	if finish == nil || finish.Text() != "hello" {
		t.Fatalf("finish response = %#v, want hello", finish)
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	gotRequestBody, err := apilog.DecodeBody(record.Request.Body)
	if err != nil {
		t.Fatalf("decode recorded request: %v", err)
	}
	if !bytes.Equal(gotRequestBody, requestBody) {
		t.Fatalf("recorded request bytes = %q, want server bytes %q", gotRequestBody, requestBody)
	}
	gotResponseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if !bytes.Equal(gotResponseBody, []byte(responseBody)) {
		t.Fatalf("recorded response bytes = %q, want %q", gotResponseBody, responseBody)
	}
	if record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want success", record.Outcome)
	}
}

func (s *wireCaptureSink) AppendAttempt(_ context.Context, record apilog.APIAttemptRecord) error {
	s.attempts = append(s.attempts, record)
	return nil
}

func (*wireCaptureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func TestCompleteWireCaptureRecordsExactCredentialFreeAttempt(t *testing.T) {
	const responseBody = `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{
		name:              "google-test",
		APIKey:            "secret-api-key",
		BaseURL:           server.URL,
		Client:            server.Client(),
		DefaultHeaders:    map[string]string{"X-Visible": "visible-value"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret-gateway-key"},
	}
	sink := &wireCaptureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_google_wire")),
		sink,
	)

	response, err := adapter.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text() != "hello" {
		t.Fatalf("response text = %q, want hello", response.Text())
	}
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	gotRequestBody, err := apilog.DecodeBody(record.Request.Body)
	if err != nil {
		t.Fatalf("decode recorded request: %v", err)
	}
	if !bytes.Equal(gotRequestBody, requestBody) {
		t.Fatalf("recorded request bytes = %q, want server bytes %q", gotRequestBody, requestBody)
	}
	gotResponseBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode recorded response: %v", err)
	}
	if !bytes.Equal(gotResponseBody, []byte(responseBody)) {
		t.Fatalf("recorded response bytes = %q, want %q", gotResponseBody, responseBody)
	}
	if record.ProviderInstance != "google-test" || record.Outcome != apilog.AttemptSuccess {
		t.Fatalf("attempt provenance/outcome = %q/%q", record.ProviderInstance, record.Outcome)
	}
	if got := record.Request.Headers["X-Visible"]; len(got) != 1 || got[0] != "visible-value" {
		t.Fatalf("visible headers = %#v, want one visible-value", got)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	for _, secret := range []string{"secret-api-key", "secret-gateway-key", "X-Gateway-Key", "?key=", "&key="} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("canonical attempt contains credential sentinel %q: %s", secret, encoded)
		}
	}
}
