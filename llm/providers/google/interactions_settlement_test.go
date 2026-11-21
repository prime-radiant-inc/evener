package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

func decodeJSONForTest(t *testing.T, body []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return raw
}

func runInteractionsSSETest(t *testing.T, sse string, instance, wireModel string, summariesDisabled bool) (llm.Response, []llm.StreamEvent, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httpResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	stream := llm.NewChanStream(cancel)
	go decodeInteractionStream(ctx, cancel, httpResp, stream, llm.Request{Model: wireModel}, instance, wireModel, "", llm.APILogCredentialMaterial{}, nil, summariesDisabled, nil)

	var events []llm.StreamEvent
	var final llm.Response
	var gotFinish bool
	var streamErr error
	for ev := range stream.Events() {
		events = append(events, ev)
		switch ev.Type {
		case llm.StreamEventFinish:
			if ev.Response != nil {
				final = *ev.Response
				gotFinish = true
			}
		case llm.StreamEventError:
			streamErr = ev.Err
		}
	}
	if !gotFinish && streamErr == nil {
		t.Fatalf("stream ended without Finish or Error; events=%v", events)
	}
	return final, events, streamErr
}

func buildSSE(events ...[2]string) string {
	var b strings.Builder
	for _, ev := range events {
		if ev[0] != "" {
			b.WriteString("event: ")
			b.WriteString(ev[0])
			b.WriteByte('\n')
		}
		b.WriteString("data: ")
		b.WriteString(ev[1])
		b.WriteString("\n\n")
	}
	return b.String()
}

func TestInteraction_JSONAndSSEParity_StepsCitationsAndMixedText(t *testing.T) {
	t.Parallel()

	// Gemini array thought summary + mixed text -> image -> text + google_search_call/result + url_citation + function_call.
	jsonBody := []byte(`{
		"id": "int_1",
		"model": "gemini-4-neon",
		"status": "completed",
		"steps": [
			{
				"type": "thought",
				"signature": "sig_t0",
				"summary": [{"type": "text", "text": "part one"}, {"type": "text", "text": "part two"}]
			},
			{
				"type": "google_search_call",
				"id": "sc_1",
				"arguments": {"queries": ["weather sf", "sf fog"]}
			},
			{
				"type": "google_search_result",
				"call_id": "sc_1",
				"result": {"url": "https://weather.example", "title": "Weather", "rendered_content": "<div>widget</div>"}
			},
			{
				"type": "model_output",
				"content": [
					{"type": "text", "text": "Hello ", "annotations": [{"type": "url_citation", "title": "Weather", "url": "https://weather.example"}]},
					{"type": "image", "mime_type": "image/png", "data": "AA=="},
					{"type": "text", "text": "world", "annotations": {"type": "url_citation", "url_citation": {"title": "Docs", "url": "https://docs.example"}}}
				]
			},
			{
				"type": "function_call",
				"id": "fc_1",
				"name": "lookup",
				"signature": "sig_fc1",
				"arguments": { "b" : 2 , "a" : 1 }
			}
		],
		"usage": {"total_input_tokens": 100, "total_output_tokens": 25, "total_tokens": 125}
	}`)

	resp, err := fromInteraction(decodeJSONForTest(t, jsonBody), "fallback-model", jsonBody, "gemini-prod", "gemini-4-neon")
	if err != nil {
		t.Fatalf("fromInteraction: %v", err)
	}
	if resp.ID != "int_1" || resp.Model != "gemini-4-neon" || resp.Finish.Reason != llm.FinishReasonToolCalls {
		t.Fatalf("unexpected header/finish: id=%q model=%q finish=%+v", resp.ID, resp.Model, resp.Finish)
	}

	parts := resp.Message.Content
	if len(parts) != 5 {
		t.Fatalf("content parts = %d, want 5: %+v", len(parts), parts)
	}
	// Part 0: thought
	if parts[0].Kind != llm.ContentThinking || parts[0].Thinking == nil ||
		!parts[0].Thinking.GoogleInteraction || parts[0].Thinking.GoogleInstance != "gemini-prod" || parts[0].Thinking.GoogleModel != "gemini-4-neon" ||
		parts[0].Thinking.Signature != "sig_t0" || parts[0].Thinking.Text != "part one\n\npart two" ||
		!reflect.DeepEqual(parts[0].Thinking.Summary, []string{"part one", "part two"}) {
		t.Fatalf("unexpected thinking part: %+v", parts[0].Thinking)
	}
	// Part 1: paired web search with normalized search_suggestions and attached citations
	if parts[1].Kind != llm.ContentWebSearch || parts[1].WebSearch == nil {
		t.Fatalf("expected ContentWebSearch at index 1, got %+v", parts[1])
	}
	if parts[1].WebSearch.Query != "weather sf; sf fog" {
		t.Fatalf("WebSearch.Query = %q, want %q", parts[1].WebSearch.Query, "weather sf; sf fog")
	}
	wantSources := []llm.WebSearchSource{
		{Title: "Weather", URL: "https://weather.example"},
		{Title: "Docs", URL: "https://docs.example"},
	}
	if !reflect.DeepEqual(parts[1].WebSearch.Sources, wantSources) {
		t.Fatalf("WebSearch.Sources = %+v, want %+v", parts[1].WebSearch.Sources, wantSources)
	}
	var rawSearchSteps []map[string]any
	if err := json.Unmarshal(parts[1].WebSearch.Raw, &rawSearchSteps); err != nil || len(rawSearchSteps) != 2 {
		t.Fatalf("WebSearch.Raw unmarshal failed (%v): %s", err, parts[1].WebSearch.Raw)
	}
	resArr, ok := rawSearchSteps[1]["result"].([]any)
	if !ok || len(resArr) != 1 {
		t.Fatalf("normalized google_search_result.result = %#v, want 1-element slice", rawSearchSteps[1]["result"])
	}
	if item, _ := resArr[0].(map[string]any); item["search_suggestions"] != "<div>widget</div>" {
		t.Fatalf("expected mirrored search_suggestions, got %#v", item)
	}
	// Parts 2 & 3: mixed text -> image -> text splits into two ContentText parts with GoogleStep == 4 (stepIdx 3 + 1)
	if parts[2].Kind != llm.ContentText || parts[2].Text != "Hello " || parts[2].GoogleStep != 4 {
		t.Fatalf("parts[2] = %+v, want text 'Hello ' GoogleStep=4", parts[2])
	}
	if parts[3].Kind != llm.ContentText || parts[3].Text != "world" || parts[3].GoogleStep != 4 {
		t.Fatalf("parts[3] = %+v, want text 'world' GoogleStep=4", parts[3])
	}
	// Part 4: function_call preserving spelling and Google provenance
	if parts[4].Kind != llm.ContentToolCall || parts[4].ToolCall == nil {
		t.Fatalf("parts[4] = %+v, want ContentToolCall", parts[4])
	}
	tc := parts[4].ToolCall
	if !tc.GoogleInteraction || tc.GoogleInstance != "gemini-prod" || tc.GoogleModel != "gemini-4-neon" ||
		tc.ID != "fc_1" || tc.Name != "lookup" || tc.ThoughtSignature != "sig_fc1" ||
		string(tc.Arguments) != `{"b":2,"a":1}` || tc.WireArguments() != `{ "b" : 2 , "a" : 1 }` {
		t.Fatalf("unexpected ToolCallData: %+v", tc)
	}

	// Now verify SSE parity for the same sequence (including Vertex singleton thought summary & top-level search_suggestions).
	sse := buildSSE(
		[2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_1","model":"gemini-4-neon","status":"in_progress"}}`},
		[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"thought","summary":{"text":"part one"}}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"part two"}}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_t0"}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
		[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"google_search_call","id":"sc_1","arguments":{"queries":["weather sf","sf fog"]}}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
		[2]string{"step.start", `{"event_type":"step.start","index":2,"step":{"type":"google_search_result","call_id":"sc_1"}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":2,"delta":{"type":"google_search_result","result":{"url":"https://weather.example","title":"Weather","rendered_content":"<div>widget</div>"}}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":2}`},
		[2]string{"step.start", `{"event_type":"step.start","index":3,"step":{"type":"model_output","content":[{"type":"text","text":"Hello ","annotations":{"type":"url_citation","title":"Weather","url":"https://weather.example"}}]}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":3,"delta":{"type":"image","mime_type":"image/png","data":"AA=="}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":3,"delta":{"type":"text","text":"world"}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":3,"delta":{"type":"text_annotation_delta","annotation":{"type":"url_citation","title":"Docs","url":"https://docs.example"}}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":3}`},
		[2]string{"step.start", `{"event_type":"step.start","index":4,"step":{"type":"function_call","id":"fc_1","name":"lookup","arguments":{}}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":4,"delta":{"type":"arguments_delta","partial_arguments":"{ \"b\" : 2 , \"a\" : 1 }"}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":4,"delta":{"type":"thought_signature","signature":"sig_fc1"}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":4}`},
		[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"int_1","model":"gemini-4-neon","status":"completed","usage":{"total_input_tokens":100,"total_output_tokens":25,"total_tokens":125}}}`},
		[2]string{"done", `[DONE]`},
	)
	sseResp, _, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
	if err != nil {
		t.Fatalf("SSE decode: %v", err)
	}
	if !reflect.DeepEqual(sseResp.Message.Content, resp.Message.Content) {
		t.Fatalf("SSE content differs from JSON:\nSSE:  %+v\nJSON: %+v", sseResp.Message.Content, resp.Message.Content)
	}

	// Verify top-level delta search_suggestions appends without dropping existing result items.
	sseTopSS := buildSSE(
		[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"google_search_result","call_id":"sc_top"}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"google_search_result","result":[{"url":"https://a.example","title":"A"}]}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"google_search_result","search_suggestions":"<div>top</div>"}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
		[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_ss","status":"completed"}}`},
	)
	topSSResp, _, err := runInteractionsSSETest(t, sseTopSS, "gemini-prod", "gemini-4-neon", false)
	if err != nil {
		t.Fatalf("top-level search_suggestions SSE: %v", err)
	}
	var topRaw []map[string]any
	if err := json.Unmarshal(topSSResp.Message.Content[0].WebSearch.Raw, &topRaw); err != nil || len(topRaw) != 1 {
		t.Fatalf("topRaw = %s (%v)", topSSResp.Message.Content[0].WebSearch.Raw, err)
	}
	if items, _ := topRaw[0]["result"].([]any); len(items) != 2 {
		t.Fatalf("expected existing item preserved + appended search_suggestions item, got %#v", items)
	}
}

func TestInteraction_CitationOnlyModelOutputInsertsWebSearchCarrier(t *testing.T) {
	t.Parallel()
	jsonBody := []byte(`{
		"id": "int_cite",
		"model": "gemini-4-neon",
		"status": "completed",
		"steps": [
			{
				"type": "model_output",
				"content": [
					{"type": "text", "text": "Grounded answer", "annotations": [{"type": "url_citation", "title": "Ref", "url": "https://ref.example"}]}
				]
			}
		]
	}`)
	resp, err := fromInteraction(decodeJSONForTest(t, jsonBody), "gemini-4-neon", jsonBody, "gemini-prod", "gemini-4-neon")
	if err != nil {
		t.Fatalf("fromInteraction: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("expected 2 parts (inserted ContentWebSearch before ContentText), got %+v", resp.Message.Content)
	}
	if resp.Message.Content[0].Kind != llm.ContentWebSearch || len(resp.Message.Content[0].WebSearch.Sources) != 1 || resp.Message.Content[0].WebSearch.Sources[0].URL != "https://ref.example" {
		t.Fatalf("expected standalone ContentWebSearch at index 0, got %+v", resp.Message.Content[0])
	}
	if resp.Message.Content[1].Kind != llm.ContentText || resp.Message.Content[1].Text != "Grounded answer" {
		t.Fatalf("expected ContentText at index 1, got %+v", resp.Message.Content[1])
	}
}

// Not parallel: swaps package-level rawArgsBodyDecode seam.
func TestInteraction_CaptureArgsRawRunsOnceAndPreservesInvalidBytes(t *testing.T) {
	calls := 0
	prev := rawArgsBodyDecode
	rawArgsBodyDecode = func(data []byte, v any) error {
		calls++
		return prev(data, v)
	}
	t.Cleanup(func() { rawArgsBodyDecode = prev })

	body := []byte(`{
		"id": "int_raw",
		"model": "gemini-4-neon",
		"status": "requires_action",
		"steps": [
			{"type": "function_call", "id": "c1", "name": "f1", "arguments": {"p": "` + "\xff" + `"}},
			{"type": "function_call", "id": "c2", "name": "f2", "arguments": { "k" : 1 }}
		]
	}`)
	resp, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
	if err != nil {
		t.Fatalf("fromInteraction: %v", err)
	}
	if calls != 1 {
		t.Fatalf("rawArgsBodyDecode called %d times, want 1", calls)
	}
	tc := resp.ToolCalls()
	if len(tc) != 2 {
		t.Fatalf("ToolCalls() = %d, want 2", len(tc))
	}
	if !bytes.Equal([]byte(tc[0].WireArguments()), []byte(`{"p": "`+"\xff"+`"}`)) {
		t.Fatalf("raw invalid UTF-8 bytes lost: % x", []byte(tc[0].WireArguments()))
	}
	if tc[1].WireArguments() != `{ "k" : 1 }` || string(tc[1].Arguments) != `{"k":1}` {
		t.Fatalf("non-canonical JSON spelling mismatch: args=%q wire=%q", tc[1].Arguments, tc[1].WireArguments())
	}
}

func TestInteraction_SSE_ToolCallPlaceholderVsDeltaConfirmedAndTerminalBackfill(t *testing.T) {
	t.Parallel()

	t.Run("step.start empty object placeholder defers ToolCallEnd until interaction.completed", func(t *testing.T) {
		sse := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"fc_0","name":"read_file","arguments":{}}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
			[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output","content":[{"type":"text","text":"after"}]}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i1","status":"requires_action","steps":[{"type":"function_call","id":"fc_0","name":"read_file","arguments":{"path":"a.go"}},{"type":"model_output","content":[{"type":"text","text":"after"}]}]}}`},
		)
		resp, events, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE decode: %v", err)
		}
		var types []llm.StreamEventType
		for _, ev := range events {
			types = append(types, ev.Type)
		}
		wantTypes := []llm.StreamEventType{
			llm.StreamEventToolCallStart,
			llm.StreamEventToolCallDelta,
			llm.StreamEventToolCallEnd,
			llm.StreamEventTextStart,
			llm.StreamEventTextDelta,
			llm.StreamEventTextEnd,
			llm.StreamEventFinish,
		}
		if !reflect.DeepEqual(types, wantTypes) {
			t.Fatalf("event order = %v, want %v", types, wantTypes)
		}
		if calls := resp.ToolCalls(); len(calls) != 1 || string(calls[0].Arguments) != `{"path":"a.go"}` {
			t.Fatalf("settled tool call = %+v", calls)
		}
	})

	t.Run("delta-confirmed empty object and non-empty step.start emit ToolCallEnd at step.stop", func(t *testing.T) {
		sse := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"fc_0","name":"ping","arguments":{}}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{}"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
			[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"function_call","id":"fc_1","name":"echo","arguments":{"x":1}}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i2","status":"requires_action"}}`},
		)
		resp, events, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE decode: %v", err)
		}
		if len(resp.ToolCalls()) != 2 || string(resp.ToolCalls()[0].Arguments) != `{}` || string(resp.ToolCalls()[1].Arguments) != `{"x":1}` {
			t.Fatalf("settled calls = %+v", resp.ToolCalls())
		}
		var endCount int
		for _, ev := range events {
			if ev.Type == llm.StreamEventToolCallEnd {
				endCount++
			}
		}
		if endCount != 2 {
			t.Fatalf("ToolCallEnd count = %d, want 2", endCount)
		}
	})

	t.Run("open unstopped function_call adopts complete terminal args or preserves malformed bytes", func(t *testing.T) {
		sseAdopted := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"fc_0","name":"write_file","arguments":{}}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"pa"}}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i3","status":"requires_action","steps":[{"type":"function_call","id":"fc_0","name":"write_file","arguments":{"path":"ok.txt"}}]}}`},
		)
		resp, _, err := runInteractionsSSETest(t, sseAdopted, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE adopted: %v", err)
		}
		if calls := resp.ToolCalls(); len(calls) != 1 || string(calls[0].Arguments) != `{"path":"ok.txt"}` {
			t.Fatalf("adopted calls = %+v", calls)
		}

		sseMalformed := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"fc_0","name":"write_file","arguments":{}}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"pa"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i4","status":"requires_action"}}`},
		)
		respMal, _, err := runInteractionsSSETest(t, sseMalformed, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE malformed: %v", err)
		}
		if calls := respMal.ToolCalls(); len(calls) != 1 || string(calls[0].Arguments) != `{"pa` || calls[0].RawArguments != "" {
			t.Fatalf("malformed calls = %+v, want verbatim Arguments='{\"pa' and RawArguments==''", calls)
		}
	})
}

func TestInteraction_SSE_SlotQueueSummariesAndUnstoppedSpans(t *testing.T) {
	t.Parallel()

	t.Run("mixed stream with unresolved step 0 and live step 1 drains in exact order", func(t *testing.T) {
		sse := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
			[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"second"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
			[2]string{"step.start", `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"fc_2","name":"run"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"cmd\":\"ls\"}"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":2}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_mix","status":"requires_action","steps":[{"type":"model_output","content":[{"type":"text","text":"first"}]},{"type":"model_output","content":[{"type":"text","text":"second"}]},{"type":"function_call","id":"fc_2","name":"run","arguments":{"cmd":"ls"}}]}}`},
		)
		resp, events, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE decode: %v", err)
		}
		var deltas []string
		var seq []llm.StreamEventType
		for _, ev := range events {
			seq = append(seq, ev.Type)
			if ev.Type == llm.StreamEventTextDelta {
				deltas = append(deltas, ev.Delta)
			}
		}
		if !reflect.DeepEqual(deltas, []string{"first", "second"}) {
			t.Fatalf("text deltas = %v, want [first second]", deltas)
		}
		if resp.Text() != "firstsecond" {
			t.Fatalf("resp.Text() = %q, want firstsecond", resp.Text())
		}
		wantSeq := []llm.StreamEventType{
			llm.StreamEventTextStart, llm.StreamEventTextDelta, llm.StreamEventTextEnd,
			llm.StreamEventTextStart, llm.StreamEventTextDelta, llm.StreamEventTextEnd,
			llm.StreamEventToolCallStart, llm.StreamEventToolCallDelta, llm.StreamEventToolCallEnd,
			llm.StreamEventFinish,
		}
		if !reflect.DeepEqual(seq, wantSeq) {
			t.Fatalf("event sequence = %v, want %v", seq, wantSeq)
		}
	})

	t.Run("summariesDisabled advances immediately at step.stop while summary-enabled signed thought waits", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pr, pw := io.Pipe()
		httpResp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: pr}
		stream := llm.NewChanStream(cancel)
		go decodeInteractionStream(ctx, cancel, httpResp, stream, llm.Request{Model: "gemini-4-neon"}, "gemini-prod", "gemini-4-neon", "", llm.APILogCredentialMaterial{}, nil, true, nil)

		go func() {
			_, _ = io.WriteString(pw, buildSSE(
				[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"thought","signature":"sig_none"}}`},
				[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
				[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`},
				[2]string{"step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"live_before_complete"}}`},
			))
		}()

		var sawLiveText bool
		for ev := range stream.Events() {
			if ev.Type == llm.StreamEventTextDelta && ev.Delta == "live_before_complete" {
				sawLiveText = true
				_, _ = io.WriteString(pw, buildSSE(
					[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
					[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_sum","status":"completed","steps":[{"type":"thought","signature":"sig_none","summary":[{"type":"text","text":"ignored"}]},{"type":"model_output","content":[{"type":"text","text":"live_before_complete"}]}]}}`},
				))
				_ = pw.Close()
			}
			if ev.Type == llm.StreamEventFinish {
				if len(ev.Response.Message.Content) != 2 || len(ev.Response.Message.Content[0].Thinking.Summary) != 0 {
					t.Fatalf("summariesDisabled must not backfill summary: %+v", ev.Response.Message.Content)
				}
			}
		}
		if !sawLiveText {
			t.Fatal("summariesDisabled stream failed to emit step 1 text delta before interaction.completed")
		}

		// With summariesDisabled=false, a signed thought without streamed summary waits until interaction.completed.
		sseEnabled := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"thought","signature":"sig_yes"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
			[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"answer"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":1}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_sum2","status":"completed","steps":[{"type":"thought","signature":"sig_yes","summary":[{"type":"text","text":"terminal summary"}]}]}}`},
		)
		resp2, events2, err := runInteractionsSSETest(t, sseEnabled, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("summary-enabled SSE: %v", err)
		}
		var seq []llm.StreamEventType
		for _, ev := range events2 {
			seq = append(seq, ev.Type)
		}
		wantSeq := []llm.StreamEventType{
			llm.StreamEventReasoningStart, llm.StreamEventReasoningDelta, llm.StreamEventReasoningEnd,
			llm.StreamEventTextStart, llm.StreamEventTextDelta, llm.StreamEventTextEnd,
			llm.StreamEventFinish,
		}
		if !reflect.DeepEqual(seq, wantSeq) {
			t.Fatalf("summary-enabled sequence = %v, want %v", seq, wantSeq)
		}
		if resp2.Message.Content[0].Thinking.Text != "terminal summary" {
			t.Fatalf("expected backfilled summary, got %+v", resp2.Message.Content[0].Thinking)
		}
	})

	t.Run("started text and reasoning spans without step.stop emit End before Finish without duplicating deltas", func(t *testing.T) {
		sse := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"thought"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"r1"}}}`},
			[2]string{"step.start", `{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"t1"}}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_unstopped","status":"completed"}}`},
		)
		resp, events, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("unstopped spans SSE: %v", err)
		}
		var seq []llm.StreamEventType
		for _, ev := range events {
			seq = append(seq, ev.Type)
		}
		wantSeq := []llm.StreamEventType{
			llm.StreamEventReasoningStart, llm.StreamEventReasoningDelta, llm.StreamEventReasoningEnd,
			llm.StreamEventTextStart, llm.StreamEventTextDelta, llm.StreamEventTextEnd,
			llm.StreamEventFinish,
		}
		if !reflect.DeepEqual(seq, wantSeq) {
			t.Fatalf("unstopped sequence = %v, want %v", seq, wantSeq)
		}
		if resp.Text() != "t1" {
			t.Fatalf("resp.Text() = %q, want t1", resp.Text())
		}
	})
}

func TestInteraction_StatusAndErrorClassification(t *testing.T) {
	t.Parallel()

	t.Run("incomplete/budget_exceeded/requires_continuation/cancelled retain continuation_token and map FinishReason", func(t *testing.T) {
		for _, tc := range []struct {
			status     string
			wantReason string
		}{
			{"incomplete", llm.FinishReasonLength},
			{"budget_exceeded", llm.FinishReasonLength},
			{"requires_continuation", llm.FinishReasonLength},
			{"cancelled", llm.FinishReasonOther},
		} {
			sse := buildSSE(
				[2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_c","status":"in_progress"}}`},
				[2]string{"interaction.status_update", `{"event_type":"interaction.status_update","interaction_id":"int_c","status":"` + tc.status + `","continuation_token":"tok_123"}`},
				[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"int_c","status":"` + tc.status + `"}}`},
			)
			resp, _, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
			if err != nil {
				t.Fatalf("status %q failed: %v", tc.status, err)
			}
			if resp.Finish.Reason != tc.wantReason || resp.Finish.Raw != tc.status {
				t.Fatalf("status %q finish = %+v, want reason %q", tc.status, resp.Finish, tc.wantReason)
			}
			if resp.Raw["continuation_token"] != "tok_123" {
				t.Fatalf("status %q lost continuation_token: %#v", tc.status, resp.Raw)
			}
		}
	})

	t.Run("terminal status empty output with gRPC code 4 returns permanent ErrTerminalStatusStepFailure", func(t *testing.T) {
		body := []byte(`{
			"id": "int_step_err",
			"model": "gemini-4-neon",
			"status": "budget_exceeded",
			"steps": [{"type": "model_output", "error": {"code": 4, "message": "deadline exceeded", "details": [{"type": "x"}]}}]
		}`)
		_, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
		if !errors.Is(err, ErrTerminalStatusStepFailure) {
			t.Fatalf("err = %T %v, want ErrTerminalStatusStepFailure", err, err)
		}
		if llm.Kind(err) != llm.KindTimeout {
			t.Fatalf("Kind(err) = %v, want KindTimeout via Unwrap", llm.Kind(err))
		}
		if llm.Classify(err) != llm.ErrorClassPermanent {
			t.Fatalf("Classify(err) = %v, want ErrorClassPermanent", llm.Classify(err))
		}
		var le llm.Error
		if !errors.As(err, &le) || le.StatusCode() != 0 || le.Retryable() {
			t.Fatalf("llm.Error mismatch: status=%d retryable=%v", le.StatusCode(), le.Retryable())
		}
		// Completed status with gRPC code 4 returns retryable KindTimeout directly.
		bodyCompleted := []byte(`{
			"id": "int_step_ok",
			"model": "gemini-4-neon",
			"status": "completed",
			"steps": [{"type": "model_output", "error": {"code": 4, "message": "deadline exceeded"}}]
		}`)
		_, errCompleted := fromInteraction(decodeJSONForTest(t, bodyCompleted), "gemini-4-neon", bodyCompleted, "gemini-prod", "gemini-4-neon")
		if errors.Is(errCompleted, ErrTerminalStatusStepFailure) || llm.Kind(errCompleted) != llm.KindTimeout || llm.Classify(errCompleted) != llm.ErrorClassRetryable {
			t.Fatalf("completed step gRPC 4 = %T %v (kind=%v class=%v), want retryable KindTimeout", errCompleted, errCompleted, llm.Kind(errCompleted), llm.Classify(errCompleted))
		}
	})

	t.Run("queued/in_progress and missing/unknown status and media-only output", func(t *testing.T) {
		for _, status := range []string{"queued", "in_progress"} {
			body := []byte(`{"id":"int_p","model":"gemini-4-neon","status":"` + status + `"}`)
			_, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
			if !errors.Is(err, ErrPendingInteractionStatus) || llm.Classify(err) != llm.ErrorClassPermanent {
				t.Fatalf("status %q err=%v class=%v, want permanent ErrPendingInteractionStatus", status, err, llm.Classify(err))
			}
			var le llm.Error
			if !errors.As(err, &le) || le.StatusCode() != 0 || le.Retryable() {
				t.Fatalf("pending status llm.Error = %v", le)
			}
			rewritten := llm.RewriteErrorProvider(err, "custom-inst")
			if rewrittenLE, ok := errors.AsType[llm.Error](rewritten); !ok || !errors.Is(rewritten, ErrPendingInteractionStatus) || rewrittenLE.Provider() != "custom-inst" {
				t.Fatalf("RewriteErrorProvider failed on ErrPendingInteractionStatus: %v", rewritten)
			}
		}

		// Premature SSE close with status == "queued" returns permanent ErrPendingInteractionStatus.
		sseQueued := buildSSE([2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_q","status":"queued"}}`})
		_, _, errQ := runInteractionsSSETest(t, sseQueued, "gemini-prod", "gemini-4-neon", false)
		if !errors.Is(errQ, ErrPendingInteractionStatus) || llm.Classify(errQ) != llm.ErrorClassPermanent {
			t.Fatalf("premature queued SSE = %v, want permanent ErrPendingInteractionStatus", errQ)
		}

		// Missing / unknown status -> ErrUnexpectedInteractionStatus.
		for _, rawStatus := range []string{`""`, `"mystery_state"`} {
			body := []byte(`{"id":"int_u","model":"gemini-4-neon","status":` + rawStatus + `}`)
			_, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
			if !errors.Is(err, ErrUnexpectedInteractionStatus) || llm.Classify(err) != llm.ErrorClassPermanent {
				t.Fatalf("status %s err=%v, want permanent ErrUnexpectedInteractionStatus", rawStatus, err)
			}
			rewritten := llm.RewriteErrorProvider(err, "custom-inst")
			if rewrittenLE, ok := errors.AsType[llm.Error](rewritten); !ok || !errors.Is(rewritten, ErrUnexpectedInteractionStatus) || rewrittenLE.Provider() != "custom-inst" {
				t.Fatalf("RewriteErrorProvider failed on ErrUnexpectedInteractionStatus: %v", rewritten)
			}
		}

		// Media-only output -> ConfigurationError.
		mediaOnly := []byte(`{"id":"int_m","model":"gemini-4-neon","status":"completed","steps":[{"type":"model_output","content":[{"type":"image","mime_type":"image/png","data":"AA=="}]}]}`)
		_, errMedia := fromInteraction(decodeJSONForTest(t, mediaOnly), "gemini-4-neon", mediaOnly, "gemini-prod", "gemini-4-neon")
		if _, ok := errors.AsType[*llm.ConfigurationError](errMedia); !ok {
			t.Fatalf("media-only err = %T %v, want *llm.ConfigurationError", errMedia, errMedia)
		}
	})

	t.Run("failed status and SSE error event classification", func(t *testing.T) {
		for _, tc := range []struct {
			code       string
			wantKind   llm.ErrorKind
			wantStatus int
			wantRetry  bool
		}{
			{"not_found", llm.KindNotFound, 404, false},
			{"permission_denied", llm.KindAccessDenied, 403, false},
			{"unauthenticated", llm.KindAuthentication, 401, false},
			{"gateway_timeout", llm.KindTimeout, 408, true},
			{"deadline_exceeded", llm.KindTimeout, 408, true},
			{"resource_exhausted", llm.KindRateLimit, 429, true},
			{"rate_limit_exceeded", llm.KindRateLimit, 429, true},
			{"internal", llm.KindServer, 503, true},
			{"unavailable", llm.KindServer, 503, true},
			{"invalid_argument", llm.KindInvalidRequest, 400, false},
			{"failed_precondition", llm.KindInvalidRequest, 400, false},
		} {
			body := []byte(`{"id":"int_f","status":"failed","errors":[{"code":"` + tc.code + `","message":"boom"}]}`)
			_, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
			var le llm.Error
			if !errors.As(err, &le) {
				t.Fatalf("code %q err = %T %v", tc.code, err, err)
			}
			if llm.Kind(err) != tc.wantKind || le.StatusCode() != tc.wantStatus || le.Retryable() != tc.wantRetry || le.ErrorCode() != tc.code {
				t.Fatalf("code %q: kind=%v status=%d retry=%v errCode=%q", tc.code, llm.Kind(err), le.StatusCode(), le.Retryable(), le.ErrorCode())
			}
		}

		// Safety failure settles as FinishReasonContentFilter.
		for _, safetyCode := range []string{"safety", "recitation", "prohibited_content", "blocklist"} {
			body := []byte(`{"id":"int_s","model":"gemini-4-neon","status":"failed","errors":[{"code":"` + safetyCode + `","message":"blocked"}]}`)
			resp, err := fromInteraction(decodeJSONForTest(t, body), "gemini-4-neon", body, "gemini-prod", "gemini-4-neon")
			if err != nil {
				t.Fatalf("safety code %q returned error: %v", safetyCode, err)
			}
			if resp.Finish.Reason != llm.FinishReasonContentFilter || resp.Finish.Raw != safetyCode {
				t.Fatalf("safety code %q finish = %+v", safetyCode, resp.Finish)
			}
		}

		// SSE error event retains payload and known interaction id.
		sseErr := buildSSE(
			[2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_sse_err","status":"in_progress"}}`},
			[2]string{"error", `{"event_type":"error","error":{"code":"not_found","message":"anchor missing"}}`},
		)
		_, _, err := runInteractionsSSETest(t, sseErr, "gemini-prod", "gemini-4-neon", false)
		var le llm.Error
		if !errors.As(err, &le) || llm.Kind(err) != llm.KindNotFound || le.StatusCode() != 404 || le.Retryable() {
			t.Fatalf("SSE error event = %T %v", err, err)
		}
		rawMap, _ := le.Raw().(map[string]any)
		if rawMap["interaction_id"] != "int_sse_err" {
			t.Fatalf("SSE error raw lost interaction_id: %#v", rawMap)
		}
	})
}

func TestInteraction_ParseInteractionUsage_8WayReconciliation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		raw        map[string]any
		wantIn     int
		wantOut    int
		wantTotal  int
		wantCached *int
		wantReason *int
	}{
		{
			name:      "100/50/25/125 subset tool-use",
			raw:       map[string]any{"total_input_tokens": 100, "total_tool_use_tokens": 50, "total_output_tokens": 25, "total_tokens": 125},
			wantIn:    100,
			wantOut:   25,
			wantTotal: 125,
		},
		{
			name:       "10/20/25/20/55 additive tool-use and subset thought",
			raw:        map[string]any{"total_input_tokens": 10, "total_tool_use_tokens": 20, "total_output_tokens": 25, "total_thought_tokens": 20, "total_tokens": 55},
			wantIn:     30,
			wantOut:    25,
			wantTotal:  55,
			wantReason: new(20),
		},
		{
			name:      "omitted total 10/20/25",
			raw:       map[string]any{"total_input_tokens": 10, "total_tool_use_tokens": 20, "total_output_tokens": 25},
			wantIn:    30,
			wantOut:   25,
			wantTotal: 55,
		},
		{
			name:       "thought > rawOut fallback 10/10/20/30 -> Output=30 Total=40",
			raw:        map[string]any{"total_input_tokens": 10, "total_output_tokens": 10, "total_thought_tokens": 20, "total_tokens": 30},
			wantIn:     10,
			wantOut:    30,
			wantTotal:  40,
			wantReason: new(20),
		},
		{
			name:       "additive cache 100/25/5/10/135",
			raw:        map[string]any{"total_input_tokens": 100, "total_output_tokens": 25, "total_thought_tokens": 5, "total_cached_tokens": 10, "total_tokens": 135},
			wantIn:     100,
			wantOut:    25,
			wantTotal:  135,
			wantCached: new(10),
			wantReason: new(5),
		},
		{
			name:       "cached subtraction 100/40/25/125",
			raw:        map[string]any{"total_input_tokens": 100, "total_cached_tokens": 40, "total_output_tokens": 25, "total_tokens": 125},
			wantIn:     60,
			wantOut:    25,
			wantTotal:  125,
			wantCached: new(40),
		},
		{
			name:       "additive tool-use and additive thought 10/20/25/20/75",
			raw:        map[string]any{"total_input_tokens": 10, "total_tool_use_tokens": 20, "total_output_tokens": 25, "total_thought_tokens": 20, "total_tokens": 75},
			wantIn:     30,
			wantOut:    45,
			wantTotal:  75,
			wantReason: new(20),
		},
		{
			name:       "omitted total with cached and thought 100/40/25/10",
			raw:        map[string]any{"total_input_tokens": 100, "total_cached_tokens": 40, "total_output_tokens": 25, "total_thought_tokens": 10},
			wantIn:     60,
			wantOut:    35,
			wantTotal:  135,
			wantCached: new(40),
			wantReason: new(10),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := parseInteractionUsage(tc.raw)
			if u.InputTokens != tc.wantIn || u.OutputTokens != tc.wantOut || u.TotalTokens != tc.wantTotal {
				t.Fatalf("usage = %+v, want in=%d out=%d total=%d", u, tc.wantIn, tc.wantOut, tc.wantTotal)
			}
			if !reflect.DeepEqual(u.CacheReadTokens, tc.wantCached) {
				t.Fatalf("CacheReadTokens = %v, want %v", u.CacheReadTokens, tc.wantCached)
			}
			if !reflect.DeepEqual(u.ReasoningTokens, tc.wantReason) {
				t.Fatalf("ReasoningTokens = %v, want %v", u.ReasoningTokens, tc.wantReason)
			}
		})
	}

	t.Run("StepStop.usage retained when interaction.completed omits usage", func(t *testing.T) {
		sse := buildSSE(
			[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`},
			[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"hi"}}`},
			[2]string{"step.stop", `{"event_type":"step.stop","index":0,"usage":{"total_input_tokens":100,"total_cached_tokens":40,"total_output_tokens":25,"total_tokens":125}}`},
			[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"i_u","status":"completed"}}`},
		)
		resp, _, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-4-neon", false)
		if err != nil {
			t.Fatalf("SSE decode: %v", err)
		}
		if resp.Usage.InputTokens != 60 || resp.Usage.OutputTokens != 25 || resp.Usage.TotalTokens != 125 || resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 40 {
			t.Fatalf("retained StepStop.usage = %+v", resp.Usage)
		}
	})
}

func TestExtractRecordedResponse_Interactions(t *testing.T) {
	t.Parallel()

	jsonBody := []byte(`{
		"id": "int_rec",
		"model": "gemini-4-neon",
		"status": "completed",
		"steps": [
			{"type": "thought", "signature": "sig_rec", "summary": [{"type": "text", "text": "plan"}]},
			{"type": "model_output", "content": [{"type": "text", "text": "done"}]},
			{"type": "function_call", "id": "fc_rec", "name": "write_file", "signature": "sig_fc", "arguments": {"path": "x"}}
		],
		"usage": {"total_input_tokens": 10, "total_output_tokens": 5, "total_tokens": 15}
	}`)
	resp, err := ExtractRecordedResponse(jsonBody, "fallback")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse JSON: %v", err)
	}
	if resp.Text() != "done" || len(resp.ToolCalls()) != 1 {
		t.Fatalf("recomputed JSON mismatch: text=%q calls=%d", resp.Text(), len(resp.ToolCalls()))
	}
	// Offline recompute leaves GoogleInstance == "" so it never replays as a live credential.
	if resp.Message.Content[0].Thinking.GoogleInstance != "" || resp.ToolCalls()[0].GoogleInstance != "" {
		t.Fatalf("offline recompute must leave GoogleInstance empty: thinking=%q tool=%q",
			resp.Message.Content[0].Thinking.GoogleInstance, resp.ToolCalls()[0].GoogleInstance)
	}

	sseBody := []byte(buildSSE(
		[2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_sse","model":"gemini-4-neon","status":"in_progress"}}`},
		[2]string{"step.start", `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`},
		[2]string{"step.delta", `{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"streamed"}}`},
		[2]string{"step.stop", `{"event_type":"step.stop","index":0}`},
		[2]string{"interaction.completed", `{"event_type":"interaction.completed","interaction":{"id":"int_sse","status":"completed"}}`},
	))
	sseResp, err := ExtractRecordedResponse(sseBody, "gemini-4-neon")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse SSE: %v", err)
	}
	if sseResp.Text() != "streamed" {
		t.Fatalf("recomputed SSE text = %q, want streamed", sseResp.Text())
	}

	// Mis-filed Chat Completions or generateContent JSON bodies must fail recompute.
	if _, err := ExtractRecordedResponse([]byte(`{"choices":[{"message":{"content":"x"}}]}`), "m"); err == nil {
		t.Fatal("expected error for mis-filed Chat Completions JSON")
	}
	if _, err := ExtractRecordedResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`), "m"); err == nil {
		t.Fatal("expected error for mis-filed generateContent JSON")
	}
}
