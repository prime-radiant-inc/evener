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
