package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// buildBodyForTest runs the real Responses request builder and round-trips the
// result through JSON so assertions see exactly the wire shape.
func buildBodyForTest(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	a := &Adapter{}
	body, err := a.buildRequestBody(req)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	return round
}

// collectInputImages returns every input_image item in the request input, both
// inside user message content and as top-level items (the tool-result image
// site).
func collectInputImages(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	input, _ := body["input"].([]any)
	var images []map[string]any
	for _, itemAny := range input {
		item, _ := itemAny.(map[string]any)
		switch item["type"] {
		case "input_image":
			images = append(images, item)
		case "message":
			content, _ := item["content"].([]any)
			for _, cAny := range content {
				c, _ := cAny.(map[string]any)
				if c["type"] == "input_image" {
					images = append(images, c)
				}
			}
		}
	}
	return images
}

// imageRequest builds a request carrying an image at both sites: a user-message
// image (with an explicit detail hint) and a tool-result image.
func imageRequest(model string) llm.Request {
	return llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "look"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{
					MediaType: "image/png",
					Data:      []byte{0x01, 0x02},
					Detail:    "high",
				}},
			}},
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "screenshot", Arguments: json.RawMessage(`{}`), Type: "function"}},
			}},
			{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					ToolCallID:     "call_1",
					Content:        "captured",
					ImageData:      []byte{0x03, 0x04},
					ImageMediaType: "image/png",
				}},
			}},
		},
	}
}

// GPT-5.6 runs on OpenAI's responses-lite backend, which mishandles the image
// "detail" field; the first-party codex client strips it from every image —
// including explicitly-set details — so serf must omit it entirely on both
// image sites.
func TestGPT56_ImageDetailOmitted(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6"} {
		t.Run(model, func(t *testing.T) {
			body := buildBodyForTest(t, imageRequest(model))
			images := collectInputImages(t, body)
			if len(images) != 2 {
				t.Fatalf("expected 2 input_image items (user + tool result), got %d: %#v", len(images), images)
			}
			for i, img := range images {
				if d, ok := img["detail"]; ok {
					t.Errorf("image %d: detail = %#v, want field omitted for %s", i, d, model)
				}
			}
		})
	}
}

// Regression guard: gpt-5.5 image encoding is unchanged — explicit detail
// passes through on the user site, and the tool-result site gets the model
// default ("original" on gpt-5.4+).
func TestGPT55_ImageDetailUnchanged(t *testing.T) {
	body := buildBodyForTest(t, imageRequest("gpt-5.5"))
	images := collectInputImages(t, body)
	if len(images) != 2 {
		t.Fatalf("expected 2 input_image items, got %d: %#v", len(images), images)
	}
	// User-message image carried an explicit detail hint.
	if images[0]["detail"] != "high" {
		t.Errorf("user image detail = %#v, want \"high\"", images[0]["detail"])
	}
	// Tool-result image gets the model default.
	if images[1]["detail"] != "original" {
		t.Errorf("tool-result image detail = %#v, want \"original\"", images[1]["detail"])
	}
}

// GPT-5.6 requests always carry the reasoning object and include
// reasoning.encrypted_content, even when no effort override is set — matching
// the codex client — so thinking summaries display and reasoning replays
// across turns.
func TestGPT56_ReasoningAlwaysRequested(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "gpt-5.6-sol",
		Messages: []llm.Message{llm.User("hi")},
	})
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil {
		t.Fatalf("reasoning missing, want it always sent for gpt-5.6; body=%v", body)
	}
	if reasoning["summary"] != "detailed" {
		t.Errorf("reasoning.summary = %#v, want \"detailed\"", reasoning["summary"])
	}
	if _, ok := reasoning["effort"]; ok {
		t.Errorf("reasoning.effort = %#v, want omitted when no effort requested", reasoning["effort"])
	}
	include, _ := body["include"].([]any)
	found := false
	for _, v := range include {
		if v == "reasoning.encrypted_content" {
			found = true
		}
	}
	if !found {
		t.Errorf("include = %#v, want reasoning.encrypted_content present", include)
	}
}

// Regression guard: models outside gpt-5.6 keep the existing contract — no
// reasoning object and no include unless an effort is requested.
func TestGPT55_NoReasoningWithoutEffort(t *testing.T) {
	body := buildBodyForTest(t, llm.Request{
		Model:    "gpt-5.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if r, ok := body["reasoning"]; ok {
		t.Errorf("reasoning = %#v, want omitted without effort on gpt-5.5", r)
	}
	if inc, ok := body["include"]; ok {
		t.Errorf("include = %#v, want omitted without effort on gpt-5.5", inc)
	}
}

// gpt-5.6 supports a real "max" wire level above xhigh; the provider must send
// it verbatim, never folding it to xhigh.
func TestGPT56_MaxEffortSentVerbatim(t *testing.T) {
	eff := "max"
	body := buildBodyForTest(t, llm.Request{
		Model:           "gpt-5.6-sol",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &eff,
	})
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "max" {
		t.Fatalf("reasoning = %#v, want effort \"max\" on the wire", body["reasoning"])
	}
}

// prompt_cache_retention is deprecated in favor of prompt_cache_options.ttl;
// gpt-5.6 uses the new field while older models keep the deprecated one.
func TestPromptCacheRetention_FieldMigration(t *testing.T) {
	build := func(model string) map[string]any {
		return buildBodyForTest(t, llm.Request{
			Model:                model,
			Messages:             []llm.Message{llm.User("hi")},
			PromptCacheRetention: "24h",
		})
	}

	t.Run("gpt-5.6 sends prompt_cache_options.ttl", func(t *testing.T) {
		body := build("gpt-5.6-sol")
		if v, ok := body["prompt_cache_retention"]; ok {
			t.Errorf("prompt_cache_retention = %#v, want omitted on gpt-5.6", v)
		}
		opts, _ := body["prompt_cache_options"].(map[string]any)
		if opts == nil || opts["ttl"] != "24h" {
			t.Fatalf("prompt_cache_options = %#v, want {ttl: \"24h\"}", body["prompt_cache_options"])
		}
	})

	t.Run("gpt-5.5 keeps prompt_cache_retention", func(t *testing.T) {
		body := build("gpt-5.5")
		if body["prompt_cache_retention"] != "24h" {
			t.Errorf("prompt_cache_retention = %#v, want \"24h\"", body["prompt_cache_retention"])
		}
		if v, ok := body["prompt_cache_options"]; ok {
			t.Errorf("prompt_cache_options = %#v, want omitted on gpt-5.5", v)
		}
	})
}

// GPT-5.6 Responses streams can interleave item types serf does not use —
// program / program_output (programmatic tool calling), tool_search_call, and
// compaction items. The decoder must tolerate them (ignore or pass through as
// provider events) and still deliver the surrounding text and tool calls.
func TestResponsesStream_UnknownItemTypesTolerated(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"program","id":"prog_1"}}`,
		`data: {"type":"response.output_item.added","item":{"type":"tool_search_call","id":"ts_1","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"program_output","id":"po_1","output":"ok"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"compaction","id":"cmp_1"}}`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"fc_1","name":"shell"}}`,
		`data: {"type":"response.function_call_arguments.done","call_id":"call_1","arguments":"{}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"fc_1","name":"shell","arguments":"{}"}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"completed","output":[{"type":"program","id":"prog_1"},{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"tool_search_call","id":"ts_1","status":"completed"},{"type":"function_call","call_id":"call_1","id":"fc_1","name":"shell","arguments":"{}"},{"type":"compaction","id":"cmp_1"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
	}, "\n\n") + "\n\n"

	resp, sawError := accumulateResponsesSSE(&Adapter{}, []byte(sse), false)
	if sawError {
		t.Fatal("stream with unknown item types emitted an error event")
	}
	if resp == nil {
		t.Fatal("stream with unknown item types never completed")
	}
	if got := resp.Message.Text(); !strings.Contains(got, "hello") {
		t.Errorf("text = %q, want it to contain \"hello\"", got)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "shell" {
		t.Errorf("tool calls = %#v, want one shell call", calls)
	}
}
