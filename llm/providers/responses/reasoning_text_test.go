package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

type replayText struct{ Type, Text string }

type replayItem struct {
	Type, ID, Phase  string
	Content, Summary []replayText
	Encrypted        string `json:"encrypted_content"`
	CallID           string `json:"call_id"`
	Name, Arguments  string
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
	const reasoningItem = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":" r3 "},{"type":"reasoning_text","text":""},{"type":"reasoning_text","text":"r4"}],"summary":[]}`
	const emptyReasoning = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":""}],"summary":[]}`
	const blankReasoning = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":" "},{"type":"reasoning_text","text":"\t"}],"summary":[]}`
	const encryptedItem = `{"type":"reasoning","id":"rs_1","encrypted_content":"e1","content":[{"type":"reasoning_text","text":""}],"summary":[]}`
	const earlierItem = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"r_earlier"}]}`
	const otherItem = `{"type":"reasoning","id":"rs_other","content":[{"type":"reasoning_text","text":"r_other"}]}`
	const summaryItem = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s1"}]}`
	const displayItem = `{"type":"reasoning","id":"rs_1","content":[{"type":"summary_text","text":"s1"}]}`
	const earlierAnswer = `{"type":"message","content":[{"type":"output_text","text":"m_earlier"}]}`
	const answerItem = `{"type":"message","content":[{"type":"output_text","text":"m1"}]}`
	const answerDelta = `{"type":"response.output_text.delta","delta":"m1"}`
	const toolDelta = `{"type":"response.function_call_arguments.delta","name":"f","call_id":"call_1","delta":"{}"}`
	const toolItem = `{"type":"function_call","name":"f","call_id":"call_1","arguments":"{}"}`
	const otherAnswerDelta = `{"type":"response.output_text.delta","item_id":"msg_2","delta":"m2"}`
	const toolAdded = `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f"}}`
	const argumentsDone = `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{}"}`
	done := func(item string) string { return `{"type":"response.output_item.done","item":` + item + "}" }
	at := func(index int, event string) string {
		return `{"output_index":` + strconv.Itoa(index) + "," + event[1:]
	}
	const messageAdded = `{"type":"response.output_item.added","item":{"type":"message","id":"msg_2","phase":"commentary"}}`
	const reasoningAdded = `{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`
	const emptyCommentary = `{"type":"message","id":"msg_2","phase":"commentary","content":[{"type":"output_text","text":""}]}`
	var prefix strings.Builder
	for _, ev := range []string{
		`{"type":"response.reasoning_part.added","item_id":"rs_1","content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":"r1"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":0,"delta":" "}`,
		`{"type":"response.reasoning_part.added","item_id":"rs_1","content_index":1,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","content_index":1,"delta":"r2"}`,
	} {
		prefix.WriteString("data: " + ev + "\n\n")
	}
	answer := replayItem{Type: "message", Content: []replayText{{"output_text", "m1"}}}
	otherAnswer := replayItem{Type: "message", Content: []replayText{{"output_text", "m2"}}}
	commentary := otherAnswer
	commentary.Phase = "commentary"
	raw := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", " r3 "}, {"reasoning_text", ""}, {"reasoning_text", "r4"}}, Summary: []replayText{}}
	rawWithSummary := raw
	rawWithSummary.Summary = []replayText{{"summary_text", "s1"}}
	encrypted := replayItem{Type: "reasoning", ID: "rs_1", Encrypted: "e1", Summary: []replayText{}}
	tool := replayItem{Type: "function_call", CallID: "call_1", Name: "f", Arguments: "{}"}
	for _, tc := range []struct {
		name   string
		events []string
		output []string
		want   []replayItem
	}{
		{"settled item", nil, []string{reasoningItem, answerItem}, []replayItem{raw, answer}},
		{"completed items with empty terminal", []string{done(reasoningItem), done(answerItem)}, nil, []replayItem{raw, answer}},
		{"display deltas with settled answer", nil, []string{answerItem}, []replayItem{answer}},
		{"display deltas with empty terminal", []string{answerDelta}, nil, []replayItem{answer}},
		{"streamed answer with empty terminal reasoning", []string{answerDelta}, []string{emptyReasoning}, []replayItem{answer}},
		{"streamed answer with blank terminal reasoning", []string{answerDelta}, []string{blankReasoning}, []replayItem{answer}},
		{"reasoning only", []string{done(reasoningItem)}, []string{emptyReasoning}, []replayItem{raw}},
		{"display-only terminal part with streamed answer", []string{done(reasoningItem), answerDelta}, []string{displayItem}, []replayItem{raw, answer}},
		{"reasoning with streamed answer and tool", []string{done(reasoningItem), answerDelta, toolDelta}, []string{emptyReasoning}, []replayItem{raw, answer, tool}},
		{"reasoning with completed siblings", []string{answerDelta, done(answerItem), done(reasoningItem), toolDelta, done(toolItem)}, []string{emptyReasoning}, []replayItem{answer, raw, tool}},
		{"distinct streamed message and completed arguments", []string{done(answerItem), done(reasoningItem), otherAnswerDelta, toolAdded, argumentsDone}, []string{emptyReasoning}, []replayItem{answer, raw, otherAnswer, tool}},
		{"completed reasoning with streamed answer and empty terminal", []string{done(reasoningItem), answerDelta}, nil, []replayItem{raw, answer}},
		{"encrypted reasoning with empty text", nil, []string{encryptedItem, answerItem}, []replayItem{encrypted, answer}},
		{"matching completed reasoning and terminal answer", []string{done(reasoningItem), done(earlierAnswer)}, []string{emptyReasoning, answerItem}, []replayItem{raw, answer}},
		{"matching item after answer keeps terminal summary", []string{done(reasoningItem)}, []string{answerItem, summaryItem}, []replayItem{answer, rawWithSummary}},
		{"different completed reasoning", []string{done(otherItem)}, []string{emptyReasoning, answerItem}, []replayItem{answer}},
		{"completed summary only", []string{done(summaryItem)}, []string{emptyReasoning, answerItem}, []replayItem{answer}},
		{"terminal raw reasoning wins", []string{done(earlierItem)}, []string{reasoningItem, answerItem}, []replayItem{raw, answer}},
		{"terminal encrypted reasoning wins", []string{done(reasoningItem)}, []string{encryptedItem, answerItem}, []replayItem{encrypted, answer}},
		{"completed encrypted reasoning survives a placeholder", []string{done(encryptedItem)}, []string{blankReasoning, answerItem}, []replayItem{encrypted, answer}},
		{"placeholder with completed answer only", []string{done(answerItem)}, []string{emptyReasoning}, []replayItem{answer}},
		{"unmatched reasoning leaves completed answer and tool", []string{done(otherItem), done(answerItem), done(toolItem)}, []string{summaryItem}, []replayItem{answer, tool}},
		{"indexed delta before completed reasoning", []string{at(1, done(reasoningItem)), at(0, otherAnswerDelta)}, []string{emptyReasoning}, []replayItem{otherAnswer, raw}},
		{"added positions order sparse text tool and reasoning", []string{at(2, reasoningAdded), at(1, toolAdded), at(0, messageAdded), done(reasoningItem), argumentsDone, otherAnswerDelta}, nil, []replayItem{commentary, tool, raw}},
		{"unindexed events preserve first appearance", []string{done(reasoningItem), otherAnswerDelta, done(toolItem)}, []string{emptyReasoning}, []replayItem{raw, otherAnswer, tool}},
		{"late message index retains its commentary delta", []string{done(reasoningItem), otherAnswerDelta, at(0, done(emptyCommentary))}, []string{emptyReasoning}, []replayItem{commentary, raw}},
		{"indexed tool delta before completed reasoning", []string{at(1, done(reasoningItem)), at(0, toolDelta)}, []string{emptyReasoning}, []replayItem{tool, raw}},
		{"substantive terminal owns its answer", []string{done(otherItem), done(earlierAnswer), toolDelta}, []string{answerItem}, []replayItem{answer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sse strings.Builder
			sse.WriteString(prefix.String())
			for _, event := range tc.events {
				sse.WriteString("data: " + event + "\n\n")
			}
			sse.WriteString(`data: {"type":"response.completed","response":{"id":"resp_1","model":"glm-5.3","output":[` + strings.Join(tc.output, ",") + "]}}\n\n")
			srv, _ := server(t, 200, sse.String())
			stream, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close() //nolint:errcheck
			var live strings.Builder
			var settled *llm.Response
			acc := llm.NewStreamAccumulator()
			for ev := range stream.Events() {
				acc.Process(ev)
				if ev.Type == llm.StreamEventReasoningDelta {
					live.WriteString(ev.ReasoningDelta)
				}
				if ev.Type == llm.StreamEventFinish {
					settled = ev.Response
				}
			}
			if got := live.String(); got != "r1 \n\nr2" {
				t.Fatalf("live reasoning = %q", got)
			}
			if acc.Response() == nil || settled == nil {
				t.Fatal("stream did not settle")
			}
			if got := replayInput(t, acc.Response().Message, true); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("next input = %#v, want %#v", got, tc.want)
			}
			recorded, err := ExtractRecordedResponse([]byte(sse.String()), "glm-5.3")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := replayInput(t, recorded.Message, true), replayInput(t, settled.Message, true); !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded input = %#v, live settled input = %#v", got, want)
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

	content := responseContentFromOutputItems(out, nil, nil, nil)
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
		{Type: "function_call", CallID: "call_1", Name: "f", Arguments: "{}"},
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
