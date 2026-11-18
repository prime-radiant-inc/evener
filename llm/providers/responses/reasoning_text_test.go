package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
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

// replayInput checks the first and reloaded requests carry identical input.
func replayInput(t *testing.T, msg llm.Message, replay bool) []replayItem {
	t.Helper()
	res := resolved(openaiCaps)
	if replay {
		res.Caps.ReplayReasoningText = new(true)
	}
	input := func(m llm.Message) string {
		body := build(t, llm.Request{Model: res.ModelID, Messages: []llm.Message{m}}, res)
		return jsonOf(t, body["input"])
	}
	warm := input(msg)
	var saved llm.Message
	if err := json.Unmarshal([]byte(jsonOf(t, msg)), &saved); err != nil {
		t.Fatal(err)
	}
	if cold := input(saved); cold != warm {
		t.Fatalf("reloaded input = %s, first input = %s", cold, warm)
	}
	var items []replayItem
	if err := json.Unmarshal([]byte(warm), &items); err != nil {
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
	const earlierSummary = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":" r3 "},{"type":"reasoning_text","text":""},{"type":"reasoning_text","text":"r4"}],"summary":[{"type":"summary_text","text":"s0"}]}`
	const otherItem = `{"type":"reasoning","id":"rs_other","content":[{"type":"reasoning_text","text":"r_other"}]}`
	const summaryItem = `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s1"}]}`
	const displayItem = `{"type":"reasoning","id":"rs_1","content":[{"type":"summary_text","text":"s1"}]}`
	const earlierAnswer = `{"type":"message","content":[{"type":"output_text","text":"m_earlier"}]}`
	const answerItem = `{"type":"message","content":[{"type":"output_text","text":"m1"}]}`
	const emptyAnswer = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":""}]}`
	const unnamedEmptyAnswer = `{"type":"message","content":[{"type":"output_text","text":""}]}`
	const secondAnswer = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":""},{"type":"output_text","text":" \t"},{"type":"output_text","text":"m2"}]}`
	const answerDelta = `{"type":"response.output_text.delta","delta":"m1"}`
	const toolDelta = `{"type":"response.function_call_arguments.delta","name":"f","call_id":"call_1","delta":"{}"}`
	const toolItem = `{"type":"function_call","name":"f","call_id":"call_1","arguments":"{}"}`
	const otherAnswerDelta = `{"type":"response.output_text.delta","item_id":"msg_2","delta":"m2"}`
	const laterPart = `{"type":"response.output_text.delta","item_id":"msg_2","content_index":2,"delta":"m2"}`
	const blankPart = `{"type":"response.output_text.delta","item_id":"msg_2","content_index":1,"delta":" \t"}`
	const toolAdded = `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f"}}`
	const argumentsDone = `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{}"}`
	const finalArguments = `{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":" \t{ \"k\": 1 }\n"}`
	const emptyToolByCall = `{"type":"function_call","call_id":"call_1","name":"f","arguments":""}`
	const emptyToolByItem = `{"type":"function_call","id":"fc_1","arguments":""}`
	const emptyToolByIndex = `{"type":"function_call","arguments":""}`
	const itemArguments = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{}"}`
	const namedFinalArguments = `{"type":"response.function_call_arguments.done","item_id":"fc_1","call_id":"call_1","name":"f","arguments":" \t{ \"k\": 1 }\n"}`
	done := func(item string) string { return `{"type":"response.output_item.done","item":` + item + "}" }
	at := func(index int, event string) string {
		return `{"output_index":` + strconv.Itoa(index) + "," + event[1:]
	}
	const messageAdded = `{"type":"response.output_item.added","item":{"type":"message","id":"msg_2","phase":"commentary"}}`
	const indexOnlyAdded = `{"type":"response.output_item.added","item":{"type":"message","phase":"commentary"}}`
	const otherMessageAdded = `{"type":"response.output_item.added","item":{"type":"message","id":"msg_3","phase":"final_answer"}}`
	const emptyFinal = `{"type":"message","id":"msg_3","phase":"final_answer","content":[{"type":"output_text","text":""}]}`
	const reasoningAdded = `{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`
	const emptyCommentary = `{"type":"message","id":"msg_2","phase":"commentary","content":[{"type":"output_text","text":""}]}`
	const blankMessageParts = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":""},{"type":"output_text","text":""},{"type":"output_text","text":""},{"type":"output_text","text":""}]}`
	const partialMessage = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":""},{"type":"output_text","text":""},{"type":"output_text","text":"m_done"},{"type":"output_text","text":""}]}`
	const partialTerminal = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":"m_terminal"},{"type":"output_text","text":""},{"type":"output_text","text":""},{"type":"output_text","text":""}]}`
	const completedTextPart = `{"type":"response.output_text.done","item_id":"msg_2","content_index":1,"text":" m_part "}`
	const completedContentPart = `{"type":"response.content_part.done","item_id":"msg_2","content_index":1,"part":{"type":"output_text","text":" m_part "}}`
	const lateTextPart = `{"type":"response.output_text.done","item_id":"msg_2","content_index":2,"text":"m_late"}`
	const indexedFirst = `{"type":"response.output_text.delta","item_id":"msg_2","content_index":0,"delta":"a"}`
	const noPartIndex = `{"type":"response.output_text.delta","item_id":"msg_2","delta":"b"}`
	const unindexedTextDone = `{"type":"response.output_text.done","item_id":"msg_2","text":" final "}`
	const unindexedPartDone = `{"type":"response.content_part.done","item_id":"msg_2","part":{"type":"output_text","text":" final "}}`
	const toolItemAdded = `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","name":"f"}}`
	const toolStart = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{ \"k\": "}`
	const toolMiddle = `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"1"}`
	const toolEnd = `{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":" }"}`
	const idArguments = `{"type":"response.function_call_arguments.delta","id":"fc_1","name":"f","delta":"{}"}`
	const linkedCall = `{"type":"function_call","id":"fc_1","call_id":"call_1","arguments":""}`
	const linkedFinalCall = `{"type":"function_call","id":"fc_1","call_id":"call_1","arguments":" \t{ \"k\": 1 }\n"}`
	const callFinalArguments = `{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":" \t{ \"k\": 1 }\n"}`
	const firstCall = `{"type":"function_call","call_id":"call_1"}`
	const secondCall = `{"type":"function_call","call_id":"call_2"}`
	indexedCalls := []string{
		at(0, `{"type":"response.function_call_arguments.done","call_id":"call_1","name":"f","arguments":" \t{ \"k\": 1 }\n"}`),
		at(0, `{"type":"response.function_call_arguments.done","call_id":"call_2","name":"g","arguments":"{ \"k\": 2 }"}`),
	}
	const secondItemAdded = `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","name":"g"}}`
	const secondItemFinal = `{"type":"response.function_call_arguments.done","item_id":"fc_2","name":"g","arguments":"{ \"k\": 2 }"}`
	const secondLinked = `{"type":"function_call","id":"fc_2","call_id":"call_2"}`
	anonymousFinal := at(0, `{"type":"response.function_call_arguments.done","arguments":"{\"k\":3}"}`)
	provisionalAtOccupiedIndex := []string{at(0, toolAdded), finalArguments, at(0, secondItemAdded), anonymousFinal}
	const indexedArguments = `{"type":"response.function_call_arguments.delta","delta":"{}"}`
	const indexedFinalArguments = `{"type":"response.function_call_arguments.done","arguments":" \t{ \"k\": 1 }\n"}`
	const indexedItemFinal = `{"type":"function_call","id":"fc_1","arguments":" \t{ \"k\": 1 }\n"}`
	const sameIDAdded = `{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"f"}}`
	const conflictingCallDelta = `{"type":"response.function_call_arguments.delta","call_id":"call_other","item_id":"fc_1","delta":"{}"}`
	const plainWithSummary = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":" r_terminal "}],"summary":[{"type":"summary_text","text":" s_terminal "}]}`
	const longReasoning = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"old"},{"type":"reasoning_text","text":" r_observed "},{"type":"summary_text","text":"display_3"},{"type":"reasoning_text","text":"withdrawn"}]}`
	const partialReasoning = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"new"},{"type":"reasoning_text","text":""},{"type":"reasoning_text","text":""}],"summary":[]}`
	const missingReasoningFields = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"new"},{"type":"reasoning_text"},{"type":"reasoning_text"},{"type":"reasoning_text","text":"\t"},{"type":"reasoning_text","text":""},{"type":"reasoning_text"}],"summary":[]}`
	const missingReasoningField = `{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text"}]}`
	const differentReasoningType = `{"type":"reasoning","id":"rs_1","content":[{"type":"summary_text","text":"display_0"},{"type":"reasoning_text","text":"r_1"}]}`
	const shortMessage = `{"type":"message","id":"msg_2","content":[{"type":"output_text","text":"m1"}]}`
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
	multipart := replayItem{Type: "message", Content: []replayText{{"output_text", ""}, {"output_text", " \t"}, {"output_text", "m2"}}}
	commentary := otherAnswer
	commentary.Phase = "commentary"
	raw := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", " r3 "}, {"reasoning_text", ""}, {"reasoning_text", "r4"}}, Summary: []replayText{}}
	empty := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", ""}}, Summary: []replayText{}}
	emptyWithSummary := empty
	emptyWithSummary.Summary = []replayText{{"summary_text", "s1"}}
	blank := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", " "}, {"reasoning_text", "\t"}}, Summary: []replayText{}}
	rawWithSummary := raw
	rawWithSummary.Summary = []replayText{{"summary_text", "s1"}}
	encrypted := replayItem{Type: "reasoning", ID: "rs_1", Encrypted: "e1", Summary: []replayText{}}
	tool := replayItem{Type: "function_call", CallID: "call_1", Name: "f", Arguments: "{}"}
	finalTool := tool
	finalTool.Arguments = " \t{ \"k\": 1 }\n"
	secondTool := replayItem{Type: "function_call", CallID: "call_2", Name: "g", Arguments: `{ "k": 2 }`}
	linkedTool := tool
	linkedTool.Arguments = `{ "k": 1 }`
	linkedAnswer := replayItem{Type: "message", Phase: "commentary", Content: []replayText{{"output_text", "m1bm1"}}}
	announcedAnswer := answer
	announcedAnswer.Phase = "commentary"
	blankCommentary := replayItem{Type: "message", Phase: "commentary", Content: []replayText{{"output_text", ""}}}
	unnamedBlank := replayItem{Type: "message", Content: []replayText{{"output_text", ""}}}
	blankFinal := blankCommentary
	blankFinal.Phase = "final_answer"
	indexedParts := replayItem{Type: "message", Phase: "commentary", Content: []replayText{{"output_text", " \t"}, {"output_text", "m2"}}}
	cryptoWithSummary := encrypted
	cryptoWithSummary.Summary = []replayText{{"summary_text", " s_terminal "}}
	partial := replayItem{Type: "message", Content: []replayText{{"output_text", "m_terminal"}, {"output_text", " m_part "}, {"output_text", "m_done"}, {"output_text", ""}}}
	filledReasoning := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", "new"}, {"reasoning_text", " r_observed "}, {"reasoning_text", ""}}, Summary: []replayText{}}
	recoveredFields := replayItem{Type: "reasoning", ID: "rs_1", Content: []replayText{{"reasoning_text", "new"}, {"reasoning_text", " r_observed "}, {"reasoning_text", "\t"}, {"reasoning_text", ""}}, Summary: []replayText{}}
	otherRaw := replayItem{Type: "reasoning", ID: "rs_other", Content: []replayText{{"reasoning_text", "r_other"}}, Summary: []replayText{}}
	for _, tc := range []struct {
		name   string
		events []string
		output []string
		want   []replayItem
	}{
		{"settled item", nil, []string{reasoningItem, answerItem}, []replayItem{raw, answer}},
		{"empty terminal arguments match call ID", []string{toolAdded, toolDelta, finalArguments}, []string{answerItem, emptyToolByCall}, []replayItem{answer, finalTool}},
		{"empty terminal arguments match item ID", []string{toolAdded, finalArguments}, []string{emptyToolByItem}, []replayItem{finalTool}},
		{"empty terminal arguments match output index", []string{at(1, toolAdded), finalArguments}, []string{answerItem, emptyToolByIndex}, []replayItem{answer, finalTool}},
		{"terminal arguments own supplied bytes", []string{toolAdded, finalArguments}, []string{toolItem}, []replayItem{tool}},
		{"terminal text placeholders fill by content index", []string{laterPart, blankPart}, []string{blankMessageParts}, []replayItem{{Type: "message", Content: []replayText{{"output_text", ""}, {"output_text", " \t"}, {"output_text", "m2"}, {"output_text", ""}}}}},
		{"terminal partial message keeps individual settled parts", []string{blankPart, laterPart, completedTextPart, done(partialMessage)}, []string{partialTerminal}, []replayItem{partial}},
		{"completed content part restores its own text", []string{blankPart, laterPart, completedContentPart, done(partialMessage)}, []string{partialTerminal}, []replayItem{partial}},
		{"completed item owns supplied text over a late part event", []string{done(partialMessage), lateTextPart, completedTextPart}, []string{partialTerminal}, []replayItem{partial}},
		{"a single text part accepts intermittent indexes", []string{noPartIndex, indexedFirst, noPartIndex}, nil, []replayItem{{Type: "message", Content: []replayText{{"output_text", "bab"}}}}},
		{"unindexed text completion owns the only indexed part", []string{indexedFirst, unindexedTextDone}, nil, []replayItem{{Type: "message", Content: []replayText{{"output_text", " final "}}}}},
		{"unindexed content completion owns the only indexed part", []string{laterPart, unindexedPartDone}, nil, []replayItem{{Type: "message", Content: []replayText{{"output_text", " final "}}}}},
		{"unindexed completion has no owner among multiple parts", []string{indexedFirst, laterPart, unindexedTextDone}, nil, []replayItem{{Type: "message", Content: []replayText{{"output_text", "a"}, {"output_text", "m2"}}}}},
		{"ambiguous completion cannot fill a selected terminal slot", []string{blankPart, laterPart, unindexedTextDone}, []string{emptyAnswer}, []replayItem{unnamedBlank}},
		{"a later link keeps the named completed message selection", []string{indexedFirst, laterPart, done(emptyAnswer), at(0, done(`{"type":"message","content":null}`))}, []string{`{"type":"message","id":"msg_2"}`}, []replayItem{{Type: "message", Content: []replayText{{"output_text", "a"}}}}},
		{"a later link keeps the indexed completed message selection", []string{indexedFirst, laterPart, at(0, done(unnamedEmptyAnswer)), done(`{"type":"message","id":"msg_2"}`)}, []string{`{"type":"message","id":"msg_2"}`}, []replayItem{{Type: "message", Content: []replayText{{"output_text", "a"}}}}},
		{"item completion links the call IDs and supplies final arguments once", []string{toolItemAdded, toolDelta, done(linkedFinalCall)}, nil, []replayItem{finalTool}},
		{"a delta addressed by the registered item ID stays on the call", []string{toolAdded, idArguments, done(linkedCall)}, nil, []replayItem{tool}},
		{"an initially unqualified ID resolves to the completed item", []string{idArguments, done(linkedFinalCall)}, nil, []replayItem{finalTool}},
		{"arguments completion identifies a previously completed provisional call", []string{toolItemAdded, done(emptyToolByItem), namedFinalArguments}, nil, []replayItem{finalTool}},
		{"item completion links interleaved argument fragments", []string{toolItemAdded, toolStart, toolMiddle, toolEnd, done(linkedCall)}, nil, []replayItem{linkedTool}},
		{"terminal call links interleaved argument fragments", []string{toolItemAdded, toolStart, toolMiddle, toolEnd}, []string{linkedCall}, []replayItem{linkedTool}},
		{"indexed argument delta names a provisional call", []string{at(2, toolItemAdded), at(2, toolDelta)}, nil, []replayItem{tool}},
		{"indexed final arguments name a provisional call", []string{at(2, toolItemAdded), itemArguments, at(2, callFinalArguments)}, nil, []replayItem{finalTool}},
		{"indexed item completion names a provisional call", []string{at(2, toolItemAdded), itemArguments, at(2, done(emptyToolByCall))}, nil, []replayItem{tool}},
		{"indexed call retains its call ID when the item ID arrives", []string{at(2, toolDelta), at(2, toolItemAdded), finalArguments}, nil, []replayItem{finalTool}},
		{"indexed provisional call accepts arguments without repeated IDs", []string{at(2, toolItemAdded), at(2, indexedArguments), at(2, done(emptyToolByCall))}, nil, []replayItem{tool}},
		{"indexed named call accepts final arguments without repeated IDs", []string{at(2, toolAdded), itemArguments, at(2, indexedFinalArguments)}, nil, []replayItem{finalTool}},
		{"issued call claims its first index before an index-only final", []string{
			`{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{"}`, at(2, toolAdded), at(2, indexedFinalArguments),
		}, nil, []replayItem{finalTool}},
		{"indexed final arguments precede their terminal call ID", []string{at(0, indexedFinalArguments)}, []string{emptyToolByCall}, []replayItem{finalTool}},
		{"issued call repeats finalized arguments at another index", []string{indexedCalls[0], at(1, callFinalArguments)}, nil, []replayItem{finalTool}},
		{"issued call gains its item ID at another index", []string{indexedCalls[0], at(1, done(linkedCall)), finalArguments}, nil, []replayItem{finalTool}},
		{"issued call links a provisional named item at another index", []string{indexedCalls[0], at(1, toolItemAdded), at(1, namedFinalArguments)}, nil, []replayItem{finalTool}},
		{"issued call repeated at another call's index keeps the other call", []string{indexedCalls[0], at(1, done(secondLinked)), secondItemFinal, at(1, namedFinalArguments)}, nil, []replayItem{finalTool, secondTool}},
		{"issued call with two item IDs stays a single call", []string{at(0, toolAdded), finalArguments, at(1, done(strings.Replace(linkedFinalCall, "fc_1", "fc_2", 1)))}, nil, []replayItem{finalTool}},
		{"terminal renames an issued item and omits finalized arguments", []string{toolAdded, finalArguments}, []string{`{"type":"function_call","id":"fc_renamed","call_id":"call_1"}`}, []replayItem{finalTool}},
		{"terminal supplies its own arguments after renaming an issued item", []string{toolAdded, finalArguments}, []string{`{"type":"function_call","id":"fc_renamed","call_id":"call_1","arguments":"{}"}`}, []replayItem{tool}},
		{"later explicit terminal arguments supersede stream recovery for the same call", []string{toolAdded, finalArguments}, []string{
			emptyToolByCall, `{"type":"function_call","id":"fc_renamed","call_id":"call_1","arguments":"{}"}`,
		}, []replayItem{tool}},
		{"terminal binds the first item ID before an item-only completion", []string{indexedCalls[0]}, []string{
			`{"type":"function_call","id":"fc_late","call_id":"call_1"}`, `{"type":"function_call","id":"fc_late","arguments":"{}"}`,
		}, []replayItem{tool}},
		{"terminal names another issued item but selects calls by call ID", []string{at(0, toolAdded), finalArguments, at(0, done(secondLinked)), secondItemFinal},
			[]string{`{"type":"function_call","id":"fc_2","call_id":"call_1"}`, secondCall}, []replayItem{finalTool, secondTool}},
		{"repeated terminal call completes its fields under a different item ID", nil, []string{
			`{"type":"function_call","id":"fc_1","call_id":"call_1"}`, `{"type":"function_call","id":"fc_2","call_id":"call_1","name":"f","arguments":"{}"}`,
		}, []replayItem{tool}},
		{"issued call selected twice by the terminal", []string{toolAdded, finalArguments}, []string{emptyToolByItem, firstCall}, []replayItem{finalTool}},
		{"issued call completed after a terminal reference", nil, []string{firstCall, toolItem}, []replayItem{tool}},
		{"issued call in a terminal link to a provisional item at another index", []string{indexedCalls[0], at(1, toolItemAdded)}, []string{linkedCall}, []replayItem{finalTool}},
		{"first call at a reused index keeps its final arguments", indexedCalls, []string{firstCall}, []replayItem{finalTool}},
		{"second call at a reused index keeps its name and final arguments", indexedCalls, []string{secondCall}, []replayItem{secondTool}},
		{"calls at a reused index follow their selected IDs in terminal order", indexedCalls, []string{secondCall, firstCall}, []replayItem{secondTool, finalTool}},
		{"unidentified final at a reused index belongs to neither call", append(slices.Clone(indexedCalls), anonymousFinal),
			[]string{firstCall, secondCall}, []replayItem{finalTool, secondTool}},
		{"terminal cannot identify a call by its reused index alone", indexedCalls, []string{emptyToolByIndex}, nil},
		{"a different provisional item makes the occupied index ambiguous", provisionalAtOccupiedIndex, []string{firstCall}, []replayItem{finalTool}},
		{"a provisional item prevents anonymous terminal selection at the occupied index", provisionalAtOccupiedIndex, []string{emptyToolByIndex}, nil},
		{"distinct provisional items keep their bytes until both call IDs arrive", []string{at(0, toolItemAdded), finalArguments, at(0, secondItemAdded), secondItemFinal, anonymousFinal},
			[]string{secondLinked, linkedCall}, []replayItem{secondTool, finalTool}},
		{"a registered call identifies its item at the occupied index", []string{at(0, toolItemAdded), finalArguments, done(secondLinked), secondItemFinal,
			at(0, `{"type":"response.function_call_arguments.done","call_id":"call_2","name":"g","arguments":"{ \"k\": 2 }"}`), anonymousFinal},
			[]string{linkedCall, secondCall}, []replayItem{finalTool, secondTool}},
		{"a registered item identifies its call at the occupied index", []string{indexedCalls[0], done(secondLinked), at(0, secondItemFinal), anonymousFinal},
			[]string{firstCall, secondCall}, []replayItem{finalTool, secondTool}},
		{"a registered event ID identifies its item at the occupied index", []string{at(0, toolAdded), finalArguments, done(secondLinked), secondItemFinal,
			at(0, `{"type":"response.function_call_arguments.done","id":"fc_2","arguments":"{ \"k\": 2 }"}`), anonymousFinal},
			[]string{firstCall, secondCall}, []replayItem{finalTool, secondTool}},
		{"indexed completed item precedes its terminal call ID", []string{at(0, done(indexedItemFinal))}, []string{emptyToolByCall}, []replayItem{finalTool}},
		{"an explicit call ID matching its item ID is already settled", []string{sameIDAdded, conflictingCallDelta}, nil, []replayItem{{Type: "function_call", CallID: "fc_1", Name: "f", Arguments: "{}"}}},
		{"indexed arguments without an eventual call ID stay undispatched", []string{at(2, indexedArguments)}, nil, nil},
		{"named item without a call ID preserves its native sibling only", []string{toolItemAdded, finalArguments, done(reasoningItem)}, nil, []replayItem{raw}},
		{"terminal item without a call ID preserves its native sibling only", []string{toolItemAdded, itemArguments}, []string{emptyToolByItem, answerItem}, []replayItem{answer}},
		{"terminal call ID confirms an earlier named item", []string{toolItemAdded, finalArguments}, []string{linkedCall, answerItem}, []replayItem{finalTool, answer}},
		{"item completion links interleaved message fragments", []string{at(0, answerDelta), noPartIndex, at(0, answerDelta), at(1, done(emptyFinal)), at(0, done(emptyCommentary))}, nil, []replayItem{linkedAnswer, blankFinal}},
		{"terminal message links interleaved fragments", []string{at(0, answerDelta), noPartIndex, at(0, answerDelta)}, []string{emptyCommentary}, []replayItem{linkedAnswer}},
		{"terminal plaintext keeps matching ciphertext", []string{done(encryptedItem)}, []string{answerItem, plainWithSummary}, []replayItem{answer, cryptoWithSummary}},
		{"substantive message selects content membership", []string{laterPart, blankPart}, []string{shortMessage}, []replayItem{answer}},
		{"terminal reasoning fills only matching raw slots", []string{done(longReasoning)}, []string{partialReasoning}, []replayItem{filledReasoning}},
		{"missing terminal reasoning fields take only their matching typed bytes", []string{done(longReasoning)}, []string{missingReasoningFields}, []replayItem{recoveredFields}},
		{"an indexed terminal recovers missing reasoning fields without repeating the item ID", []string{at(0, done(longReasoning))}, []string{strings.Replace(missingReasoningFields, `"id":"rs_1",`, "", 1)}, []replayItem{recoveredFields}},
		{"single missing reasoning field takes only its own index", []string{done(longReasoning), answerDelta}, []string{missingReasoningField}, []replayItem{{Type: "reasoning", ID: "rs_1", Summary: []replayText{}, Content: []replayText{{"reasoning_text", "old"}}}, answer}},
		{"missing reasoning field has no bytes of its type at its index", []string{done(differentReasoningType), answerDelta}, []string{missingReasoningField}, []replayItem{answer}},
		{"empty terminal keeps display beside another native reasoning item", []string{done(otherItem), done(answerItem)}, nil, []replayItem{otherRaw, answer}},
		{"empty terminal keeps display beside ciphertext", []string{done(encryptedItem), done(answerItem)}, nil, []replayItem{encrypted, answer}},
		{"anonymous terminal call leaves the explicitly matched call available", []string{at(0, toolAdded), finalArguments}, []string{emptyToolByIndex, emptyToolByCall}, []replayItem{finalTool}},
		// An explicit completed array owns the message's parts. Text a clear
		// removed must not be recovered from the part cache when the terminal
		// selects that slot without supplying a value.
		{"explicit empty completion retires the draft its slot held", []string{
			done(`{"type":"message","id":"msg_2","content":[{"type":"output_text","text":"draft"}]}`),
			done(`{"type":"message","id":"msg_2","content":[]}`),
		}, []string{`{"type":"message","id":"msg_2","content":[{"type":"output_text"}]}`}, nil},
		// One issued call ID is one dispatchable operation. A repeated
		// positional frame must not mint a second record for it, or an empty
		// terminal hands the agent the same mutating call twice.
		{"one issued call ID stays one record across repeated indexes", []string{
			at(1, `{"type":"response.output_item.added","item":{"type":"function_call","name":"write_file"}}`),
			at(1, `{"type":"response.function_call_arguments.done","call_id":"call_dup","arguments":"{\"a\":1}"}`),
			at(2, `{"type":"response.function_call_arguments.done","call_id":"call_dup","arguments":"{\"a\":1}"}`),
		}, nil, []replayItem{{Type: "function_call", CallID: "call_dup", Name: "write_file", Arguments: `{"a":1}`}}},

		{"reasoning deltas alone stay on display", nil, nil, nil},
		{"indexed answer and final arguments without completed items", []string{at(2, toolAdded), toolDelta, finalArguments, at(0, messageAdded), laterPart, blankPart, at(1, answerDelta)}, nil, []replayItem{indexedParts, answer, finalTool}},
		{"final arguments identify the call", []string{itemArguments, namedFinalArguments}, nil, []replayItem{finalTool}},
		{"announced message owns anonymous text", []string{messageAdded, answerDelta}, []string{emptyAnswer}, []replayItem{announcedAnswer}},
		{"late announcement identifies pending anonymous text", []string{answerDelta, messageAdded}, []string{emptyAnswer}, []replayItem{announcedAnswer}},
		{"announced index owns anonymous text", []string{at(0, indexOnlyAdded), answerDelta}, []string{emptyAnswer}, []replayItem{announcedAnswer}},
		{"a distinct indexed completion keeps both messages", []string{at(0, answerDelta), at(1, done(secondAnswer))}, nil, []replayItem{answer, multipart}},
		{"unnamed terminal matches its only announced message", []string{messageAdded, answerDelta}, []string{unnamedEmptyAnswer}, []replayItem{announcedAnswer}},
		{"unnamed terminal leaves the explicitly matched message available", []string{at(0, messageAdded), answerDelta}, []string{unnamedEmptyAnswer, emptyAnswer}, []replayItem{unnamedBlank, announcedAnswer}},
		{"sparse message keeps an indexed completed reasoning sibling", []string{at(0, done(reasoningItem)), at(1, indexOnlyAdded), answerDelta}, []string{unnamedEmptyAnswer}, []replayItem{raw, announcedAnswer}},
		{"empty message preserves earlier tool sibling", []string{at(0, toolAdded), at(1, messageAdded), answerDelta, finalArguments}, []string{emptyAnswer}, []replayItem{finalTool, announcedAnswer}},
		{"ambiguous anonymous text stays unassigned", []string{at(0, messageAdded), at(1, otherMessageAdded), answerDelta}, []string{emptyCommentary, emptyFinal}, []replayItem{blankCommentary, blankFinal}},
		{"substantive terminal replaces anonymous text and sparse siblings", []string{messageAdded, answerDelta, toolDelta}, []string{secondAnswer}, []replayItem{multipart}},
		{"standalone empty native answer", nil, []string{emptyAnswer}, []replayItem{{Type: "message", Content: []replayText{{"output_text", ""}}}}},
		{"empty native answer with a matching streamed answer", []string{otherAnswerDelta}, []string{emptyAnswer}, []replayItem{otherAnswer}},
		{"distinct native messages sharing a phase", nil, []string{answerItem, secondAnswer}, []replayItem{answer, multipart}},
		{"substantive native message owns its phase", []string{messageAdded}, []string{secondAnswer}, []replayItem{multipart}},
		{"out of order completed messages sharing a phase", []string{at(1, done(secondAnswer)), at(0, done(answerItem))}, nil, []replayItem{answer, multipart}},
		{"indexed delta and completed answer sharing a phase", []string{at(1, done(secondAnswer)), at(0, answerDelta)}, nil, []replayItem{answer, multipart}},
		{"indexed parts of a sparse message", []string{at(1, done(answerItem)), at(0, laterPart), at(0, blankPart)}, nil, []replayItem{{Type: "message", Content: []replayText{{"output_text", " \t"}, {"output_text", "m2"}}}, answer}},
		{"completed items with empty terminal", []string{done(reasoningItem), done(answerItem)}, nil, []replayItem{raw, answer}},
		{"display deltas with settled answer", nil, []string{answerItem}, []replayItem{answer}},
		{"display deltas with empty terminal", []string{answerDelta}, nil, []replayItem{answer}},
		{"standalone empty native reasoning", nil, []string{emptyReasoning}, []replayItem{empty}},
		{"empty completed reasoning with terminal summary", []string{done(emptyReasoning)}, []string{summaryItem, answerItem}, []replayItem{emptyWithSummary, answer}},
		{"streamed answer with empty terminal reasoning", []string{answerDelta}, []string{emptyReasoning}, []replayItem{empty, answer}},
		{"streamed answer with blank terminal reasoning", []string{answerDelta}, []string{blankReasoning}, []replayItem{blank, answer}},
		{"blank terminal reasoning with completed answer and call", []string{done(answerItem), done(toolItem)}, []string{blankReasoning}, []replayItem{blank, answer, tool}},
		{"completed blank reasoning fills empty terminal", []string{done(blankReasoning)}, []string{emptyReasoning, answerItem}, []replayItem{blank, answer}},
		{"terminal native whitespace wins over earlier text", []string{done(reasoningItem)}, []string{blankReasoning, answerItem}, []replayItem{blank, answer}},
		{"terminal empty summary clears completed summary", []string{done(earlierSummary)}, []string{emptyReasoning, answerItem}, []replayItem{raw, answer}},
		{"reasoning only", []string{done(reasoningItem)}, []string{emptyReasoning}, []replayItem{raw}},
		{"display-only terminal part withdraws completed reasoning and preserves the answer", []string{done(reasoningItem), answerDelta}, []string{displayItem}, []replayItem{answer}},
		{"reasoning with streamed answer and tool", []string{done(reasoningItem), answerDelta, toolDelta}, []string{emptyReasoning}, []replayItem{raw, answer, tool}},
		{"reasoning with completed siblings", []string{answerDelta, done(answerItem), done(reasoningItem), toolDelta, done(toolItem)}, []string{emptyReasoning}, []replayItem{answer, raw, tool}},
		{"distinct streamed message and completed arguments", []string{done(answerItem), done(reasoningItem), otherAnswerDelta, toolAdded, argumentsDone}, []string{emptyReasoning}, []replayItem{answer, raw, otherAnswer, tool}},
		{"completed reasoning with streamed answer and empty terminal", []string{done(reasoningItem), answerDelta}, nil, []replayItem{raw, answer}},
		{"encrypted reasoning with empty text", nil, []string{encryptedItem, answerItem}, []replayItem{encrypted, answer}},
		{"matching completed reasoning and terminal answer", []string{done(reasoningItem), done(earlierAnswer)}, []string{emptyReasoning, answerItem}, []replayItem{raw, answer}},
		{"matching item after answer keeps terminal summary", []string{done(reasoningItem)}, []string{answerItem, summaryItem}, []replayItem{answer, rawWithSummary}},
		{"different completed reasoning", []string{done(otherItem)}, []string{emptyReasoning, answerItem}, []replayItem{empty, answer}},
		{"completed summary only", []string{done(summaryItem)}, []string{emptyReasoning, answerItem}, []replayItem{empty, answer}},
		{"terminal raw reasoning wins", []string{done(earlierItem)}, []string{reasoningItem, answerItem}, []replayItem{raw, answer}},
		{"terminal encrypted reasoning wins", []string{done(reasoningItem)}, []string{encryptedItem, answerItem}, []replayItem{encrypted, answer}},
		{"completed encrypted reasoning survives a placeholder", []string{done(encryptedItem)}, []string{blankReasoning, answerItem}, []replayItem{encrypted, answer}},
		{"placeholder with completed answer only", []string{done(answerItem)}, []string{emptyReasoning}, []replayItem{empty, answer}},
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
				t.Errorf("next input = %#v, want %#v", got, tc.want)
			}
			recorded, err := ExtractRecordedResponse([]byte(sse.String()), "glm-5.3")
			if err != nil {
				t.Fatal(err)
			}
			if got, want := replayInput(t, recorded.Message, true), replayInput(t, settled.Message, true); !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded input = %#v, live settled input = %#v", got, want)
			} else if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("recorded input = %#v, want %#v", got, tc.want)
			}
			for _, resp := range []*llm.Response{acc.Response(), &recorded} {
				if slices.Contains(tc.output, displayItem) && !slices.ContainsFunc(resp.Message.Content, func(p llm.ContentPart) bool {
					return p.Thinking != nil && p.Thinking.Text == "s1" && len(p.Thinking.ResponsesTextParts) == 1 && p.Thinking.ResponsesTextParts[0].Type == "summary_text"
				}) {
					t.Error("selected display text and its native type did not survive settlement")
				}
				calls := resp.ToolCalls()
				var count int
				for _, want := range tc.want {
					if want.Type != "function_call" {
						continue
					}
					if count < len(calls) && want.CallID != "" {
						call := calls[count]
						if call.ID != want.CallID || call.Name != want.Name || string(call.Arguments) != want.Arguments {
							t.Errorf("dispatch call = %+v, want %+v", call, want)
						}
					}
					count++
				}
				if len(calls) != count {
					t.Errorf("dispatchable calls = %d, want %d", len(calls), count)
				}
				if len(tc.output) == 0 || slices.Equal(tc.output, []string{emptyReasoning}) || slices.Equal(tc.output, []string{missingReasoningField}) {
					want := &llm.ThinkingData{Text: live.String()}
					hasDisplay := slices.ContainsFunc(resp.Message.Content, func(p llm.ContentPart) bool { return reflect.DeepEqual(p.Thinking, want) })
					hasNativeText := slices.ContainsFunc(tc.want, func(it replayItem) bool {
						return it.Type == "reasoning" && it.ID == "rs_1" && slices.ContainsFunc(it.Content, func(part replayText) bool { return part.Text != "" })
					})
					if hasDisplay == hasNativeText {
						t.Errorf("display reasoning present = %v, matching native text = %v", hasDisplay, hasNativeText)
					}
				}
				if slices.Contains(tc.output, plainWithSummary) {
					th := resp.Message.Content[1].Thinking
					if th == nil || th.Text != " r_terminal " || th.EncryptedContent != "e1" {
						t.Errorf("terminal thinking = %#v", th)
					}
					strict := replayInput(t, resp.Message, false)
					if !slices.ContainsFunc(strict, func(it replayItem) bool { return reflect.DeepEqual(it, cryptoWithSummary) }) {
						t.Errorf("strict input lost ciphertext: %#v", strict)
					}
				}
			}
		})
	}
}

// Two genuinely distinct issued call IDs that reuse one output index are two
// operations, and settlement must hand the agent both. The call ID owns
// identity, so sharing a position cannot collapse them into one record.
func TestResponsesSparseTerminalKeepsDistinctCallsAtOneIndex(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_one","call_id":"call_one","name":"write_file"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"call_id":"call_one","arguments":"{\"a\":1}"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_two","call_id":"call_two","name":"write_file"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"call_id":"call_two","arguments":"{\"b\":2}"}`,
	}
	const completed = `{"type":"response.completed","response":{"status":"completed","output":[]}}`
	all := append(slices.Clone(frames), completed)
	for _, source := range []string{"live", "recorded"} {
		var resp *llm.Response
		if source == "live" {
			_, resp = responsesEvents(t, all...)
		} else {
			recorded, err := ExtractRecordedResponse([]byte("data: "+strings.Join(all, "\n\ndata: ")+"\n\n"), "m")
			if err != nil {
				t.Fatal(err)
			}
			resp = &recorded
		}
		calls := resp.ToolCalls()
		got := make(map[string]string, len(calls))
		for _, call := range calls {
			got[call.ID] = string(call.Arguments)
		}
		if len(calls) != 2 {
			t.Fatalf("%s dispatchable calls = %d, want one per issued call ID: %+v", source, len(calls), calls)
		}
		for id, want := range map[string]string{"call_one": `{"a":1}`, "call_two": `{"b":2}`} {
			if got[id] != want {
				t.Errorf("%s call %q arguments = %q, want %q", source, id, got[id], want)
			}
		}
	}
}

// The mirror of the case above: one issued call ID that the gateway repeats
// with a different item ID is still one operation. The call ID owns the
// record, so the disagreeing item ID cannot mint the second copy an empty
// terminal would hand the agent as the same mutating call. The frame that
// repeats it still settles the call it names.
func TestResponsesIssuedCallKeepsOneRecordAcrossItemIDs(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_one","call_id":"call_dup","name":"write_file"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"call_id":"call_dup","delta":"{\"a\":"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_two","call_id":"call_dup","name":"write_file"}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_two","call_id":"call_dup","arguments":"{\"a\":1}"}`,
	}
	const completed = `{"type":"response.completed","response":{"status":"completed","output":[]}}`
	all := append(slices.Clone(frames), completed)
	for _, source := range []string{"live", "recorded"} {
		var resp *llm.Response
		if source == "live" {
			_, resp = responsesEvents(t, all...)
		} else {
			recorded, err := ExtractRecordedResponse([]byte("data: "+strings.Join(all, "\n\ndata: ")+"\n\n"), "m")
			if err != nil {
				t.Fatal(err)
			}
			resp = &recorded
		}
		calls := resp.ToolCalls()
		if len(calls) != 1 {
			t.Fatalf("%s dispatchable calls = %d, want one per issued call ID: %+v", source, len(calls), calls)
		}
		if calls[0].ID != "call_dup" || string(calls[0].Arguments) != `{"a":1}` {
			t.Fatalf("%s dispatch = %q %q, want the one issued call with its finalized arguments", source, calls[0].ID, string(calls[0].Arguments))
		}
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
		{"id":" \trs_1 ","type":"reasoning","content":[{"type":"reasoning_text","text":" α\n\nβ "},{"type":"summary_text","text":"v2"},{"type":"reasoning_text","text":"\t"},{"type":"reasoning_text","text":"γ\n"}],"summary":[{"type":"summary_text","text":" \ns1\t "},{"type":"summary_text","text":""},{"type":"summary_text","text":"\t \n"}]},
		{"type":"message","content":[{"type":"output_text","text":"m1"},{"type":"output_text","text":" \t"},{"type":"output_text","text":""}]},
		{"type":"message","content":[{"type":"output_text","text":"m2"}]},
		{"type":"function_call","name":"f","call_id":"call_1","arguments":"{}"},
		{"type":"reasoning","content":[{"type":"reasoning_text","text":"q2"}],"summary":[]},
		{"type":"reasoning","id":"rs_display","content":[{"type":"summary_text","text":"v3"}]},
		{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"m3"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out, nil, nil, nil)
	if len(content) != 9 {
		t.Fatalf("content parts = %d, want 9: %#v", len(content), content)
	}
	th := content[0]
	if th.Kind != llm.ContentThinking || th.Thinking == nil {
		t.Fatalf("content[0] = %#v, want thinking part", th)
	}
	if th.Thinking.Text != " α\n\nβ \n\nv2\n\n\t\n\nγ\n" {
		t.Fatalf("settled thinking text = %q", th.Thinking.Text)
	}
	if th.Thinking.ID != " \trs_1 " {
		t.Fatalf("thinking id = %q", th.Thinking.ID)
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
		{Type: "reasoning", ID: " \trs_1 ", Content: []replayText{{"reasoning_text", " α\n\nβ "}, {"reasoning_text", "\t"}, {"reasoning_text", "γ\n"}}, Summary: []replayText{{"summary_text", " \ns1\t "}, {"summary_text", ""}, {"summary_text", "\t \n"}}},
		{Type: "message", Content: []replayText{{"output_text", "m1"}, {"output_text", " \t"}, {"output_text", ""}}},
		{Type: "message", Content: []replayText{{"output_text", "m2"}}},
		{Type: "function_call", CallID: "call_1", Name: "f", Arguments: "{}"},
		{Type: "reasoning", Content: []replayText{{"reasoning_text", "q2"}}, Summary: []replayText{}},
		{Type: "message", Phase: "final_answer", Content: []replayText{{"output_text", "m3"}}},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("replayed items = %#v, want %#v", items, want)
	}
	strict := replayInput(t, msg, false)
	group := replayItem{Type: "message", Content: []replayText{{"output_text", "m1"}, {"output_text", "m2"}}}
	if len(strict) == 0 || !reflect.DeepEqual(strict[0], group) {
		t.Fatalf("strict target grouping = %#v, want first group %#v", strict, group)
	}
	for _, it := range strict {
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
