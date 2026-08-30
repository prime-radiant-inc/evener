package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// Some Responses-API gateways (lunaroute fronting GLM, observed live) stream
// reasoning as raw reasoning_text events on the reasoning item's content
// rather than as OpenAI's reasoning_summary events. The decoder must surface
// those as reasoning deltas so the UI shows the model thinking instead of
// nothing for minutes at a time.
func TestAdapter_Stream_EmitsReasoningTextDeltas(t *testing.T) {
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
		write("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[],"summary":[]}}`)
		write("response.reasoning_part.added", `{"type":"response.reasoning_part.added","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`)
		write("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"Let me "}`)
		write("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"think."}`)
		write("response.reasoning_text.done", `{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"Let me think."}`)
		write("response.reasoning_part.done", `{"type":"response.reasoning_part.done","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":"Let me think."}}`)
		write("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me think."}],"summary":[]}}`)
		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Answer"}`)
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"glm-5.3","output":[{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me think."}],"summary":[]},{"type":"message","content":[{"type":"output_text","text":"Answer"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "glm-5.3", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var reasoning strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoning.WriteString(ev.ReasoningDelta)
		}
	}
	if got := reasoning.String(); got != "Let me think." {
		t.Fatalf("reasoning stream = %q, want %q", got, "Let me think.")
	}
}

// A reasoning item that carries plain reasoning_text content (no
// encrypted_content) must still land in the settled message as a thinking
// part, or the transcript loses everything the model thought.
func TestResponseContentFromOutputItems_KeepsReasoningTextContent(t *testing.T) {
	var out []any
	if err := json.Unmarshal([]byte(`[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me "},{"type":"reasoning_text","text":"think."}],"summary":[]},
		{"type":"message","content":[{"type":"output_text","text":"Answer"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out)
	if len(content) != 2 {
		t.Fatalf("content parts = %d, want 2 (thinking + text): %#v", len(content), content)
	}
	th := content[0]
	if th.Kind != llm.ContentThinking || th.Thinking == nil {
		t.Fatalf("content[0] = %#v, want thinking part", th)
	}
	if th.Thinking.Text != "Let me think." {
		t.Fatalf("thinking text = %q, want %q", th.Thinking.Text, "Let me think.")
	}
	if th.Thinking.ID != "rs_1" {
		t.Fatalf("thinking id = %q, want rs_1", th.Thinking.ID)
	}
	if th.Thinking.EncryptedContent != "" {
		t.Fatalf("encrypted content = %q, want empty", th.Thinking.EncryptedContent)
	}
}

// With no effort requested, a Responses-API model that may reason gets a
// medium default rather than whatever the provider decides. Leaving the
// reasoning object out let a gateway-fronted GLM think for 25k tokens on one
// turn (observed live).
func TestResponses_DefaultsReasoningEffortMediumWhenUnset(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "glm-5.3",
		Messages: []llm.Message{llm.User("hi")},
	})
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "medium" {
		t.Fatalf("reasoning = %#v, want effort medium when unset", body["reasoning"])
	}
	include, _ := body["include"].([]any)
	found := false
	for _, v := range include {
		if v == "reasoning.encrypted_content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("include = %#v, want reasoning.encrypted_content alongside the default effort", body["include"])
	}
}

// Models the catalog knows cannot reason must not receive a reasoning object:
// the API rejects it with a 400.
func TestResponses_NoDefaultReasoningEffortForNonReasoningModel(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "gpt-4.1",
		Messages: []llm.Message{llm.User("hi")},
	})
	if r, ok := body["reasoning"]; ok {
		t.Fatalf("reasoning = %#v, want omitted for a non-reasoning model", r)
	}
	if inc, ok := body["include"]; ok {
		t.Fatalf("include = %#v, want omitted for a non-reasoning model", inc)
	}
}
