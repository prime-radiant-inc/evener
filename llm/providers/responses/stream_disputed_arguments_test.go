package responses

import (
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestResponsesDisputedArgumentsSettleOnTheirIssuedCall repeats an issued item
// identifier while naming a not-yet-issued call: the finalized arguments must
// reach the later call, never the item's owner. Live and recorded decoding must
// dispatch the two issued calls in order with their own exact bytes, and the
// live stream must announce each dispatched call exactly once.
func TestResponsesDisputedArgumentsSettleOnTheirIssuedCall(t *testing.T) {
	t.Parallel()
	const (
		good = `{ "a" : 1 }`
		bad  = `{ "b" : 2 }`
	)
	type call struct{ id, itemID, name, arguments string }
	const (
		firstAdded = `{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_first","call_id":"c1","name":"write_file"}}`
		firstDelta = `{"type":"response.function_call_arguments.delta","item_id":"fc_first","delta":"{"}`
		firstDone  = `{"type":"response.function_call_arguments.done","id":"c1","output_index":4,"arguments":"{ \"a\" : 1 }"}`
		// The finalized frame names issued item fc_first but not-yet-issued call c2.
		disputedDone = `{"type":"response.function_call_arguments.done","item_id":"fc_first","call_id":"c2","arguments":"{ \"b\" : 2 }"}`
		secondAdded  = `{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","id":"fc_second","call_id":"c2","name":"f"}}`
		terminal     = `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_first","call_id":"c1","name":"write_file","arguments":"{ \"a\" : 1 }"},{"type":"function_call","id":"fc_second","call_id":"c2","name":"f","arguments":"{ \"b\" : 2 }"}]}}`
	)
	want := []call{{"c1", "fc_first", "write_file", good}, {"c2", "fc_second", "f", bad}}
	wantStarts := []call{{"c1", "fc_first", "write_file", ""}, {"c2", "fc_second", "f", ""}}
	check := func(t *testing.T, label string, frames []string) {
		t.Helper()
		events, live := responsesEvents(t, frames...)
		recorded, err := ExtractRecordedResponse([]byte("data: "+strings.Join(frames, "\n\ndata: ")+"\n\n"), "gpt-5.5")
		if err != nil {
			t.Fatal(err)
		}
		var starts, ends []call
		for _, ev := range events {
			switch ev.Type {
			case llm.StreamEventToolCallStart:
				starts = append(starts, call{ev.ToolCall.ID, ev.ToolCall.ItemID, ev.ToolCall.Name, ""})
			case llm.StreamEventToolCallEnd:
				ends = append(ends, call{ev.ToolCall.ID, ev.ToolCall.ItemID, ev.ToolCall.Name, ev.ToolCall.WireArguments()})
			}
		}
		if !slices.Equal(starts, wantStarts) {
			t.Errorf("%s: live tool-call starts = %+v, want %+v", label, starts, wantStarts)
		}
		if !slices.Equal(ends, want) {
			t.Errorf("%s: live tool-call ends = %+v, want %+v", label, ends, want)
		}
		for _, response := range []struct {
			source string
			result *llm.Response
		}{{"live", live}, {"recorded", &recorded}} {
			var calls []call
			for _, c := range response.result.ToolCalls() {
				calls = append(calls, call{c.ID, c.ItemID, c.Name, c.WireArguments()})
			}
			if !slices.Equal(calls, want) {
				t.Errorf("%s: %s calls = %+v, want %+v", label, response.source, calls, want)
			}
		}
	}
	// A second output_item.added issues c2; otherwise only the terminal does.
	check(t, "later output item settles the disputed call", []string{firstAdded, firstDelta, firstDone, disputedDone, secondAdded, terminal})
	check(t, "later terminal record settles the disputed call", []string{firstAdded, firstDelta, firstDone, disputedDone, terminal})
}
