package openaicompat

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
// forces the Chat Completions SSE decoder through a different read-segmentation
// than a bytes.Reader, which is what makes the re-chunk metamorphic oracle below
// mean something: an accumulator whose result depends on read boundaries is buggy.
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

// accumulateChatSSE drives the openai-compatible Chat Completions SSE decoder
// over sse and folds the emitted stream events back into a final llm.Response,
// exactly as the live completion path does. When chunkOneByte is set, the bytes
// are delivered one at a time to vary read boundaries without changing the
// logical stream.
func accumulateChatSSE(a *Adapter, sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go a.decodeStream(ctx, resp, s, llm.Request{Model: "fuzz-model"}, nil, nil)

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

// normalizeChatResponse clears the framing-dependent raw-body fields. Those carry
// the verbatim SSE bytes when raw-body logging is enabled, which the
// semantics-preserving transforms legitimately alter; everything else must match.
func normalizeChatResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	cp.RawRequestBody = ""
	cp.RawResponseBody = ""
	return &cp
}

func sameChatResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeChatResponse(a), normalizeChatResponse(b))
}

// FuzzOpenAICompatStreamMetamorphic asserts the same metamorphic property as the
// OpenAI Responses decoder, for the openai-compatible Chat Completions SSE
// decoder. This decoder backs openaicompat directly and (via wrapping) glm, kimi,
// openrouter, and ollama.
//
// Two transforms are applied, each compared against the untransformed baseline:
//
//   - Re-chunking: deliver the exact same bytes one at a time. Read boundaries
//     must not influence the parsed result.
//   - Interstitial comments: insert an SSE comment frame (":...") after every
//     event boundary. Comment lines and empty events carry no data, so the
//     decoded result must be unchanged.
func FuzzOpenAICompatStreamMetamorphic(f *testing.F) {
	textStream := `data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}` + "\n\n" +
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"c1","model":"m","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	toolStream := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	reasoningStream := `data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	seeds := []string{
		textStream,
		toolStream,
		reasoningStream,
		"data: not-json\n\n",
		"data: [DONE]\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := accumulateChatSSE(a, raw, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateChatSSE(a, raw, true)
		if !sameChatResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateChatSSE(a, commented, false)
		if !sameChatResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
