package responses

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/llm"
)

// responsesEvents drives one Responses SSE stream through the real
// Protocol.Stream against a local httptest server and returns every emitted
// stream event plus the accumulated final response.
func responsesEvents(t *testing.T, events ...string) ([]llm.StreamEvent, *llm.Response) {
	t.Helper()
	sse := ""
	for _, ev := range events {
		sse += "data: " + ev + "\n\n"
	}
	srv, _ := server(t, 200, sse)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer s.Close() //nolint:errcheck
	var out []llm.StreamEvent
	acc := llm.NewStreamAccumulator()
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("unexpected stream error: %v", ev.Err)
		}
		out = append(out, ev)
		acc.Process(ev)
	}
	return out, acc.Response()
}

// hasEventType reports whether any collected event has the given type.
func hasEventType(events []llm.StreamEvent, typ llm.StreamEventType) bool {
	for _, ev := range events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// toolEvent returns the first event of the given tool-call type.
func toolEvent(t *testing.T, events []llm.StreamEvent, typ llm.StreamEventType) *llm.ToolCallData {
	t.Helper()
	for _, ev := range events {
		if ev.Type == typ {
			return ev.ToolCall
		}
	}
	t.Fatalf("no %v event in the stream", typ)
	return nil
}

// TestResponsesStreamToolStateMapping drives the accumulator's id-resolution
// arms — the state machine output_item.added / function_call_arguments
// delta+done / output_item.done share — through the live decoder, asserting
// the tool-call events each mapping must produce.
func TestResponsesStreamToolStateMapping(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_ts","status":"completed","output":[]}}`

	t.Run("delta keyed by a foreign call id falls back to item state", func(t *testing.T) {
		events, final := responsesEvents(t,
			`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","id":"fc_a","name":"orig"}}`,
			`{"type":"response.function_call_arguments.delta","call_id":"call_other","item_id":"fc_a","delta":"{\"x\":"}`,
			`{"type":"response.function_call_arguments.delta","call_id":"call_other","item_id":"fc_a","delta":"1}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","arguments":"{\"x\":1}"}}`,
			completed,
		)
		start := toolEvent(t, events, llm.StreamEventToolCallStart)
		if start.ID != "call_a" || start.Name != "orig" {
			t.Fatalf("start = %+v, want the state filed under fc_a (id call_a, name orig)", start)
		}
		calls := final.ToolCalls()
		if len(calls) != 1 || calls[0].ID != "call_a" || calls[0].Name != "orig" || string(calls[0].Arguments) != `{"x":1}` {
			t.Fatalf("final calls = %+v", calls)
		}
	})

	t.Run("delta addressed only by payload id", func(t *testing.T) {
		events, final := responsesEvents(t,
			`{"type":"response.function_call_arguments.delta","id":"call_by_id","delta":"{\"i\":2}"}`,
			`{"type":"response.function_call_arguments.done","call_id":"call_by_id","arguments":"{\"i\":2}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_by_id","arguments":"{\"i\":2}"}}`,
			completed,
		)
		start := toolEvent(t, events, llm.StreamEventToolCallStart)
		if start.ID != "call_by_id" {
			t.Fatalf("start = %+v, want the payload id as the call id", start)
		}
		if calls := final.ToolCalls(); len(calls) != 1 || string(calls[0].Arguments) != `{"i":2}` {
			t.Fatalf("final calls = %+v", calls)
		}
	})

	t.Run("delta keyed by an unannounced item id maps to itself", func(t *testing.T) {
		events, final := responsesEvents(t,
			`{"type":"response.function_call_arguments.delta","item_id":"fc_ghost","delta":"{\"g\":1}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"fc_ghost","arguments":"{\"g\":1}"}}`,
			completed,
		)
		start := toolEvent(t, events, llm.StreamEventToolCallStart)
		if start.ID != "fc_ghost" {
			t.Fatalf("start = %+v, want the unknown item id to key its own state", start)
		}
		if calls := final.ToolCalls(); len(calls) != 1 || calls[0].ID != "fc_ghost" {
			t.Fatalf("final calls = %+v", calls)
		}
	})

	t.Run("unmappable delta and done pass through raw", func(t *testing.T) {
		events, _ := responsesEvents(t,
			`{"type":"response.function_call_arguments.delta","delta":"x"}`,
			`{"type":"response.function_call_arguments.done","arguments":"{}"}`,
			completed,
		)
		if hasEventType(events, llm.StreamEventToolCallStart) || hasEventType(events, llm.StreamEventToolCallEnd) {
			t.Fatalf("an unmappable delta must not start or end a tool call: %+v", events)
		}
		var passthrough int
		for _, ev := range events {
			if ev.Type == llm.StreamEventProviderEvent {
				passthrough++
			}
		}
		if passthrough != 2 {
			t.Fatalf("both unmappable frames must pass through raw, got %d: %+v", passthrough, events)
		}
	})

	t.Run("done keyed by an unannounced item id maps without passthrough", func(t *testing.T) {
		events, _ := responsesEvents(t,
			`{"type":"response.function_call_arguments.done","item_id":"fc_g2","arguments":"{\"g2\":1}"}`,
			completed,
		)
		if hasEventType(events, llm.StreamEventProviderEvent) {
			t.Fatalf("a resolvable done must not pass through raw: %+v", events)
		}
	})

	t.Run("added item without ids is ignored", func(t *testing.T) {
		events, _ := responsesEvents(t,
			`{"type":"response.output_item.added","item":{"type":"function_call"}}`,
			completed,
		)
		if hasEventType(events, llm.StreamEventToolCallStart) || hasEventType(events, llm.StreamEventProviderEvent) {
			t.Fatalf("an id-less added item must be ignored: %+v", events)
		}
	})

	t.Run("done item carrying only its id resolves through the item map", func(t *testing.T) {
		events, final := responsesEvents(t,
			`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_m","id":"fc_m","name":"mapped"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_m","arguments":"{\"m\":1}"}}`,
			completed,
		)
		end := toolEvent(t, events, llm.StreamEventToolCallEnd)
		if end.ID != "call_m" || end.Name != "mapped" || string(end.Arguments) != `{"m":1}` {
			t.Fatalf("end = %+v, want resolution via the item→call map", end)
		}
		if calls := final.ToolCalls(); len(calls) != 1 || calls[0].ID != "call_m" {
			t.Fatalf("final calls = %+v", calls)
		}
	})

	t.Run("done function_call with an unknown id passes through raw", func(t *testing.T) {
		events, _ := responsesEvents(t,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_unknown","name":"f"}}`,
			completed,
		)
		if hasEventType(events, llm.StreamEventToolCallEnd) {
			t.Fatalf("an unresolvable done must not end a tool call: %+v", events)
		}
		if !hasEventType(events, llm.StreamEventProviderEvent) {
			t.Fatalf("an unresolvable function_call done must pass through raw: %+v", events)
		}
	})

	t.Run("a name arriving on a later delta names the existing state", func(t *testing.T) {
		events, _ := responsesEvents(t,
			`{"type":"response.function_call_arguments.delta","call_id":"call_c","delta":"{}"}`,
			`{"type":"response.function_call_arguments.delta","call_id":"call_c","name":"named_late","delta":"{}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_c","arguments":"{}"}}`,
			completed,
		)
		end := toolEvent(t, events, llm.StreamEventToolCallEnd)
		if end.Name != "named_late" {
			t.Fatalf("end name = %q, want the late delta name", end.Name)
		}
	})

	t.Run("a done without arguments settles on the accumulated deltas", func(t *testing.T) {
		events, final := responsesEvents(t,
			`{"type":"response.function_call_arguments.delta","call_id":"call_q","delta":"{\"a\":"}`,
			`{"type":"response.function_call_arguments.delta","call_id":"call_q","delta":"1}"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_q","name":"done_name"}}`,
			completed,
		)
		end := toolEvent(t, events, llm.StreamEventToolCallEnd)
		if string(end.Arguments) != `{"a":1}` {
			t.Fatalf("end args = %s, want the accumulated deltas", end.Arguments)
		}
		if end.Name != "done_name" {
			t.Fatalf("end name = %q, want the done item's late name", end.Name)
		}
		if calls := final.ToolCalls(); len(calls) != 1 || string(calls[0].Arguments) != `{"a":1}` {
			t.Fatalf("final calls = %+v", calls)
		}
	})

	t.Run("an event frame with no data is tolerated", func(t *testing.T) {
		srv, _ := server(t, 200, "event: response.created\n\ndata: "+completed+"\n\n")
		s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		defer s.Close() //nolint:errcheck
		for ev := range s.Events() {
			if ev.Type == llm.StreamEventError {
				t.Fatalf("a data-less event frame must not fail the stream: %v", ev.Err)
			}
		}
	})
}

// TestResponsesInbandErrorDecoding drives responsesInbandError's shape arms
// through the live stream: an undecodable error frame and an empty envelope
// stay raw passthroughs without failing the stream, while a code-only nested
// failure ends the stream with the typed in-band error.
func TestResponsesInbandErrorDecoding(t *testing.T) {
	drain := func(t *testing.T, sse string) (passthrough bool, streamErr error) {
		t.Helper()
		srv, _ := server(t, 200, sse)
		s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		defer s.Close() //nolint:errcheck
		for ev := range s.Events() {
			switch ev.Type {
			case llm.StreamEventProviderEvent:
				passthrough = true
			case llm.StreamEventError:
				streamErr = ev.Err
			}
		}
		return passthrough, streamErr
	}

	t.Run("undecodable error frame passes through", func(t *testing.T) {
		// The payload's first JSON value names an error, but the trailing
		// bytes make the whole frame undecodable for responsesInbandError —
		// it must return nothing and the frame must stay a raw passthrough.
		const completed = `data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}` + "\n\n"
		passthrough, streamErr := drain(t, "event: error\ndata: {\"type\":\"error\",\"message\":\"m\"} junk\n\n"+completed)
		if !passthrough || streamErr != nil {
			t.Fatalf("an undecodable error frame must stay a passthrough without ending the stream: passthrough=%v err=%v", passthrough, streamErr)
		}
	})

	t.Run("empty error envelope passes through", func(t *testing.T) {
		const completed = `data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}` + "\n\n"
		passthrough, streamErr := drain(t, "event: error\ndata: {}\n\n"+completed)
		if !passthrough || streamErr != nil {
			t.Fatalf("an error frame with no error payload must stay a passthrough without ending the stream: passthrough=%v err=%v", passthrough, streamErr)
		}
	})

	t.Run("code-only failure ends the stream typed", func(t *testing.T) {
		_, streamErr := drain(t, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":429}}}\n\n")
		le, ok := errors.AsType[llm.Error](streamErr)
		if !ok || le.StatusCode() != 429 || le.Provider() != "oai-prod" {
			t.Fatalf("stream error = %v, want a typed 429 stamped with the instance", streamErr)
		}
	})
}
