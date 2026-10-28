package responses

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// TestExtractRecordedResponse_SettlesAccumulatedTextAsStop pins the terminal
// settlement rule for a stream that accumulated only message items: with the
// terminal payload's output empty, the accumulated items become the settled
// content and the finish reason is a plain stop (not tool_calls, and not
// whatever an incomplete status would have said).
func TestExtractRecordedResponse_SettlesAccumulatedTextAsStop(t *testing.T) {
	body := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_st\",\"status\":\"completed\",\"output\":[]}}\n\n")
	resp, err := ExtractRecordedResponse(body, "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "hi" {
		t.Fatalf("Text() = %q, want the accumulated message text", resp.Text())
	}
	if resp.Finish.Reason != "stop" {
		t.Fatalf("finish = %+v, want a plain stop for text-only accumulation", resp.Finish)
	}
}

// TestExtractRecordedResponse_IncompleteDetailReasons pins the
// incomplete-details mapping: max_output_tokens stays length, a named
// content_filter is kept as-is, and any other reason becomes "other".
func TestExtractRecordedResponse_IncompleteDetailReasons(t *testing.T) {
	for _, tc := range []struct {
		reason, want string
	}{
		{"max_output_tokens", "length"},
		{"content_filter", "content_filter"},
		{"some_unmapped_reason", "other"},
	} {
		body := []byte(`{"id":"r","status":"incomplete","incomplete_details":{"reason":"` + tc.reason + `"}}`)
		resp, err := ExtractRecordedResponse(body, "m")
		if err != nil {
			t.Fatalf("ExtractRecordedResponse(%s): %v", tc.reason, err)
		}
		if resp.Finish.Reason != tc.want {
			t.Fatalf("reason %q mapped to %q, want %q", tc.reason, resp.Finish.Reason, tc.want)
		}
		if resp.Finish.Raw != "incomplete" {
			t.Fatalf("finish raw = %q, want the raw status", resp.Finish.Raw)
		}
	}
}

// TestExtractRecordedResponse_WebSearchCallItem pins the web_search_call
// output walk: the item's action.query reaches the part and the raw wire item
// round-trips for replay.
func TestExtractRecordedResponse_WebSearchCallItem(t *testing.T) {
	body := []byte(`{"id":"r","status":"completed","output":[{"type":"web_search_call","action":{"query":"fuzz q"}}]}`)
	resp, err := ExtractRecordedResponse(body, "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	var part *llm.WebSearchData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentWebSearch {
			part = p.WebSearch
		}
	}
	if part == nil {
		t.Fatalf("content = %+v, want a web-search part", resp.Message.Content)
	}
	if part.Query != "fuzz q" || len(part.Raw) == 0 {
		t.Fatalf("web search part = %+v, want the query and a raw payload", part)
	}
}

// TestExtractRecordedResponse_MalformedOutputEntries pins the tolerant walk:
// non-object output entries and non-object content entries are skipped without
// failing the decode, while the well-formed entry survives.
func TestExtractRecordedResponse_MalformedOutputEntries(t *testing.T) {
	body := []byte(`{"id":"r","status":"completed","output":["bare",{"type":"message","content":["bare",{"type":"output_text","text":"ok"}]}]}`)
	resp, err := ExtractRecordedResponse(body, "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.Text() != "ok" {
		t.Fatalf("Text() = %q, want the one decodable entry", resp.Text())
	}
}

// TestExtractRecordedResponse_ReasoningSummaryShapes pins the reasoning-item
// arms: non-array content and non-object or blank summary entries contribute
// nothing, and a content array's text items become the thinking text.
func TestExtractRecordedResponse_ReasoningSummaryShapes(t *testing.T) {
	body := []byte(`{"id":"r","status":"completed","output":[` +
		`{"type":"reasoning","content":"plain-text","summary":["str",{"text":"   "},{"text":"kept sum"}]},` +
		`{"type":"reasoning","id":"rs_2","content":["str",{"text":""},{"text":"t1"}],"summary":"not-an-array"}]}` +
		`]}`)
	resp, err := ExtractRecordedResponse(body, "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	var thinking []*llm.ThinkingData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking {
			thinking = append(thinking, p.Thinking)
		}
	}
	if len(thinking) != 1 {
		t.Fatalf("thinking parts = %d, want exactly the one with decodable text", len(thinking))
	}
	if thinking[0].Text != "t1" || thinking[0].ID != "rs_2" {
		t.Fatalf("thinking = %+v, want rs_2 with text t1", thinking[0])
	}
}
