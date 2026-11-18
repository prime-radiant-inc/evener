package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// chatStreamEvents drives one Chat Completions SSE body through the real
// Protocol.Stream and returns every emitted event.
func chatStreamEvents(t *testing.T, sse string, mutate func(c *registry.Caps)) ([]llm.StreamEvent, error) {
	t.Helper()
	srv, _ := server(t, 200, sse)
	res := liveRes(srv, mutate)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), res)
	if err != nil {
		return nil, err
	}
	defer s.Close() //nolint:errcheck
	var out []llm.StreamEvent
	for ev := range s.Events() {
		out = append(out, ev)
	}
	var terminal error
	for _, ev := range out {
		if ev.Type == llm.StreamEventError {
			terminal = ev.Err
		}
	}
	return out, terminal
}

// countEvents tallies the events of each given type.
func countEvents(events []llm.StreamEvent) map[llm.StreamEventType]int {
	counts := make(map[llm.StreamEventType]int)
	for _, ev := range events {
		counts[ev.Type]++
	}
	return counts
}

// TestStreamReasoningOnlyClosesAtDone pins the reasoning-only close: a stream
// that emitted reasoning and no text or tool call closes the reasoning segment
// at [DONE], before the finish event.
func TestStreamReasoningOnlyClosesAtDone(t *testing.T) {
	t.Parallel()
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"only thinking\"}}]}\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	counts := countEvents(events)
	if counts[llm.StreamEventReasoningEnd] != 1 {
		t.Fatalf("reasoning end events = %d, want exactly one at [DONE]: %+v", counts[llm.StreamEventReasoningEnd], events)
	}
	var reasoningEndSeen, finishSeen bool
	for _, ev := range events {
		if ev.Type == llm.StreamEventReasoningEnd {
			if finishSeen {
				t.Fatalf("reasoning ended only after the finish event: %+v", events)
			}
			reasoningEndSeen = true
		}
		if ev.Type == llm.StreamEventFinish {
			finishSeen = true
			if !reasoningEndSeen {
				t.Fatalf("finish event before the reasoning segment closed: %+v", events)
			}
			if ev.Response == nil {
				t.Fatal("the finish event must carry the settled response")
			}
			if text := ev.Response.Text(); text != "" {
				t.Fatalf("a reasoning-only stream settles with no text content, got %q", text)
			}
		}
	}
	if !reasoningEndSeen || !finishSeen {
		t.Fatalf("events = %+v, want reasoning end then finish", events)
	}
}

func TestStreamReasoningReplay(t *testing.T) {
	t.Parallel()
	const scalar = `data: {"choices":[{"delta":{"reasoning":" α\n"}}]}` + "\n\n"
	const tail = `data: {"choices":[{"delta":{"reasoning":"β "}}]}` + "\n\n"
	const details = `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"detail"}]}}]}` + "\n\n"
	const together = `data: {"choices":[{"delta":{"reasoning_text":" α\n","reasoning_details":[{"type":"reasoning.text","text":"detail"}]}}]}` + "\n\n"
	for _, tc := range []struct {
		name, body, text, signature, replayField, replayText string
	}{
		{"scalar only", scalar + tail, " α\nβ ", "reasoning", "reasoning", " α\nβ "},
		{"details only", details, "detail", "", "reasoning_content", "detail"},
		{"details after scalar", scalar + details, " α\ndetail", "", "reasoning", " α\n"},
		{"scalar after details", details + scalar, "detail α\n", "", "reasoning", " α\n"},
		{"scalar on both sides of details", scalar + details + tail, " α\ndetailβ ", "", "reasoning", " α\nβ "},
		{"scalar and details in one delta", together, "detail", "", "reasoning_text", " α\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := ExtractRecordedResponse([]byte(tc.body+"data: [DONE]\n\n"), "m")
			if err != nil {
				t.Fatal(err)
			}
			parts := resp.Message.Content
			if len(parts) != 1 || parts[0].Kind != llm.ContentThinking || parts[0].Thinking == nil {
				t.Fatalf("settled content = %+v, want one thinking part", parts)
			}
			if got := parts[0].Thinking; got.Text != tc.text || got.Signature != tc.signature || got.EncryptedContent != "" {
				t.Fatalf("settled thinking = %+v, want text %q and signature %q without opaque details", got, tc.text, tc.signature)
			}
			stored, err := json.Marshal(resp.Message)
			if err != nil {
				t.Fatal(err)
			}
			var history llm.Message
			if err := json.Unmarshal(stored, &history); err != nil {
				t.Fatal(err)
			}
			for _, override := range []string{"", "reasoning_content", "reasoning_details"} {
				field := tc.replayField
				if override != "" {
					field = override
				}
				body, err := buildBody(llm.Request{Messages: []llm.Message{history}}, registry.Resolved{
					Caps: registry.Caps{ReasoningField: &override},
				}, false)
				if err != nil {
					t.Fatal(err)
				}
				assistant := body["messages"].([]map[string]any)[0]
				if field == "reasoning_details" {
					items, ok := assistant[field].([]map[string]any)
					if !ok || len(items) != 1 || items[0]["text"] != tc.text {
						t.Errorf("array replay = %v, want one detail with text %q", assistant, tc.text)
					}
				} else if assistant[field] != tc.replayText {
					t.Errorf("scalar replay = %v, want %s = %q", assistant, field, tc.replayText)
				}
				if override == "" && tc.text != tc.replayText {
					thinking := history.Content[0].Thinking
					display := thinking.Text
					thinking.Text = ""
					hidden, err := toChatMessages([]llm.Message{history}, registry.Caps{}, false)
					thinking.Text = display
					if err != nil || hidden[0][field] != tc.replayText {
						t.Errorf("scalar replay with display cleared = %v (%v), want %s = %q", hidden, err, field, tc.replayText)
					}
				}
				for _, other := range append(llm.OpenAICompatReasoningFields(), "reasoning_details") {
					if _, ok := assistant[other]; other != field && ok {
						t.Errorf("unexpected %s in replay = %v", other, assistant)
					}
				}
			}
		})
	}
}

func TestStreamMixedReasoningWithEncryptedReplay(t *testing.T) {
	t.Parallel()
	sse := `data: {"choices":[{"delta":{"reasoning":"scalar"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"detail"},{"type":"reasoning.encrypted","id":"e1","data":"d1"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	resp, err := ExtractRecordedResponse([]byte(sse), "m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{}, false)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := out[0]["reasoning_details"].([]map[string]any)
	if !ok || len(items) != 2 || items[0]["type"] != "reasoning.text" || items[0]["text"] != "scalardetail" ||
		items[1]["type"] != "reasoning.encrypted" || items[1]["id"] != "e1" || items[1]["data"] != "d1" {
		t.Fatalf("encrypted replay = %v, want combined display and original encrypted item", out[0])
	}
	for _, field := range llm.OpenAICompatReasoningFields() {
		if _, ok := out[0][field]; ok {
			t.Errorf("unexpected %s in encrypted replay = %v", field, out[0])
		}
	}
}

// TestStreamEncryptedDetailsRideThinkingPart pins the accumulation of opaque
// encrypted reasoning items: they survive to the settled thinking part's
// EncryptedContent, verbatim.
func TestStreamEncryptedDetailsRideThinkingPart(t *testing.T) {
	t.Parallel()
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[" +
		`{"type":"reasoning.encrypted","id":"e1","data":"d1"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	var final *llm.Response
	for _, ev := range events {
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("no finish event")
	}
	var thinking *llm.ThinkingData
	for _, p := range final.Message.Content {
		if p.Kind == llm.ContentThinking {
			thinking = p.Thinking
		}
	}
	if thinking == nil {
		t.Fatalf("content = %+v, want a thinking part carrying the encrypted items", final.Message.Content)
	}
	var want []map[string]any
	if err := json.Unmarshal([]byte(thinking.EncryptedContent), &want); err != nil || len(want) != 1 ||
		want[0]["type"] != "reasoning.encrypted" || want[0]["id"] != "e1" || want[0]["data"] != "d1" {
		t.Fatalf("EncryptedContent = %q (%v), want the verbatim item", thinking.EncryptedContent, err)
	}
}

// TestStreamMappedFinishReason pins the row's FinishReasonMap applying on the
// streaming settle path, not only in the non-streaming decode.
func TestStreamMappedFinishReason(t *testing.T) {
	t.Parallel()
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, func(c *registry.Caps) {
		c.FinishReasonMap = map[string]string{"stop": "end_turn"}
	})
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	for _, ev := range events {
		if ev.Type == llm.StreamEventFinish {
			if ev.Response.Finish.Reason != "end_turn" || ev.Response.Finish.Raw != "stop" {
				t.Fatalf("finish = %+v, want the mapped reason with the raw kept", ev.Response.Finish)
			}
			return
		}
	}
	t.Fatal("no finish event")
}

// TestStreamToolCallThoughtSignature survives the SSE delta state machine and
// lands on the settled response used to build the next assistant turn.
func TestStreamToolCallThoughtSignature(t *testing.T) {
	t.Parallel()
	sse := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_gemini","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"},"extra_content":{"google":{"thought_signature":"sig-1"}}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	for _, ev := range events {
		if ev.Type != llm.StreamEventFinish {
			continue
		}
		if ev.Response == nil {
			t.Fatal("finish event has no response")
		}
		calls := ev.Response.ToolCalls()
		if len(calls) != 1 || calls[0].ThoughtSignature != "sig-1" {
			t.Fatalf("tool calls = %+v, want one call with the Gemini signature", calls)
		}
		return
	}
	t.Fatal("no finish event")
}

// TestStreamInbandErrorWithoutMessage pins the message-less in-band error
// chunk: with only a code, the stream still ends typed with that status.
func TestStreamInbandErrorWithoutMessage(t *testing.T) {
	t.Parallel()
	sse := "data: {\"error\":{\"code\":429,\"type\":\"rate_limit_error\"}}\n\n"
	_, terminal := chatStreamEvents(t, sse, nil)
	if terminal == nil {
		t.Fatal("an error chunk without a message must still end the stream")
	}
	le, ok := errors.AsType[llm.Error](terminal)
	if !ok || le.StatusCode() != 429 {
		t.Fatalf("terminal error = %v, want a typed 429", terminal)
	}
}

// TestStreamChoiceLevelUsage pins the per-choice usage fallback on the settle:
// with no top-level usage, choices[0].usage becomes the final response usage.
func TestStreamChoiceLevelUsage(t *testing.T) {
	t.Parallel()
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}]}\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	for _, ev := range events {
		if ev.Type == llm.StreamEventFinish {
			if ev.Response.Usage.InputTokens != 7 || ev.Response.Usage.OutputTokens != 3 || ev.Response.Usage.TotalTokens != 10 {
				t.Fatalf("usage = %+v, want the per-choice numbers", ev.Response.Usage)
			}
			return
		}
	}
	t.Fatal("no finish event")
}

// TestStreamToolCallClosesReasoningOnce pins the reasoning→tool-call
// transition: the reasoning segment closes before the first tool-call start,
// and does not close a second time at [DONE].
func TestStreamToolCallClosesReasoningOnce(t *testing.T) {
	t.Parallel()
	sse := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"prelude\"}}]}\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_r","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	counts := countEvents(events)
	if counts[llm.StreamEventReasoningEnd] != 1 {
		t.Fatalf("reasoning end events = %d, want exactly one (at the tool-call open, not again at [DONE]): %+v",
			counts[llm.StreamEventReasoningEnd], events)
	}
	var order []llm.StreamEventType
	for _, ev := range events {
		if ev.Type == llm.StreamEventReasoningEnd || ev.Type == llm.StreamEventToolCallStart {
			order = append(order, ev.Type)
		}
	}
	if len(order) != 2 || order[0] != llm.StreamEventReasoningEnd || order[1] != llm.StreamEventToolCallStart {
		t.Fatalf("event order = %v, want reasoning end before tool call start", order)
	}
}
