package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// TestAdapter_Stream_SynthesizesRecordingFromAccumulatedItems covers the
// affected Responses-API wire shape: the terminal response.completed event
// carries an empty "output" array even though earlier
// response.output_item.done events in the same stream carried real content
// (tool call + text). The settled Response recorded for the attempt must
// come from what the stream actually sent, not the empty terminal payload.
func TestAdapter_Stream_SynthesizesRecordingFromAccumulatedItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\ndata: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
		write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
		write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\":\"x\"}"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`)
		// Terminal payload's output is empty, matching the affected wire shape.
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var finish *llm.Response
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finish = ev.Response
		}
	}
	if finish == nil {
		t.Fatalf("expected finish response")
	}
	if strings.TrimSpace(finish.Text()) != "Hello" {
		t.Fatalf("Text() = %q, want %q", finish.Text(), "Hello")
	}
	calls := finish.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1 (%+v)", len(calls), calls)
	}
	if calls[0].Name != "write_file" {
		t.Fatalf("tool call name = %q, want write_file", calls[0].Name)
	}
}

// TestAdapter_Stream_TerminalOutputWinsWhenNonEmpty pins the opposite case:
// when the terminal response.completed payload's "output" is non-empty, it
// is authoritative even though it disagrees with what the stream
// accumulated — the terminal payload is the provider's settled truth.
func TestAdapter_Stream_TerminalOutputWinsWhenNonEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\ndata: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
		write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
		write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\":\"x\"}"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`)
		// Terminal payload's output is non-empty and disagrees with the
		// accumulated stream items (one message, no tool call).
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Different terminal text"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var finish *llm.Response
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finish = ev.Response
		}
	}
	if finish == nil {
		t.Fatalf("expected finish response")
	}
	if strings.TrimSpace(finish.Text()) != "Different terminal text" {
		t.Fatalf("Text() = %q, want terminal payload's text", finish.Text())
	}
	if calls := finish.ToolCalls(); len(calls) != 0 {
		t.Fatalf("ToolCalls() = %d, want 0 (terminal payload has no tool call): %+v", len(calls), calls)
	}
}
