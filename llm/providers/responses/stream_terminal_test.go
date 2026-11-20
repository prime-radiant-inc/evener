package responses

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
)

// settledBothEvents settles one SSE body through the live decoder and the recorded
// replay, returning the live events and both settled responses.
func settledBothEvents(t *testing.T, frames string) ([]llm.StreamEvent, []*llm.Response) {
	t.Helper()
	body := []byte("data: " + strings.ReplaceAll(frames, "\n\n", "\n\ndata: ") + "\n\n")
	events, live := responsesEventsFromSSE(t, string(body))
	recorded, err := ExtractRecordedResponse(body, "gpt-5.5") // both paths must settle the same body
	if err != nil {
		t.Fatal(err)
	}
	return events, []*llm.Response{live, &recorded}
}

func settledBoth(t *testing.T, frames string) []*llm.Response {
	_, responses := settledBothEvents(t, frames)
	return responses
}

func summary(r *llm.Response) string {
	var parts []string
	for _, p := range r.Message.Content {
		if p.ToolCall != nil {
			parts = append(parts, "call")
		} else if p.Thinking == nil {
			parts = append(parts, p.Text+"|"+p.Phase)
		} else if p.Thinking.ResponsesIncomplete {
			parts = append(parts, "incomplete("+p.Thinking.Text+map[bool]string{true: "s", false: "c"}[len(p.Thinking.ResponsesSummaryParts) > 0]+")")
		} else {
			parts = append(parts, "think("+p.Thinking.Text+"+"+strings.Join(p.Thinking.Summary, "+")+")")
		}
	}
	return r.Finish.Reason + " " + strings.Join(parts, " ")
}

// A terminal payload settles only what the stream settled, whatever its own output holds.
func TestSettledResponsesFromEmptyTerminalOutput(t *testing.T) {
	t.Parallel()
	const nl, open, done = "\n\n", `{"type":"response.function_call_arguments.delta","call_id":"call_open","name":"probe","delta":"{}"}`, `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`

	for want, frames := range map[string]string{
		"content_filter ": `{"type":"response.output_text.delta","output_index":0,"delta":"filtered answer"}` + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`,
		"length incomplete(truncated thoughts) Hello world|final_answer": `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"truncated thought"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"Hello"}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":" world"}` + nl + open + nl + `{"type":"response.output_item.done","output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":""}]}}` + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`,
		"stop one|commentary two!|final_answer ten|commentary":           `{"type":"response.output_item.added","output_index":2,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":2,"delta":"two"}` + nl + `{"type":"response.output_item.added","output_index":10,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":10,"delta":"ten"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"one"}` + nl + `{"type":"response.output_text.delta","output_index":2,"delta":"!"}` + nl + done,
		"length incomplete(cut offc)":                                    `{"type":"response.reasoning_text.delta","output_index":0,"delta":"cut off"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"incomplete"}}` + nl + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`,
		"stop think(think+)":                                             `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"think"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"completed","content":[{"type":"reasoning_text","text":"think"}]}}` + nl + done,
		"stop think(+think) answer|":                                     open + nl + `{"type":"response.output_text.delta","output_index":0,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		"stop think(one\n\ntwo+one+two)":                                 `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"one"}` + nl + `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"two"}` + nl + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","status":"completed"}}` + nl + done,
		"stop think(thought+think) answer|":                              `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"thought"}],"summary":[{"type":"summary_text","text":"think"}]}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		"stop first|commentary second|final_answer":                      `{"type":"response.output_item.added","item":{"type":"message","id":"m1","phase":"commentary"}}` + nl + `{"type":"response.output_item.added","item":{"type":"message","id":"m2","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","item_id":"m1","delta":"first"}` + nl + `{"type":"response.output_text.delta","item_id":"m2","delta":"second"}` + nl + done,
		"stop incomplete(thoughts) one|commentary two|final_answer":      `{"type":"response.reasoning_summary_part.added"}` + nl + `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + nl + `{"type":"response.output_item.added","output_index":5,"item":{"type":"message","phase":"final_answer"}}` + nl + `{"type":"response.output_text.delta","output_index":5,"delta":"two"}` + nl + `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","phase":"commentary"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"one"}` + nl + done,
		"stop think(+eye) answer|":                                       `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","encrypted_content":"opaque"}}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"eye"}]}]}}`,
		"tool_calls think(+think) call":                                  `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"completed"}}` + nl + `{"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}]}]}}`,
		// An unfinished call settles no answer, so the streamed text is recovered in its place.
		"stop think(+) incomplete(thoughts) omitted|":   `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"omitted"}` + nl + `{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thought"}]},{"type":"function_call","call_id":"c","name":"probe","arguments":"{}","status":"incomplete"}]}}`,
		"length incomplete(thoughts) answer|commentary": `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","phase":"commentary","content":[{"type":"output_text","text":""}]}]}}`,
	} {
		for _, r := range settledBoth(t, frames) {
			if got := summary(r); got != want {
				t.Errorf("settled = %q, want %q", got, want)
			}
		}
	}
}

func TestRecoveredSummaryDisplaysAndReplaysAsSummary(t *testing.T) {
	t.Parallel()
	const frames = `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thought"}` + "\n\n" + `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed"}}` + "\n\n" + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + "\n\n" + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought"}]}]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		if got := jsonOf(t, items); r.ReasoningText() != "thought" || r.Text() != "answer" || !strings.Contains(got, `"summary_text"`) {
			t.Fatalf("reasoning %q, replay %s: want the summary shown and replayed as summary", r.ReasoningText(), got)
		}
	}
}

// An incomplete summary stays display-only: it shows as thought text, never as replayed content.
func TestIncompleteStreamedSummaryDoesNotReplay(t *testing.T) {
	t.Parallel()
	const frames = `{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"half"}` + "\n\n" + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		if th := r.Message.Content[0].Thinking; !th.ResponsesIncomplete || len(th.ResponsesSummaryParts) == 0 || r.ReasoningText() != "half" || len(items) != 0 {
			t.Fatalf("reasoning %+v, replay %s: want a display-only summary", th, jsonOf(t, items))
		}
	}
}

// Deltas named by item_id and a terminal repeating that id settle as one completed item.
func TestStreamedItemIDReconcilesWithTerminalRepeat(t *testing.T) {
	t.Parallel()
	const frames = `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"think"}` + "\n\n" + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + "\n\n" + `{"type":"response.completed","response":{"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]}]}}`
	for _, r := range settledBoth(t, frames) {
		var thinking []*llm.ThinkingData
		for _, p := range r.Message.Content {
			if p.Thinking != nil {
				thinking = append(thinking, p.Thinking)
			}
		}
		if len(thinking) != 1 || thinking[0].ID != "rs_1" || thinking[0].ResponsesIncomplete || r.Text() != "answer" {
			t.Fatalf("thinking = %+v, text %q: want one completed rs_1 beside the answer", thinking, r.Text())
		}
	}
}

// The terminal settles an answer only when it carries text, a refusal or a finished call. Otherwise the
// stream's answer is recovered, minus a call the terminal marks unfinished. Both paths must settle alike.
func TestSettledTerminalAnswersAndDisownedCalls(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const reason = `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},`
	const blob = `{"type":"reasoning","id":"rs_1","encrypted_content":"blob"}`
	const think = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]}`
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const call = `{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}`
	for name, c := range map[string]struct {
		frames, text, finish string
		calls                int
	}{
		// Reasoning alone settles no answer, so the stream's text is recovered beside it.
		"blob only":    {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `]}}`, "answer", "", 0},
		"summary only": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + think + `]}}`, "answer", "", 0},
		// The terminal settles its own answer, so the stream's is not recovered.
		"terminal text":    {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"output_text","text":"own"}]}]}}`, "own", "", 0},
		"terminal refusal": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal","text":"no"}]}]}}`, "", "", 0},
		// A wire refusal carries its text under "refusal"; an empty placeholder is no answer.
		"wire refusal":  {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}}`, "", "", 0},
		"empty refusal": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"message","content":[{"type":"refusal"}]}]}}`, "answer", "", 0},
		// The terminal disowns the streamed call, so recovery must not restore it.
		"disowned call": {`{"type":"response.output_item.done","item":` + call + `}` + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"incomplete"}]}}`, "", "", 0},
		// Recovery keeps a call the terminal does not disown and drops the one it does.
		"recovered call":       {`{"type":"response.output_item.done","item":` + call + `}` + nl + answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `]}}`, "answer", "", 1},
		"disowned with answer": {`{"type":"response.output_item.done","item":` + call + `}` + nl + answer + nl + `{"type":"response.incomplete",` + reason + `"output":[` + blob + `,{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"in_progress"}]}}`, "answer", "", 0},
		// A filtered answer was retracted, so nothing the stream sent is recovered.
		"filtered":                {answer + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`, "", "", 0},
		"filtered with reasoning": {answer + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[` + blob + `]}}`, "", "", 0},
		// Terminal reasoning content is no answer either, so the stream's text is still recovered.
		"terminal reasoning": {answer + nl + `{"type":"response.incomplete",` + reason + `"output":[{"type":"reasoning","id":"rs_2","content":[{"type":"reasoning_text","text":"thought"}]}]}}`, "answer", "", 0},
		// A completed response keeps the call the stream finished, even though the terminal lists it
		// unfinished: only an incomplete response may disown a call.
		"settled keeps call": {`{"type":"response.output_item.done","item":` + call + `}` + nl + `{"type":"response.completed","response":{"id":"r","status":"completed","output":[` + blob + `,{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}","status":"incomplete"}]}}`, "", "tool_calls", 1},
	} {
		for _, r := range settledBoth(t, c.frames) {
			if got := r.Text(); got != c.text {
				t.Errorf("%s: settled text %q, want %q", name, got, c.text)
			}
			if calls := len(r.ToolCalls()); calls != c.calls {
				t.Errorf("%s: settled %d tool calls, want %d", name, calls, c.calls)
			}
			if c.finish != "" && r.Finish.Reason != c.finish {
				t.Errorf("%s: settled finish %q, want %q", name, r.Finish.Reason, c.finish)
			}
		}
	}
}

// A reasoning item the terminal repeats by ID lands once, keeping the native content the repeat
// omitted, and a search both the terminal and the stream reported is not duplicated by recovery.
func TestSettledTerminalRepeatKeepsNativeReasoning(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"native step"}]}}`
	const search = `{"type":"web_search_call","id":"ws_1","action":{"query":"q"}}`
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const report = `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},`
	frames := done + nl + `{"type":"response.output_item.done","item":` + search + `}` + nl + answer + nl +
		`{"type":"response.incomplete",` + report + `"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"short summary"}]},` + search + `]}}`
	for _, r := range settledBoth(t, frames) {
		search := slices.DeleteFunc(slices.Clone(r.Message.Content), func(p llm.ContentPart) bool { return p.Kind != llm.ContentWebSearch })
		thinking := slices.DeleteFunc(slices.Clone(r.Message.Content), func(p llm.ContentPart) bool { return p.Thinking == nil })
		if r.Text() != "answer" || len(search) != 1 || len(thinking) != 1 || thinking[0].Thinking.Text != "native step" {
			t.Errorf("settled %+v, want the recovered answer and one reasoning item keeping the native text", r.Message.Content)
		}
	}
}

// Recovery keeps a phase the terminal stated on an empty message, and a search only it reported.
func TestRecoveryKeepsTerminalPhaseAndOnlySearch(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const search = `{"type":"web_search_call","id":"ws_1","action":{"query":"q"}}`
	const terminal = `"response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},`
	frames := answer + nl + `{"type":"response.incomplete",` + terminal +
		`"output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}]},{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":""}]},` + search + `]}}`
	for _, r := range settledBoth(t, frames) {
		search := slices.DeleteFunc(slices.Clone(r.Message.Content), func(p llm.ContentPart) bool { return p.Kind != llm.ContentWebSearch })
		text := slices.DeleteFunc(slices.Clone(r.Message.Content), func(p llm.ContentPart) bool { return p.Kind != llm.ContentText })
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		think := r.Message.Content[0].Thinking
		if len(search) != 1 || len(text) != 1 || text[0].Phase != "final_answer" || r.Text() != "answer" ||
			think == nil || !slices.Contains(think.Summary, "think") || !strings.Contains(jsonOf(t, items), `"web_search_call"`) {
			t.Errorf("settled %+v, replay %s: want one replayable search, the terminal phase and the recovered answer", r.Message.Content, jsonOf(t, items))
		}
	}
}

// A shared item ID never disowns a call whose own call_id the terminal leaves alone.
func TestDisownedCallComparesCallIDFirst(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const mine = `{"type":"response.output_item.done","item":{"type":"function_call","id":"item_1","call_id":"call_a","name":"probe","arguments":"{}"}}`
	const other = `{"type":"function_call","id":"item_1","call_id":"call_b","name":"probe","arguments":"{}","status":"incomplete"}`
	frames := mine + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + other + `]}}`
	for _, r := range settledBoth(t, frames) {
		if calls := r.ToolCalls(); len(calls) != 1 || calls[0].ID != "call_a" {
			t.Errorf("settled calls = %v, want the call the terminal never disowned", calls)
		}
	}
}

// A streamed call the provider named only by item ID carries that ID as a synthesized call_id.
// When an incomplete terminal repeats the same item with the provider's real call_id, the item
// ID still disowns the unfinished call; only a provider-issued call_id is compared as a call_id.
func TestIncompleteTerminalDisownsCallNamedOnlyByItemID(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_a","name":"probe"}}` + nl +
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_a","arguments":"{}"}}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_a","call_id":"call_real","name":"probe","arguments":"{}","status":"incomplete"}]}}`
	for _, r := range settledBoth(t, frames) {
		if calls := r.ToolCalls(); len(calls) != 0 {
			t.Errorf("settled calls = %v, want the unfinished item-ID call disowned", calls)
		}
		if got := r.Text(); got != "answer" {
			t.Errorf("settled text = %q, want the recovered answer", got)
		}
	}
}

// A call that opens by item ID and settles its real call_id at done keeps that issued id for
// settlement, so an incomplete terminal for a different call on the same item ID cannot suppress it.
func TestCompletedCallKeepsRealCallIDAgainstIncompleteSibling(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_a","name":"probe"}}` + nl +
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_a","call_id":"call_real","arguments":"{}"}}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_a","call_id":"call_other","name":"other","arguments":"{}","status":"incomplete"}]}}`
	for _, r := range settledBoth(t, frames) {
		if calls := r.ToolCalls(); len(calls) != 1 || calls[0].ID != "fc_a" || string(calls[0].Arguments) != "{}" {
			t.Errorf("settled calls = %v, want the completed item-ID call kept once", calls)
		}
		if got := r.Text(); got != "answer" {
			t.Errorf("settled text = %q, want the recovered answer", got)
		}
	}
}

// A response-wide incomplete status cuts off no thought the stream already completed, and the native
// parts still replay on the next request.
func TestIncompleteResponseKeepsCompletedStreamedReasoning(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","content":[{"type":"reasoning_text","text":"native"}],"summary":[{"type":"summary_text","text":"shown"}]}}`
	const answer = `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`
	const repeat = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"shown"}]}`
	frames := done + nl + answer + nl + `{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + repeat + `]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		th := r.Message.Content[0].Thinking
		if th == nil || th.ResponsesIncomplete || !strings.Contains(jsonOf(t, items), `"native"`) {
			t.Fatalf("reasoning %+v, replay %s: want the completed thought kept and replayed", th, jsonOf(t, items))
		}
	}
}

// An encrypted thought carrying typed summaries replays those parts, not only the deduped display text.
func TestEncryptedReasoningReplaysTypedSummaries(t *testing.T) {
	t.Parallel()
	msg := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
		ID:                    "rs_1",
		EncryptedContent:      "blob",
		Summary:               nil, // display deduplication emptied it
		ResponsesSummaryParts: []llm.ResponsesReasoningPart{{Type: "summary_text", Text: "shown"}},
	}}}}
	_, items, _ := toResponsesInput([]llm.Message{msg}, "", nil)
	if got := jsonOf(t, items); !strings.Contains(got, `"shown"`) {
		t.Fatalf("replay %s, want the typed summary kept", got)
	}
}

// Each recovered message takes its own terminal placeholder's phase, never the first one's.
func TestRecoveredMessagesTakeTheirOwnPhase(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const placeholder = `{"type":"message","content":[{"type":"output_text","text":""}],"phase":`
	frames := `{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m1"}}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"one"}` + nl +
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"message","id":"m2"}}` + nl +
		`{"type":"response.output_text.delta","output_index":2,"delta":"two"}` + nl +
		`{"type":"response.completed","response":{"status":"completed","output":[` + placeholder + `"commentary"},` + placeholder + `"final_answer"}]}}`
	for _, r := range settledBoth(t, frames) {
		if got := summary(r); got != "stop one|commentary two|final_answer" {
			t.Errorf("settled = %q, want each message on its own phase", got)
		}
	}
}

// A terminal placeholder an explicit item ID already names is reserved before the ordinal
// fallback runs, so an anonymous streamed message at that ordinal never claims its phase.
func TestAnonymousMessageNeverClaimsIDMatchedPhase(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.output_text.delta","output_index":0,"delta":"one"}` + nl +
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m2"}}` + nl +
		`{"type":"response.output_text.delta","item_id":"m2","output_index":1,"delta":"two"}` + nl +
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m2","phase":"final_answer","content":[{"type":"output_text","text":""}]}]}}`
	for _, r := range settledBoth(t, frames) {
		if got := summary(r); got != "stop one| two|final_answer" {
			t.Errorf("settled = %q, want the anonymous message unphased and m2 on final_answer", got)
		}
	}
}

// A streamed message keeps a phase the stream already established, conflicting terminal IDs never
// steal another message's phase, and an unmatched terminal web search is retained with its Raw bytes.
func TestRecoveredMessagesPreserveStreamPhaseAndRetainTerminalSearch(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const search = `{"type":"web_search_call","id":"ws_term","action":{"query":"go"}}`
	frames := `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","phase":"commentary"}}` + nl +
		`{"type":"response.output_text.delta","output_index":0,"delta":"one"}` + nl +
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"m2"}}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"two"}` + nl +
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"m1","phase":"final_answer","content":[{"type":"output_text","text":""}]},` + search + `]}}`
	for _, r := range settledBoth(t, frames) {
		var texts []string
		var searches []*llm.WebSearchData
		for _, p := range r.Message.Content {
			switch p.Kind {
			case llm.ContentText:
				texts = append(texts, p.Text+"|"+p.Phase)
			case llm.ContentWebSearch:
				searches = append(searches, p.WebSearch)
			}
		}
		if !slices.Equal(texts, []string{"one|commentary", "two|"}) ||
			len(searches) != 1 || searches[0] == nil || searches[0].Query != "go" || len(searches[0].Raw) == 0 {
			t.Fatalf("texts = %v, searches = %+v: want m1 to keep commentary, m2 unphased, and terminal search retained", texts, searches)
		}
	}
}

// A thought the stream closed as incomplete stays cut off when the terminal repeats it without a status,
// so it never replays.
func TestExplicitIncompleteThoughtStaysCutOff(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"incomplete","content":[{"type":"reasoning_text","text":"half"}]}}`
	const repeat = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"half"}]}`
	frames := done + nl + `{"type":"response.completed","response":{"id":"r","status":"completed","output":[` + repeat + `]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		th := r.Message.Content[0].Thinking
		if th == nil || !th.ResponsesIncomplete || len(items) != 0 {
			t.Fatalf("reasoning %+v, replay %s: want the cut-off thought kept out of replay", th, jsonOf(t, items))
		}
	}
}

// When the terminal explicitly marks a cut-off thought completed, it clears ResponsesIncomplete.
func TestExplicitIncompleteThoughtClearsWhenTerminalCompleted(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"incomplete","content":[{"type":"reasoning_text","text":"half"}]}}`
	const completed = `{"type":"reasoning","id":"rs_1","status":"completed","summary":[{"type":"summary_text","text":"full"}]}`
	frames := done + nl + `{"type":"response.completed","response":{"id":"r","status":"completed","output":[` + completed + `]}}`
	for _, r := range settledBoth(t, frames) {
		th := r.Message.Content[0].Thinking
		if th == nil || th.ResponsesIncomplete {
			t.Fatalf("reasoning %+v: want terminal status=completed to settle thought", th)
		}
	}
}

// A status-less output_item.done reasoning item stays completed even at max_output_tokens.
func TestStatuslessDoneReasoningStaysCompletedAtIncompleteTerminal(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"finished thought"}]}}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		th := r.Message.Content[0].Thinking
		if th == nil || th.ResponsesIncomplete || !strings.Contains(jsonOf(t, items), `"finished thought"`) {
			t.Fatalf("reasoning %+v, replay %s: want status-less done reasoning kept completed", th, jsonOf(t, items))
		}
	}
}

// Delta-only reasoning repeated by ID without a status at max_output_tokens settles as completed
// with the terminal summary, while terminal-only status-less items still stay incomplete.
func TestDeltaOnlyReasoningWithStatuslessRepeatAtIncompleteTerminal(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.reasoning_summary_text.delta","item_id":"rs_delta","delta":"think"}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"rs_delta","summary":[{"type":"summary_text","text":"think"}]}]}}`
	for _, r := range settledBoth(t, frames) {
		_, items, _ := toResponsesInput([]llm.Message{r.Message}, "", nil)
		th := r.Message.Content[0].Thinking
		if th == nil || th.ID != "rs_delta" || th.ResponsesIncomplete ||
			len(items) != 2 || !strings.Contains(jsonOf(t, items), `"rs_delta"`) || !strings.Contains(jsonOf(t, items), `"think"`) {
			t.Fatalf("reasoning %+v, replay %s: want delta-only item settled as completed with terminal summary", th, jsonOf(t, items))
		}
	}

	// Native reasoning_text deltas + terminal summary repeat keep both native content and terminal summary.
	nativeFrames := `{"type":"response.reasoning_text.delta","item_id":"rs_native","delta":"native step"}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"rs_native","summary":[{"type":"summary_text","text":"sum"}]}]}}`
	for _, r := range settledBoth(t, nativeFrames) {
		th := r.Message.Content[0].Thinking
		if th == nil || th.ResponsesIncomplete || len(th.ResponsesContentParts) != 1 || th.ResponsesContentParts[0].Text != "native step" ||
			len(th.ResponsesSummaryParts) != 1 || th.ResponsesSummaryParts[0].Text != "sum" {
			t.Fatalf("native+summary reasoning = %+v, want both native content and terminal summary kept completed", th)
		}
	}

	// Empty terminal summary [] falls back to streamed deltas instead of dropping the thought.
	emptySummaryFrames := `{"type":"response.reasoning_text.delta","item_id":"rs_empty","delta":"native kept"}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"reasoning","id":"rs_empty","summary":[]}]}}`
	for _, r := range settledBoth(t, emptySummaryFrames) {
		th := r.Message.Content[0].Thinking
		if th == nil || th.ResponsesIncomplete || th.Text != "native kept" ||
			len(th.ResponsesContentParts) != 1 || th.ResponsesContentParts[0].Text != "native kept" {
			t.Fatalf("empty-summary reasoning = %+v, want streamed native content preserved", th)
		}
	}
}

// A terminal-only status-less reasoning item stays incomplete at max_output_tokens, while an
// already-completed streamed reasoning item keeps its completed status when merged by ID.
func TestTerminalOnlyStatuslessReasoningAtIncompleteStaysIncomplete(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const done = `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_done","status":"completed","content":[{"type":"reasoning_text","text":"done"}]}}`
	const termOnly = `{"type":"reasoning","id":"rs_term","summary":[{"type":"summary_text","text":"term"}]}`
	const repeat = `{"type":"reasoning","id":"rs_done","summary":[{"type":"summary_text","text":"done"}]}`
	frames := done + nl + `{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + termOnly + `,` + repeat + `]}}`
	for _, r := range settledBoth(t, frames) {
		byID := map[string]*llm.ThinkingData{}
		for _, p := range r.Message.Content {
			if p.Thinking != nil {
				byID[p.Thinking.ID] = p.Thinking
			}
		}
		if byID["rs_term"] == nil || !byID["rs_term"].ResponsesIncomplete {
			t.Fatalf("rs_term = %+v, want terminal-only status-less item marked incomplete", byID["rs_term"])
		}
		if byID["rs_done"] == nil || byID["rs_done"].ResponsesIncomplete {
			t.Fatalf("rs_done = %+v, want completed streamed item kept completed", byID["rs_done"])
		}
	}
}

// An unmatched terminal empty message carrying a non-empty phase is retained during recovery.
func TestUnmatchedTerminalPhasedMessageIsRetained(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"c1","name":"probe","arguments":"{}"}}` + nl +
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","phase":"commentary","content":[{"type":"output_text","text":""}]}]}}`
	for _, r := range settledBoth(t, frames) {
		if got := summary(r); got != "tool_calls call |commentary" {
			t.Errorf("settled = %q, want call and unmatched phased message retained", got)
		}
	}
}

// Distinct web_search_call wire IDs are never deduplicated even when they share a query.
func TestTerminalWebSearchDeduplicatesByWireID(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	const s1 = `{"type":"web_search_call","id":"ws_1","action":{"query":"go"}}`
	const s2 = `{"type":"web_search_call","id":"ws_2","action":{"query":"go"}}`
	const s1Updated = `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"query":"go"}}`
	frames := `{"type":"response.output_item.done","item":` + s1 + `}` + nl +
		`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
		`{"type":"response.completed","response":{"status":"completed","output":[` + s1Updated + `,` + s2 + `]}}`
	for _, r := range settledBoth(t, frames) {
		var ids []string
		for _, p := range r.Message.Content {
			if p.Kind == llm.ContentWebSearch && p.WebSearch != nil {
				var item struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(p.WebSearch.Raw, &item)
				ids = append(ids, item.ID)
			}
		}
		if !slices.Equal(ids, []string{"ws_1", "ws_2"}) {
			t.Fatalf("web search ids = %v, want [ws_1 ws_2]", ids)
		}
	}
}

// An unindexed web_search_call keeps its stream order relative to indexed items.
func TestRecoveredOutputKeepsUnindexedWebSearchOrder(t *testing.T) {
	t.Parallel()
	acc := newResponsesOutputAccumulator()
	acc.HandleOutputItemDone(map[string]any{
		"output_index": 1,
		"item":         map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "first"}}},
	}, nil)
	acc.HandleOutputItemDone(map[string]any{
		"item": map[string]any{"type": "web_search_call", "action": map[string]any{"query": "second"}},
	}, nil)
	out, _ := acc.recoveredOutput(nil)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if typ, _ := out[0].(map[string]any)["type"].(string); typ != "message" {
		t.Fatalf("out[0] type = %q, want message to keep stream order ahead of unindexed web_search_call", typ)
	}
}

// Reasoning deltas carrying fragments in "text" emit live and settle identically on replay.
func TestReasoningDeltaTextFieldMatchesLiveAndRecorded(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	for _, typ := range []string{"response.reasoning_summary_text.delta", "response.reasoning_text.delta"} {
		frames := `{"type":"` + typ + `","output_index":0,"text":"via text"}` + nl +
			`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}` + nl +
			`{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`
		body := "data: " + strings.ReplaceAll(frames, "\n\n", "\n\ndata: ") + "\n\n"
		events, live := responsesEventsFromSSE(t, body)
		recorded, err := ExtractRecordedResponse([]byte(body), "gpt-5.5")
		if err != nil {
			t.Fatal(err)
		}
		var liveReasoning strings.Builder
		for _, ev := range events {
			if ev.Type == llm.StreamEventReasoningDelta {
				liveReasoning.WriteString(ev.ReasoningDelta)
			}
		}
		if liveReasoning.String() != "via text" {
			t.Errorf("%s: live reasoning = %q, want %q", typ, liveReasoning.String(), "via text")
		}
		if live.ReasoningText() != "via text" || recorded.ReasoningText() != "via text" {
			t.Errorf("%s: live=%q recorded=%q, want both %q", typ, live.ReasoningText(), recorded.ReasoningText(), "via text")
		}
	}
}

// Interleaved reasoning parts within one item route by summary_index / content_index without mixing.
func TestInterleavedReasoningPartsStaySeparate(t *testing.T) {
	t.Parallel()
	const nl = "\n\n"
	frames := `{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0}` + nl +
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":1}` + nl +
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":1,"delta":"b1"}` + nl +
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"a1"}` + nl +
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":1,"delta":"b2"}` + nl +
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"a2"}` + nl +
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed"}}` + nl +
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`
	for _, r := range settledBoth(t, frames) {
		if got := summary(r); got != "stop think(a1a2\n\nb1b2+a1a2+b1b2)" {
			t.Errorf("settled = %q, want interleaved summary_index parts kept separate and ordered", got)
		}
	}

	contentFrames := `{"type":"response.reasoning_part.added","item_id":"rs_2","output_index":0,"content_index":0}` + nl +
		`{"type":"response.reasoning_part.added","item_id":"rs_2","output_index":0,"content_index":1}` + nl +
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","output_index":0,"content_index":1,"delta":"d1"}` + nl +
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","output_index":0,"content_index":0,"delta":"c1"}` + nl +
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","output_index":0,"content_index":1,"delta":"d2"}` + nl +
		`{"type":"response.reasoning_text.delta","item_id":"rs_2","output_index":0,"content_index":0,"delta":"c2"}` + nl +
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_2","status":"completed"}}` + nl +
		`{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`
	want := []llm.ResponsesReasoningPart{
		{Type: "reasoning_text", Text: "c1c2"},
		{Type: "reasoning_text", Text: "d1d2"},
	}
	for _, r := range settledBoth(t, contentFrames) {
		th := r.Message.Content[0].Thinking
		if th == nil || !slices.Equal(th.ResponsesContentParts, want) {
			t.Fatalf("content parts = %+v, want %+v", th, want)
		}
	}
}

func TestIncompleteResponseKeepsUnsettledToolArgumentsOutOfFinalResponse(t *testing.T) {
	t.Parallel()
	const delta = `{"type":"response.function_call_arguments.delta","call_id":"call_unfinished","name":"probe","delta":"{}"}`
	const unfinished = `{"type":"function_call","call_id":"call_unfinished","name":"probe","arguments":"{}","status":"incomplete"}`
	const running = `{"type":"function_call","call_id":"call_running","name":"probe","arguments":"{}","status":"in_progress"}`
	const completed = `{"type":"function_call","call_id":"call_completed","name":"probe","arguments":"{}","status":"completed"}`
	const unstamped = `{"type":"function_call","call_id":"call_unstamped","name":"probe","arguments":"{}"}`
	frame := func(payload string) string { return "data: " + payload + "\n\n" }
	for _, terminalType := range []string{"response.completed", "response.incomplete"} {
		for _, mode := range []string{"empty", "terminal", "streamed", "both", "mismatch"} {
			t.Run(terminalType+"/"+mode, func(t *testing.T) {
				var wire strings.Builder
				wire.WriteString(frame(delta))
				output := "[]"
				var wantCalls []string
				if mode == "terminal" || mode == "both" || mode == "mismatch" {
					output = "[" + unfinished + "," + running + "," + completed + "," + unstamped + "]"
				}
				if mode == "streamed" || mode == "both" || mode == "mismatch" {
					items := []string{unfinished, running, completed, unstamped}
					if mode == "mismatch" {
						items = items[:3]
					}
					for _, item := range items {
						wire.WriteString(frame(`{"type":"response.output_item.done","item":` + item + `}`))
					}
				}
				if mode != "empty" {
					wantCalls = []string{"call_completed", "call_unstamped"}
				}
				wire.WriteString(frame(`{"type":"` + terminalType + `","response":{"status":"incomplete","output":` + output + `}}`))
				body := wire.String()
				recorded, err := ExtractRecordedResponse([]byte(body), "gpt-5.5")
				if err != nil {
					t.Error(err)
				}
				events, final := responsesEventsFromSSE(t, body)
				if event := toolEvent(t, events, llm.StreamEventToolCallDelta); event == nil || event.ID != "call_unfinished" || string(event.Arguments) != "{}" {
					t.Fatalf("transient tool delta = %+v", event)
				}
				for _, response := range []*llm.Response{final, &recorded} {
					if response == nil || response.Finish.Reason != llm.FinishReasonLength || response.Finish.Raw != "incomplete" {
						t.Fatalf("incomplete response = %+v", response)
					}
					var ids []string
					for _, call := range response.ToolCalls() {
						ids = append(ids, call.ID)
					}
					if !slices.Equal(ids, wantCalls) {
						t.Errorf("settled tool calls = %v, want %v", ids, wantCalls)
					}
					mismatch := slices.ContainsFunc(response.Warnings, func(w llm.Warning) bool {
						return w.Code == "responses_output_item_count_mismatch"
					})
					if mismatch != (mode == "mismatch") {
						t.Errorf("settled count mismatch warning = %v, want %v", mismatch, mode == "mismatch")
					}
				}
				if mode == "streamed" && !hasEventType(events, llm.StreamEventProviderEvent) {
					t.Error("stream omitted the raw unfinished completion")
				}
			})
		}
	}
}

// abortingSSEServer writes prefix and then hijacks and closes the connection
// without a terminating chunk, so the client's read fails mid-stream. That is
// evidence about the transport, never about which endpoints the model
// implements.
func abortingSSEServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = conn.Write([]byte(prefix))
		if tcp, ok := conn.(*net.TCPConn); ok {
			// A reset rather than a clean FIN, so the client reads an error
			// instead of an orderly EOF.
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isUnsupportedEndpoint reports whether err is the endpoint-capability
// sentinel, which is the one terminal error that routes the caller off this
// endpoint entirely.
func isUnsupportedEndpoint(err error) bool {
	_, ok := errors.AsType[*llm.UnsupportedEndpointError](err)
	return ok
}

// streamTerminalError drains a stream and returns its terminal error event.
func streamTerminalError(t *testing.T, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	return streamTerminalErrorWithClient(t, srv.Client(), srv, req)
}
func streamTerminalErrorWithClient(t *testing.T, client *http.Client, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	s, err := (&Protocol{Client: client}).Stream(t.Context(), req, liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var terminal error
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			terminal = ev.Err
		}
	}
	return terminal
}

// TestStreamTerminalErrorsSeparateTheFourEndings pins the classification the
// Responses transport needs and the shared runner's single IncompleteMsg
// cannot express. Only one of the four says anything about endpoint support:
// a clean close with nothing this decoder recognizes. A stall, a broken read
// and a truncated-but-real stream are all evidence about the transport or the
// provider, and misreading any of them as "this model has no /v1/responses"
// sends the request to an endpoint that cannot serve it (#484).
func TestStreamTerminalErrorsSeparateTheFourEndings(t *testing.T) {
	t.Parallel()
	t.Run("stall", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			req := userReq("hi")
			req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
			client, srv := stallingSSEClient(t, twoResponsesEvents)
			err := streamTerminalErrorWithClient(t, client, srv, req)
			if _, ok := errors.AsType[*llm.StreamError](err); !ok {
				t.Fatalf("stalled stream error = %v, want a retryable stream error", err)
			}
			if !errors.Is(err, llm.ErrResponseIdleTimeout) {
				t.Fatalf("a stall must carry the response-byte idle timeout as its cause: %v", err)
			}
			if isUnsupportedEndpoint(err) {
				t.Fatalf("silence is not evidence about endpoint support: %v", err)
			}
		})
	})

	t.Run("read failure", func(t *testing.T) {
		err := streamTerminalError(t, abortingSSEServer(t, twoResponsesEvents), userReq("hi"))
		if _, ok := errors.AsType[*llm.StreamError](err); !ok {
			t.Fatalf("broken stream error = %v, want a retryable stream error", err)
		}
		if errors.Unwrap(err) == nil {
			t.Fatalf("a broken read must carry the read failure as its cause: %v", err)
		}
		if errors.Is(err, llm.ErrResponseIdleTimeout) || errors.Is(err, llm.ErrSSEReadTimeout) {
			t.Fatalf("a broken read is not a stall: %v", err)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a broken read is not evidence about endpoint support: %v", err)
		}
	})

	t.Run("no responses events", func(t *testing.T) {
		// A clean 200 carrying only events this decoder does not recognize as
		// the Responses API: the model likely does not implement /v1/responses.
		srv, _ := server(t, 200, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		err := streamTerminalError(t, srv, userReq("hi"))
		if !isUnsupportedEndpoint(err) {
			t.Fatalf("a clean close with no Responses event is the endpoint sentinel: %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		// Real Responses events, then a clean close with no response.completed.
		srv, _ := server(t, 200, twoResponsesEvents)
		err := streamTerminalError(t, srv, userReq("hi"))
		if _, ok := errors.AsType[*llm.StreamError](err); !ok {
			t.Fatalf("truncated stream error = %v, want a retryable stream error", err)
		}
		if cause := errors.Unwrap(err); cause != nil {
			t.Fatalf("a clean close has no read failure to report, got cause %v", cause)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a stream that produced Responses events proves the endpoint served it: %v", err)
		}
	})
}

// A gateway can answer 200 OK with an error payload and no Responses event at
// all: lunaroute wraps an upstream rejection as a bare data frame, and
// OpenAI-compatible endpoints send the flat or nested envelope under an
// "error" event. Each of those is the provider reporting a failure, not
// evidence about which protocol the model speaks, so it must classify as a
// retryable stream error. Reading it as the endpoint sentinel makes a
// transient upstream failure permanent and the turn gets no retry.
func TestStreamTerminalErrorsDecodeInBandErrorFrames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "bare data frame",
			body: "data: {\"error\":{\"code\":null,\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}}\n\n",
		},
		{
			name: "error event with flat envelope",
			body: "event: error\ndata: {\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}\n\n",
		},
		{
			name: "error event with error envelope",
			body: "event: error\ndata: {\"error\":{\"message\":\"upstream returned status 400\",\"type\":\"server_error\"}}\n\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			srv, _ := server(t, 200, testCase.body)
			err := streamTerminalError(t, srv, userReq("hi"))
			if err == nil {
				t.Fatal("a stream carrying an error payload must report it")
			}
			if isUnsupportedEndpoint(err) {
				t.Fatalf("an error payload is not evidence about endpoint support: %v", err)
			}
			if got := llm.Classify(err); got != llm.ErrorClassRetryable {
				t.Fatalf("Classify(%v) = %v, want Retryable so the retry chain runs", err, got)
			}
			var le llm.Error
			if !errors.As(err, &le) {
				t.Fatalf("in-band error %T does not implement llm.Error", err)
			}
			if le.Provider() != "oai-prod" {
				t.Fatalf("provider = %q, want the resolved instance", le.Provider())
			}
		})
	}
}
