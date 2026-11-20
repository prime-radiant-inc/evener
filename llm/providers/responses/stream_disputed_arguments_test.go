package responses

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

const (
	goodArgs = `{ "a" : 1 }`
	badArgs  = `{ "b" : 2 }`
	altArgs  = `{ "z" : 9 }`

	addedFirst = `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","call_id":"c1","name":"write_file"}}`
	firstDelta = `{"type":"response.function_call_arguments.delta","item_id":"fc_first","delta":"{"}`
	firstDone  = `{"type":"response.function_call_arguments.done","id":"c1","output_index":4,"arguments":"{ \"a\" : 1 }"}`
	addedOwned = `{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_second","call_id":"c2","name":"f"}}`
	firstEnd   = `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","call_id":"c1","name":"write_file"}}`
	secondEnd  = `{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_second","call_id":"c2","name":"f"}}`
	noOutput   = `{"type":"response.completed","response":{"status":"completed","output":[]}}`
)

// call is one tool call as the wire or the decoder states it.
type call struct{ id, itemID, name, args string }

func disputedDone(args string) string {
	return `{"type":"response.function_call_arguments.done","item_id":"fc_first","call_id":"c2","arguments":` + strconv.Quote(args) + "}"
}

func disputedDelta(delta string) string {
	return `{"type":"response.function_call_arguments.delta","item_id":"fc_first","call_id":"c2","delta":` + strconv.Quote(delta) + "}"
}

func terminalWith(items ...string) string {
	return `{"type":"response.completed","response":{"status":"completed","output":[` + strings.Join(items, ",") + `]}}`
}

func deltaFor(callID, delta string) string {
	return `{"type":"response.function_call_arguments.delta","call_id":"` + callID + `","delta":` + strconv.Quote(delta) + "}"
}

func terminalCall(id, callID, name, args string) string {
	return `{"type":"function_call","id":"` + id + `","call_id":"` + callID + `","name":"` + name + `","arguments":` + strconv.Quote(args) + "}"
}

// decodeBoth drives frames through the live Protocol.Stream and the recorded
// replay, so every case asserts both decoders.
func decodeBoth(t *testing.T, frames ...string) ([]llm.StreamEvent, *llm.Response, *llm.Response) {
	t.Helper()
	events, live := responsesEvents(t, frames...)
	recorded, err := ExtractRecordedResponse([]byte("data: "+strings.Join(frames, "\n\ndata: ")+"\n\n"), "gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	return events, live, &recorded
}

func callEvents(events []llm.StreamEvent, typ llm.StreamEventType) []call {
	var calls []call
	for _, ev := range events {
		if ev.Type == typ && ev.ToolCall != nil {
			calls = append(calls, call{ev.ToolCall.ID, ev.ToolCall.ItemID, ev.ToolCall.Name, ev.ToolCall.WireArguments()})
		}
	}
	return calls
}

func settledCalls(r *llm.Response) []call {
	var calls []call
	for _, c := range r.ToolCalls() {
		calls = append(calls, call{c.ID, c.ItemID, c.Name, c.WireArguments()})
	}
	return calls
}

func passthroughs(events []llm.StreamEvent) int {
	n := 0
	for _, ev := range events {
		if ev.Type == llm.StreamEventProviderEvent {
			n++
		}
	}
	return n
}

// testResponsesDisputedCallSettles checks the live announcement and both
// decoded responses for one disputed-call fixture.
func testResponsesDisputedCallSettles(t *testing.T, frames []string, starts, calls []call) {
	t.Helper()
	events, live, recorded := decodeBoth(t, frames...)
	if got := callEvents(events, llm.StreamEventToolCallStart); !slices.Equal(got, starts) {
		t.Errorf("live starts = %+v, want %+v", got, starts)
	}
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, calls) {
		t.Errorf("live ends = %+v, want %+v", got, calls)
	}
	assertSettledCalls(t, live, recorded, calls)
}

func assertSettledCalls(t *testing.T, live, recorded *llm.Response, want []call) {
	t.Helper()
	for _, r := range []struct {
		source string
		result *llm.Response
	}{{"live", live}, {"recorded", recorded}} {
		if got := settledCalls(r.result); !slices.Equal(got, want) {
			t.Errorf("%s calls = %+v, want %+v", r.source, got, want)
		}
	}
}

// The item-added case must depend on the accumulated bytes, not terminal copies:
// both items end without arguments and the terminal carries no output.
func TestResponsesDisputedArgumentsSettleOnTheirIssuedCall(t *testing.T) {
	t.Parallel()
	want := []call{{"c1", "fc_first", "write_file", goodArgs}, {"c2", "fc_second", "f", badArgs}}
	starts := []call{{"c1", "fc_first", "write_file", ""}, {"c2", "fc_second", "f", ""}}
	t.Run("item ends settle the accumulated bytes", func(t *testing.T) {
		frames := []string{addedFirst, firstDelta, firstDone, disputedDone(badArgs), addedOwned, firstEnd, secondEnd, noOutput}
		testResponsesDisputedCallSettles(t, frames, starts, want)
	})
	t.Run("terminal announces the disputed call", func(t *testing.T) {
		frames := []string{addedFirst, firstDelta, firstDone, disputedDone(badArgs),
			terminalWith(terminalCall("fc_first", "c1", "write_file", goodArgs), terminalCall("fc_second", "c2", "f", badArgs))}
		testResponsesDisputedCallSettles(t, frames, starts, want)
	})
}

// A terminal that issues the disputed call without arguments must settle it
// with the bytes the stream already buffered, live and recorded.
func TestResponsesDisputedArgumentsFillTerminalBytes(t *testing.T) {
	t.Parallel()
	want := []call{{"c2", "fc_second", "f", badArgs}}
	starts := []call{{"c2", "fc_second", "f", ""}}
	terminal := terminalWith(terminalCall("fc_second", "c2", "f", ""))
	t.Run("terminal first issues the call", func(t *testing.T) {
		testResponsesDisputedCallSettles(t, []string{addedFirst, disputedDone(badArgs), terminal}, starts, want)
	})
	t.Run("added released the bytes first", func(t *testing.T) {
		testResponsesDisputedCallSettles(t, []string{addedFirst, disputedDone(badArgs), addedOwned, terminal}, starts, want)
	})
}

// Observations for one call settle in arrival order, and a later done with
// arguments still replaces every earlier fragment.
func TestResponsesDisputedArgumentsKeepArrivalOrder(t *testing.T) {
	t.Parallel()
	starts := []call{{"c2", "fc_second", "f", ""}}
	prefix, suffix := disputedDelta(`{ "a"`), deltaFor("c2", " : 1 }")
	t.Run("fragments append in arrival order", func(t *testing.T) {
		frames := []string{addedFirst, prefix, suffix, addedOwned, secondEnd, noOutput}
		want := []call{{"c2", "fc_second", "f", goodArgs}}
		testResponsesDisputedCallSettles(t, frames, starts, want)
	})
	t.Run("later done replaces earlier fragments", func(t *testing.T) {
		frames := []string{addedFirst, prefix, suffix, disputedDone(altArgs), addedOwned, secondEnd, noOutput}
		want := []call{{"c2", "fc_second", "f", altArgs}}
		testResponsesDisputedCallSettles(t, frames, starts, want)
	})
}

// An output_item.done that completes the disputed call by its explicit ID must
// not complete the item's issued owner a second time.
func TestResponsesDisputedCompletionKeepsBothCalls(t *testing.T) {
	t.Parallel()
	disputedEnd := `{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_first","call_id":"c2","name":"f","arguments":"{ \"b\" : 2 }"}}`
	frames := []string{addedFirst, firstDelta, firstDone, firstEnd, disputedEnd, noOutput}
	starts := []call{{"c1", "fc_first", "write_file", ""}, {"c2", "", "f", ""}}
	want := []call{{"c1", "fc_first", "write_file", goodArgs}, {"c2", "", "f", badArgs}}
	testResponsesDisputedCallSettles(t, frames, starts, want)
}

// Buffered frames emit nothing: no passthrough may mark the attempt delivered
// while no call has been announced.
func TestResponsesDisputedFramesEmitNoEvent(t *testing.T) {
	t.Parallel()
	frames := []string{addedFirst, disputedDelta(`{ "b"`), disputedDone(badArgs), addedOwned, secondEnd, noOutput}
	events, _, _ := decodeBoth(t, frames...)
	if got := passthroughs(events); got != 0 {
		t.Errorf("provider passthrough events = %d, want 0", got)
	}
}

// A done frame with no arguments must not erase fragments buffered before it.
func TestResponsesDisputedEmptyDonePreservesDeltas(t *testing.T) {
	t.Parallel()
	emptyDone := `{"type":"response.function_call_arguments.done","item_id":"fc_first","call_id":"c2"}`
	frames := []string{addedFirst, disputedDelta(`{ "a"`), deltaFor("c2", " : 1 }"), emptyDone, addedOwned, secondEnd, noOutput}
	starts := []call{{"c2", "fc_second", "f", ""}}
	want := []call{{"c2", "fc_second", "f", goodArgs}}
	testResponsesDisputedCallSettles(t, frames, starts, want)
}

// An item registered by item ID alone keeps that synthesized call ID, but a done
// frame's real call ID must not look like a competing owner: its bytes land on
// the item's state, so a later argumentless item end completes with them.
func TestResponsesItemKeyedCallKeepsBufferedBytes(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	argsDone := `{"type":"response.function_call_arguments.done","item_id":"fc_first","call_id":"c9","arguments":"{ \"a\" : 1 }"}`
	itemEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	frames := []string{added, argsDone, itemEnd, noOutput}
	starts := []call{{"fc_first", "fc_first", "f", ""}}
	want := []call{{"fc_first", "fc_first", "f", goodArgs}}
	testResponsesDisputedCallSettles(t, frames, starts, want)
}

// A terminal that names an item-keyed call only by its real call ID must settle
// it with the bytes the stream buffered under the item ID.
func TestResponsesItemKeyedCallFillsTerminalBytes(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	argsDone := `{"type":"response.function_call_arguments.done","item_id":"fc_first","call_id":"c9","arguments":"{ \"a\" : 1 }"}`
	terminal := terminalWith(terminalCall("fc_first", "c9", "f", ""))
	starts := []call{{"c9", "fc_first", "f", ""}}
	want := []call{{"c9", "fc_first", "f", goodArgs}}
	testResponsesDisputedCallSettles(t, []string{added, argsDone, terminal}, starts, want)
}

// A call ended under a synthesized item ID must not be announced again when the
// terminal supplies its real call ID.
func TestResponsesLateCallIDDoesNotRepeatAnnouncement(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	itemEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f","arguments":"{ \"a\" : 1 }"}}`
	frames := []string{added, itemEnd, terminalWith(terminalCall("fc_first", "c9", "f", goodArgs))}
	events, live, recorded := decodeBoth(t, frames...)
	ends := []call{{"fc_first", "fc_first", "f", goodArgs}}
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, ends) {
		t.Errorf("live ends = %+v, want exactly %+v", got, ends)
	}
	if got := len(callEvents(events, llm.StreamEventToolCallStart)); got != 1 {
		t.Errorf("live starts = %d, want 1", got)
	}
	want := []call{{"c9", "fc_first", "f", goodArgs}}
	assertSettledCalls(t, live, recorded, want)
}

// A disputed output_item.done must not rebind the item's issued owner, or a
// later item-only completion resolves to the disputed call and the first call
// is lost.
func TestResponsesDisputedItemDoneKeepsOwnerMapping(t *testing.T) {
	t.Parallel()
	disputedEnd := `{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"fc_first","call_id":"c2","name":"f","arguments":"{ \"b\" : 2 }"}}`
	itemOnlyEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"write_file"}}`
	frames := []string{addedFirst, firstDelta, firstDone, disputedDone(badArgs), disputedEnd, itemOnlyEnd, noOutput}
	ends := []call{{"c2", "", "f", badArgs}, {"c1", "fc_first", "write_file", goodArgs}}
	want := []call{{"c1", "fc_first", "write_file", goodArgs}, {"c2", "", "f", badArgs}}
	events, live, recorded := decodeBoth(t, frames...)
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, ends) {
		t.Errorf("live ends = %+v, want %+v", got, ends)
	}
	assertSettledCalls(t, live, recorded, want)
}

// A call ID learned from a frame is the item state's own: later frames naming
// it must keep appending there, not open a provisional bucket that drops the
// prefix.
func TestResponsesLateCallIDKeepsAppendingFrames(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	first := `{"type":"response.function_call_arguments.delta","item_id":"fc_first","call_id":"c9","delta":"{ \"a\""}`
	second := `{"type":"response.function_call_arguments.delta","item_id":"fc_first","call_id":"c9","delta":" : 1 }"}`
	itemEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","call_id":"c9","name":"f"}}`
	starts := []call{{"fc_first", "fc_first", "f", ""}}
	want := []call{{"fc_first", "fc_first", "f", goodArgs}}
	testResponsesDisputedCallSettles(t, []string{added, first, second, itemEnd, noOutput}, starts, want)
}

// A terminal call that shares an item ID with another issued call must not be
// filled with that call's bytes: the item's owner is not this call.
func TestResponsesTerminalCallDoesNotBorrowOwnerBytes(t *testing.T) {
	t.Parallel()
	terminal := terminalWith(terminalCall("fc_first", "c1", "write_file", goodArgs), terminalCall("fc_first", "c2", "f", ""))
	frames := []string{addedFirst, firstDelta, firstDone, terminal}
	starts := []call{{"c1", "fc_first", "write_file", ""}, {"c2", "fc_first", "f", ""}}
	want := []call{{"c1", "fc_first", "write_file", goodArgs}, {"c2", "fc_first", "f", ""}}
	testResponsesDisputedCallSettles(t, frames, starts, want)
}

// A disputed call that has since been issued takes later observations directly:
// they are announced on it, not buffered away behind the dispute.
func TestResponsesIssuedDisputedCallTakesLaterFrames(t *testing.T) {
	t.Parallel()
	frames := []string{addedFirst, disputedDone(badArgs), addedOwned, disputedDelta("!"), secondEnd, noOutput}
	events, live, recorded := decodeBoth(t, frames...)
	deltas := callEvents(events, llm.StreamEventToolCallDelta)
	if len(deltas) != 1 || deltas[0].id != "c2" || deltas[0].args != "!" {
		t.Errorf("live deltas = %+v, want one c2 fragment %q", deltas, "!")
	}
	want := []call{{"c2", "fc_second", "f", badArgs + "!"}}
	assertSettledCalls(t, live, recorded, want)
}

// An item-keyed call that ended before the terminal learns its real call ID is
// announced once, live, even though the terminal names it by that ID.
func TestResponsesLearnedCallIDEndsOnce(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	delta := `{"type":"response.function_call_arguments.delta","item_id":"fc_first","call_id":"c9","delta":"{ \"a\" : 1 }"}`
	itemEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	frames := []string{added, delta, itemEnd, terminalWith(terminalCall("fc_first", "c9", "f", goodArgs))}
	events, live, recorded := decodeBoth(t, frames...)
	ends := []call{{"fc_first", "fc_first", "f", goodArgs}}
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, ends) {
		t.Errorf("live ends = %+v, want exactly %+v", got, ends)
	}
	want := []call{{"c9", "fc_first", "f", goodArgs}}
	assertSettledCalls(t, live, recorded, want)
}

// A content-filtered terminal retracted its answer: it must not borrow bytes the
// stream had accumulated.
func TestResponsesFilteredTerminalDoesNotBorrowBytes(t *testing.T) {
	t.Parallel()
	filtered := `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[` + terminalCall("fc_first", "c1", "write_file", "") + `]}}`
	frames := []string{addedFirst, firstDelta, firstDone, filtered}
	events, live, recorded := decodeBoth(t, frames...)
	ends := []call{{"c1", "fc_first", "write_file", ""}}
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, ends) {
		t.Errorf("live ends = %+v, want %+v", got, ends)
	}
	assertSettledCalls(t, live, recorded, ends)
}

// An added record that issues the disputed call under the item ID another call
// already owns must not take that item's mapping: the original call still
// completes from its item-only record.
func TestResponsesAddedDisputedCallKeepsItemOwner(t *testing.T) {
	t.Parallel()
	addedSameItem := `{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_first","call_id":"c2","name":"f"}}`
	itemOnlyEnd := `{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"write_file"}}`
	frames := []string{addedFirst, firstDelta, firstDone, disputedDone(badArgs), addedSameItem, itemOnlyEnd, noOutput}
	ends := []call{{"c1", "fc_first", "write_file", goodArgs}}
	events, live, recorded := decodeBoth(t, frames...)
	if got := callEvents(events, llm.StreamEventToolCallEnd); !slices.Equal(got, ends) {
		t.Errorf("live ends = %+v, want %+v", got, ends)
	}
	assertSettledCalls(t, live, recorded, ends)
}

// A call the stream started under its item ID keeps that ID for its end, while
// the settled response keeps the provider's real call ID for dispatch.
func TestResponsesTerminalCallEndUsesAnnouncedID(t *testing.T) {
	t.Parallel()
	added := `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","name":"f"}}`
	delta := `{"type":"response.function_call_arguments.delta","item_id":"fc_first","delta":"{ \"a\""}`
	frames := []string{added, delta, terminalWith(terminalCall("fc_first", "c9", "f", goodArgs))}
	events, live, recorded := decodeBoth(t, frames...)
	starts := callEvents(events, llm.StreamEventToolCallStart)
	if len(starts) != 1 || starts[0].id != "fc_first" {
		t.Errorf("live starts = %+v, want one fc_first start", starts)
	}
	ends := callEvents(events, llm.StreamEventToolCallEnd)
	if len(ends) != 1 || ends[0].id != "fc_first" {
		t.Errorf("live ends = %+v, want one fc_first end", ends)
	}
	want := []call{{"c9", "fc_first", "f", goodArgs}}
	assertSettledCalls(t, live, recorded, want)
}

// A call-ID-only done record does not issue its call: a later delta whose item
// belongs to another call must wait, not commit a delta for an unissued call.
func TestResponsesCallIDOnlyDoneDoesNotIssueDisputedCall(t *testing.T) {
	t.Parallel()
	callIDOnlyDone := `{"type":"response.function_call_arguments.done","call_id":"c2","arguments":"{ \"b\" : 2 }"}`
	frames := []string{addedFirst, firstDelta, firstDone, callIDOnlyDone, disputedDelta("!"), firstEnd, addedOwned, secondEnd, noOutput}
	events, live, recorded := decodeBoth(t, frames...)
	for _, d := range callEvents(events, llm.StreamEventToolCallDelta) {
		if d.id == "c2" {
			t.Errorf("c2 delta emitted before its call was issued: %+v", d)
		}
	}
	want := []call{{"c1", "fc_first", "write_file", goodArgs}, {"c2", "fc_second", "f", badArgs + "!"}}
	assertSettledCalls(t, live, recorded, want)
}
