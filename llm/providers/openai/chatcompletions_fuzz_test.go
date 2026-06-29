package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

// accumulateChatCompletionsSSE drives the OpenAI Chat Completions SSE decoder
// over sse and folds the emitted stream events back into a final llm.Response,
// exactly as the live completion path does. When chunkOneByte is set, the bytes
// are delivered one at a time to vary read boundaries without changing the
// logical stream. It reuses byteAtATimeReader / normalizeResponse /
// sameAccumulatedResponse from responses_fuzz_test.go (same package).
func accumulateChatCompletionsSSE(a *Adapter, sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go a.decodeChatCompletionsStream(ctx, cancel, resp, s, llm.Request{Model: "fuzz-model"}, nil)

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

// FuzzOpenAIChatCompletionsMetamorphic asserts the same metamorphic property as
// the OpenAI Responses decoder, for the OpenAI Chat Completions SSE decoder
// (the Responses-API fallback path): re-chunking the bytes and inserting SSE
// comment frames are semantics-preserving, so the accumulated llm.Response must
// be unchanged.
func FuzzOpenAIChatCompletionsMetamorphic(f *testing.F) {
	textStream := `data: {"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}` + "\n\n" +
		`data: {"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"c1","model":"gpt-test","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"

	toolStream := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"cmd\":"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	seeds := []string{
		textStream,
		toolStream,
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
		base, baseErr := accumulateChatCompletionsSSE(a, raw, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateChatCompletionsSSE(a, raw, true)
		if !sameAccumulatedResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateChatCompletionsSSE(a, commented, false)
		if !sameAccumulatedResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
