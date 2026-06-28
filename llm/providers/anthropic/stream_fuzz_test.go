package anthropic

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
// forces the Anthropic SSE decoder through a different read-segmentation than a
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

// accumulateAnthropicSSE drives the Anthropic event-stream decoder over sse and
// folds the emitted stream events back into a final llm.Response, exactly as the
// live completion path does. It returns the accumulated response (nil if the
// stream never completed) and whether any error event was emitted. When
// chunkOneByte is set, the bytes are delivered one at a time to vary read
// boundaries without changing the logical stream.
func accumulateAnthropicSSE(a *Adapter, sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go a.decodeStream(ctx, cancel, resp, s, llm.Request{Model: "fuzz-model"}, nil)

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

// normalizeAnthropicResponse copies r with the framing-dependent raw-body fields
// cleared. Those carry the verbatim SSE bytes when raw-body logging is enabled,
// which the semantics-preserving transforms legitimately alter; everything else
// (text, tool calls, reasoning, finish, usage, the parsed Raw object) must match.
func normalizeAnthropicResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	cp.RawRequestBody = ""
	cp.RawResponseBody = ""
	return &cp
}

func sameAnthropicResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeAnthropicResponse(a), normalizeAnthropicResponse(b))
}

// FuzzAnthropicStreamMetamorphic asserts a metamorphic property of the Anthropic
// event-stream decoder (the same oracle that guards the OpenAI Responses
// decoder): parsing a stream of SSE frames and then re-parsing the SAME logical
// stream after a semantics-preserving transformation must accumulate the
// identical llm.Response. This decoder backs anthropic, kimi-anthropic, minimax,
// and openrouter-anthropic (they all wrap *anthropic.Adapter).
//
// Two transforms are applied, each compared against the untransformed baseline:
//
//   - Re-chunking: deliver the exact same bytes one at a time. Read boundaries
//     must not influence the parsed result.
//   - Interstitial comments: insert an SSE comment frame (":...") after every
//     event boundary. Comment lines and empty events carry no data, so the
//     decoded result must be unchanged.
func FuzzAnthropicStreamMetamorphic(f *testing.F) {
	textStream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	toolStream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude-test"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	thinkingStream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_3","model":"claude-test"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	seeds := []string{
		textStream,
		toolStream,
		thinkingStream,
		"event: message_stop\ndata: not-json\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
		"event: ping\ndata: {}\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := accumulateAnthropicSSE(a, raw, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateAnthropicSSE(a, raw, true)
		if !sameAnthropicResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		// Insert a comment-only event after every SSE event boundary. SSE comment
		// lines and data-less events are ignored, so the result must not change.
		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateAnthropicSSE(a, commented, false)
		if !sameAnthropicResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
