package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
)

// byteAtATimeReader delivers its payload one byte per Read call, then io.EOF. It
// forces the Responses SSE decoder through a different read-segmentation than a
// bytes.Reader, which is what makes the re-chunk metamorphic oracle below mean
// something: an accumulator whose result depends on read boundaries is buggy.
type byteAtATimeReader struct {
	data []byte
	pos  int
}

func (r *byteAtATimeReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// accumulateResponsesSSE drives the Responses SSE decoder over sse and folds the
// emitted stream events back into a final llm.Response, exactly as the live
// completion path does (Adapter.completeViaStream). It returns the accumulated
// response (nil if the stream never completed) and whether any error event was
// emitted. When chunkOneByte is set, the bytes are delivered one at a time to
// vary read boundaries without changing the logical stream.
func accumulateResponsesSSE(a *Adapter, sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go a.decodeResponsesStream(ctx, cancel, resp, s, llm.Request{Model: "fuzz-model"}, nil)

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

// normalizeResponse copies r with the framing-dependent raw-body fields cleared.
// Those carry the verbatim SSE bytes when raw-body logging is enabled, which the
// semantics-preserving transforms legitimately alter; everything else (text,
// tool calls, reasoning, finish, usage, the parsed Raw object) must match.
func normalizeResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	cp.RawRequestBody = ""
	cp.RawResponseBody = ""
	return &cp
}

func sameAccumulatedResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeResponse(a), normalizeResponse(b))
}

// FuzzOpenAIResponsesMetamorphic asserts a metamorphic property of the OpenAI
// Responses SSE decoder (research §4 — metamorphic, not differential): parsing a
// stream of SSE frames and then re-parsing the SAME logical stream after a
// semantics-preserving transformation must accumulate the identical llm.Response.
//
// Two transforms are applied, each compared against the untransformed baseline:
//
//   - Re-chunking: deliver the exact same bytes one at a time. Read boundaries
//     must not influence the parsed result.
//   - Interstitial comments: insert an SSE comment frame (":...") after every
//     event boundary. Comment lines and empty events carry no data, so the
//     decoded result must be unchanged.
//
// A divergence here is a real decoder bug (stateful accumulation that leaks
// across read boundaries or mis-handles benign framing), not a test artifact.
func FuzzOpenAIResponsesMetamorphic(f *testing.F) {
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

	seeds := []string{
		textStream,
		toolStream,
		reasoningStream,
		"data: not-json\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
		`data: {"type":"response.completed","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := accumulateResponsesSSE(a, raw, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateResponsesSSE(a, raw, true)
		if !sameAccumulatedResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		// Insert a comment-only event after every SSE event boundary. SSE comment
		// lines and data-less events are ignored, so the result must not change.
		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateResponsesSSE(a, commented, false)
		if !sameAccumulatedResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
