package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

type replayText struct{ Type, Text string }

type replayItem struct {
	Type, ID, Phase  string
	Content, Summary []replayText
	Encrypted        string `json:"encrypted_content"`
}

// replayInput reads the next request after a transcript save and reload.
func replayInput(t *testing.T, msg llm.Message, replay bool) []replayItem {
	t.Helper()
	var saved llm.Message
	if err := json.Unmarshal([]byte(jsonOf(t, msg)), &saved); err != nil {
		t.Fatal(err)
	}
	res := resolved(openaiCaps)
	if replay {
		res.Caps.ReplayReasoningText = new(true)
	}
	body := build(t, llm.Request{Model: res.ModelID, Messages: []llm.Message{saved}}, res)
	var items []replayItem
	if err := json.Unmarshal([]byte(jsonOf(t, body["input"])), &items); err != nil {
		t.Fatal(err)
	}
	return items
}

// Some Responses-API gateways (lunaroute fronting GLM, observed live) stream
// reasoning as raw reasoning_text events on the reasoning item's content
// rather than as OpenAI's reasoning_summary events. The decoder must surface
// those as reasoning deltas or the UI shows nothing for minutes at a time
// while the model thinks. Two parts are sent so the part separator is
// asserted too.
func TestStreamEmitsReasoningTextDeltas(t *testing.T) {
	t.Parallel()
	const reasoningItem = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":" r3 "},{"type":"reasoning_text","text":"r4"}],"summary":[]}`
	const answerItem = `{"type":"message","content":[{"type":"output_text","text":"m1"}]}`
	var prefix strings.Builder
	for _, ev := range []string{
		`{"type":"response.reasoning_part.added","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"r1"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":" "}`,
		`{"type":"response.reasoning_part.added","item_id":"rs_1","output_index":0,"content_index":1,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":1,"delta":"r2"}`,
		`{"type":"response.output_text.delta","delta":"m1"}`,
	} {
		prefix.WriteString("data: " + ev + "\n\n")
	}
	for _, tc := range []struct {
		name    string
		done    []string
		output  []string
		wantRaw bool
	}{
		{"settled item", nil, []string{reasoningItem, answerItem}, true},
		{"completed items with empty terminal", []string{reasoningItem, answerItem}, nil, true},
		{"display deltas with settled answer", nil, []string{answerItem}, false},
		{"display deltas with empty terminal", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sse strings.Builder
			sse.WriteString(prefix.String())
			for _, item := range tc.done {
				sse.WriteString(`data: {"type":"response.output_item.done","item":` + item + "}\n\n")
			}
			sse.WriteString(`data: {"type":"response.completed","response":{"id":"resp_1","model":"glm-5.3","output":[` + strings.Join(tc.output, ",") + "]}}\n\n")
			srv, _ := server(t, 200, sse.String())
			stream, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close() //nolint:errcheck
			var live strings.Builder
			acc := llm.NewStreamAccumulator()
			for ev := range stream.Events() {
				acc.Process(ev)
				if ev.Type == llm.StreamEventReasoningDelta {
					live.WriteString(ev.ReasoningDelta)
				}
			}
			if got := live.String(); got != "r1 \n\nr2" {
				t.Fatalf("live reasoning = %q", got)
			}
			if acc.Response() == nil {
				t.Fatal("stream did not settle")
			}
			want := []replayItem{{Type: "message", Content: []replayText{{"output_text", "m1"}}}}
			if tc.wantRaw {
				item := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", " r3 "}, {"reasoning_text", "r4"}}, Summary: []replayText{}}
				want = append([]replayItem{item}, want...)
			}
			if got := replayInput(t, acc.Response().Message, true); !reflect.DeepEqual(got, want) {
				t.Fatalf("next input = %#v, want %#v", got, want)
			}
		})
	}
}

// A reasoning item that carries plain reasoning_text content (no
// encrypted_content) must still land in the settled message as a thinking
// part, or the transcript loses everything the model thought. Parts are
// joined with the same blank line the stream emits between them, so the
// settled text reads like the live view. A capable target also receives the
// original reasoning item in its place in the assistant turn.
func TestResponseContentFromOutputItems_KeepsReasoningTextContent(t *testing.T) {
	t.Parallel()
	var out []any
	if err := json.Unmarshal([]byte(`[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":" α\n\nβ "},{"type":"summary_text","text":"v2"},{"type":"reasoning_text","text":"\t"},{"type":"reasoning_text","text":"γ\n"}],"summary":[{"type":"summary_text","text":"s1"}]},
		{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"m1"}]},
		{"type":"function_call","name":"f","call_id":"call_1","arguments":"{}"},
		{"type":"reasoning","content":[{"type":"reasoning_text","text":"q2"}],"summary":[]},
		{"type":"reasoning","id":"rs_display","content":[{"type":"summary_text","text":"v3"}]},
		{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"m2"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out, nil)
	if len(content) != 6 {
		t.Fatalf("content parts = %d, want 6: %#v", len(content), content)
	}
	th := content[0]
	if th.Kind != llm.ContentThinking || th.Thinking == nil {
		t.Fatalf("content[0] = %#v, want thinking part", th)
	}
	if th.Thinking.Text != " α\n\nβ \n\nv2\n\n\t\n\nγ\n" {
		t.Fatalf("settled thinking text = %q", th.Thinking.Text)
	}
	if th.Thinking.ID != "rs_1" {
		t.Fatalf("thinking id = %q, want rs_1", th.Thinking.ID)
	}
	if th.Thinking.EncryptedContent != "" {
		t.Fatalf("encrypted content = %q, want empty", th.Thinking.EncryptedContent)
	}
	var record struct {
		Parts []replayText `json:"responses_text_parts"`
	}
	if err := json.Unmarshal([]byte(jsonOf(t, th.Thinking)), &record); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, p := range record.Parts {
		kinds = append(kinds, p.Type)
	}
	if want := []string{"reasoning_text", "summary_text", "reasoning_text", "reasoning_text"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("saved content types = %q, want %q", kinds, want)
	}
	msg := llm.Message{Role: llm.RoleAssistant, Content: content}
	items := replayInput(t, msg, true)
	want := []replayItem{
		{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", " α\n\nβ "}, {"reasoning_text", "\t"}, {"reasoning_text", "γ\n"}}, Summary: []replayText{{"summary_text", "s1"}}},
		{Type: "message", Phase: "commentary", Content: []replayText{{"output_text", "m1"}}},
		{Type: "function_call"},
		{Type: "reasoning", Content: []replayText{{"reasoning_text", "q2"}}, Summary: []replayText{}},
		{Type: "message", Phase: "final_answer", Content: []replayText{{"output_text", "m2"}}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("replayed items = %#v, want %#v", items, want)
	}
	for _, it := range replayInput(t, msg, false) {
		if it.Type == "reasoning" {
			t.Fatalf("strict target received raw reasoning: %+v", it)
		}
	}
}

func TestBuildBody_ReasoningReplaySources(t *testing.T) {
	t.Parallel()
	var msg llm.Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":[
		{"kind":"thinking","thinking":{"id":"rs_old","text":" r1\n\nr2 "}},
		{"kind":"thinking","thinking":{"text":"v1"}},
		{"kind":"thinking","thinking":{"id":"rs_s","summary":["s1"],"text":""}},
		{"kind":"thinking","thinking":{"id":"rs_a","signature":"sig_a","text":"v2"}},
		{"kind":"thinking","thinking":{"id":"rs_r","redacted":true,"text":"v3"}},
		{"kind":"redacted_thinking","thinking":{"id":"rs_k","text":"v4"}},
		{"kind":"thinking","thinking":{"id":"rs_c","encrypted_content":"[{\"type\":\"reasoning.encrypted\",\"data\":\"e1\"}]","text":"v5"}},
		{"kind":"thinking","thinking":{"id":"rs_e","encrypted_content":"e2","summary":["s2"],"text":"v6"}}
	]}`), &msg); err != nil {
		t.Fatal(err)
	}
	encrypted := replayItem{Type: "reasoning", ID: "rs_e", Encrypted: "e2", Summary: []replayText{{"summary_text", "s2"}}}
	for _, replay := range []bool{true, false} {
		want := []replayItem{encrypted}
		if replay {
			want = append([]replayItem{{Type: "reasoning", ID: "rs_old", Content: []replayText{{"reasoning_text", " r1\n\nr2 "}}, Summary: []replayText{}}}, want...)
		}
		if got := replayInput(t, msg, replay); !reflect.DeepEqual(got, want) {
			t.Errorf("replay %v: got %#v, want %#v", replay, got, want)
		}
	}
}
