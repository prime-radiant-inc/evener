package responses

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

// normalizeResponse returns a shallow comparison copy. Every response field
// must match across equivalent SSE framings.
func normalizeResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func sameAccumulatedResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeResponse(a), normalizeResponse(b))
}

// FuzzResponsesStreamMetamorphic asserts a metamorphic property of the
// Responses SSE decoder (research §4 — metamorphic, not differential),
// driven through the real transport (Protocol.Stream against an httptest
// server) rather than the decoder in isolation: parsing a stream of SSE
// frames and then re-parsing the SAME logical stream after a
// semantics-preserving transformation must accumulate the identical
// llm.Response.
//
// Two transforms are applied, each compared against the untransformed
// baseline:
//
//   - Re-chunking: the server flushes the response one byte at a time
//     instead of in one write, forcing different read boundaries on the
//     client. Read boundaries must not influence the parsed result.
//   - Interstitial comments: insert an SSE comment frame (":...") after
//     every event boundary. Comment lines and empty events carry no data,
//     so the decoded result must be unchanged.
//
// A divergence here is a real decoder bug (stateful accumulation that leaks
// across read boundaries or mis-handles benign framing), not a test
// artifact.
func FuzzResponsesStreamMetamorphic(f *testing.F) {
	textStream := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"Hello"}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":" world"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"

	toolStream := `data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"fc_1","name":"shell"}}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"cmd\":"}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"\"ls\"}"}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.done","call_id":"call_1","arguments":"{\"cmd\":\"ls\"}"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"type":"function_call","call_id":"call_1","id":"fc_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}]}}` + "\n\n"

	reasoningStream := `data: {"type":"response.reasoning_summary_part.added"}` + "\n\n" +
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}` + "\n\n" +
		`data: {"type":"response.output_text.delta","delta":"answer"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_3","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}}` + "\n\n"

	// itemIDToolStream exercises the id-mapping fallbacks the call_id-keyed seeds
	// above skip: an output_item.added that carries only an item "id" (no call_id),
	// then a delta/done addressed by item_id, with arguments arriving via the
	// "arguments" field rather than "delta".
	itemIDToolStream := `data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_9","name":"grep"}}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_9","arguments":"{\"q\":1}"}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_9","arguments":"{\"q\":1}"}` + "\n\n" +
		`data: {"type":"response.completed","id":"resp_4","status":"completed","output":[]}` + "\n\n"

	// itemDoneToolStream drives the response.output_item.done function_call path
	// (carried under the alternate "output_item" key) end-to-end.
	itemDoneToolStream := `data: {"type":"response.output_item.done","output_item":{"type":"function_call","call_id":"call_z","id":"fc_z","name":"shell","arguments":"{}"}}` + "\n\n"

	// textThenItemDone starts a text segment then closes it via a non-function_call
	// output_item.done (the best-effort end-of-text branch).
	textThenItemDone := `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
		`data: {"type":"response.output_item.done","item":{"type":"message"}}` + "\n\n"

	seeds := []string{
		textStream,
		toolStream,
		reasoningStream,
		itemIDToolStream,
		itemDoneToolStream,
		textThenItemDone,
		"data: not-json\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
		`data: {"type":"response.completed","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n",
		// The event type taken from the SSE `event:` line when the data has no "type".
		"event: response.output_text.delta\n" + `data: {"delta":"viaEvent"}` + "\n\n",
		// output_text.delta whose text rides the "text" field, not "delta".
		`data: {"type":"response.output_text.delta","text":"viaText"}` + "\n\n",
		// Empty deltas: the text and reasoning early-return guards.
		`data: {"type":"response.output_text.delta"}` + "\n\n" +
			`data: {"type":"response.reasoning_summary_text.delta"}` + "\n\n",
		// A standalone function_call_arguments.done keyed by call_id (no prior added).
		`data: {"type":"response.function_call_arguments.done","call_id":"call_d","arguments":"{\"a\":1}"}` + "\n\n",
		// An output_item.done with no item payload, after text started.
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
			`data: {"type":"response.output_item.done"}` + "\n\n",
		// An unrecognized event type → the default raw-passthrough branch.
		`data: {"type":"response.totally_unknown_event"}` + "\n\n",
		// GPT-5.6 streams interleave item types evener does not use (program /
		// program_output / tool_search_call / compaction); the decoder must
		// tolerate them around ordinary text and tool calls.
		`data: {"type":"response.output_item.added","item":{"type":"program","id":"prog_1"}}` + "\n\n" +
			`data: {"type":"response.output_item.added","item":{"type":"tool_search_call","id":"ts_1","status":"in_progress"}}` + "\n\n" +
			`data: {"type":"response.output_item.done","item":{"type":"program_output","id":"po_1","output":"ok"}}` + "\n\n" +
			`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n" +
			`data: {"type":"response.output_item.done","item":{"type":"compaction","id":"cmp_1"}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_56","model":"gpt-5.6-sol","status":"completed","output":[{"type":"program","id":"prog_1"},{"type":"message","content":[{"type":"output_text","text":"hi"}]},{"type":"compaction","id":"cmp_1"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n",
		// A failure delivered in-band on a 200 stream: the flat "error"
		// event, whose typed error ends the stream.
		"event: error\n" + `data: {"type":"error","message":"slow down","code":429}` + "\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	// One shared server for the whole run (like the old test's one shared
	// Adapter): the served bytes and write granularity are set by each call
	// to stream below, and every call fully drains the response before the
	// next one reassigns them, so reuse across sequential fuzz calls is
	// safe. Sharing a server also keeps the accumulated Response's stamped
	// endpoint URL identical across variants, so it doesn't spuriously
	// break the DeepEqual comparison below.
	var body []byte
	var flushEachByte bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if !flushEachByte {
			_, _ = w.Write(body)
			return
		}
		flusher, _ := w.(http.Flusher)
		for _, b := range body {
			_, _ = w.Write([]byte{b})
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	f.Cleanup(srv.Close)
	res := liveRes(srv, openaiCaps)
	proto := &Protocol{Client: srv.Client()}

	// stream drives the Responses protocol's public Stream method against
	// the shared server serving sse, and folds the emitted stream events
	// back into a final llm.Response, exactly as the live completion path
	// does. It returns the accumulated response (nil if the stream never
	// completed) and whether any error event was emitted.
	stream := func(sse []byte, chunkOneByte bool) (*llm.Response, bool) {
		body, flushEachByte = sse, chunkOneByte
		req := llm.Request{Model: "fuzz-model"}
		s, err := proto.Stream(context.Background(), req, res)
		if err != nil {
			return nil, true
		}
		defer func() { _ = s.Close() }()

		acc := llm.NewStreamAccumulator()
		sawError := false
		for ev := range s.Events() {
			if ev.Type == llm.StreamEventError {
				sawError = true
				continue
			}
			acc.Process(ev)
		}
		return acc.Response(), sawError
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := stream(raw, false) // Oracle (floor): never panics.

		rechunked, reErr := stream(raw, true)
		if !sameAccumulatedResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		// Insert a comment-only event after every SSE event boundary. SSE comment
		// lines and data-less events are ignored, so the result must not change.
		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := stream(commented, false)
		if !sameAccumulatedResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
