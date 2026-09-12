package chatcompletions

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/iotest"

	"primeradiant.com/evener/llm"
)

// oneByteRoundTripper forces the decoder's reads of the response body to
// happen one byte at a time, regardless of how the underlying connection
// actually delivered them. It reproduces openaicompat's byteAtATimeReader
// re-chunk oracle (stream_fuzz_test.go) over a real HTTP round trip through
// Protocol.Stream, instead of feeding a fabricated *http.Response straight
// to the decoder.
type oneByteRoundTripper struct{ next http.RoundTripper }

func (t oneByteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{iotest.OneByteReader(resp.Body), resp.Body}
	return resp, nil
}

// chatCompletionsStreamHarness serves a mutable SSE payload from one fixed
// httptest server, so a fuzz iteration's base/re-chunked/commented variants
// all hit the same endpoint URL. A fresh server per variant would give each
// one a different ephemeral port, and the decoded Response's
// Raw["endpoint_url"] would then differ for a reason that has nothing to do
// with the property under test, failing the metamorphic comparison for no
// real bug.
type chatCompletionsStreamHarness struct {
	srv     *httptest.Server
	payload atomic.Pointer[[]byte]
}

func newChatCompletionsStreamHarness() *chatCompletionsStreamHarness {
	h := &chatCompletionsStreamHarness{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if p := h.payload.Load(); p != nil {
			_, _ = w.Write(*p)
		}
	}))
	return h
}

func (h *chatCompletionsStreamHarness) close() { h.srv.Close() }

// run serves sse from the harness's fixed endpoint and drives it through the
// real (&Protocol{...}).Stream, folding the emitted stream events back into
// a final llm.Response exactly as the live completion path does. When
// oneByte is set, the client reads the response body one byte at a time
// (oneByteRoundTripper).
func (h *chatCompletionsStreamHarness) run(sse []byte, oneByte bool) (*llm.Response, bool) {
	h.payload.Store(&sse)
	client := h.srv.Client()
	if oneByte {
		client = &http.Client{Transport: oneByteRoundTripper{next: h.srv.Client().Transport}}
	}
	p := &Protocol{Client: client}
	s, err := p.Stream(context.Background(), userReq("hi"), liveRes(h.srv, nil))
	if err != nil {
		return nil, true
	}

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

// normalizeChatCompletionsResponse returns a shallow comparison copy. Every
// response field must match across equivalent SSE framings.
func normalizeChatCompletionsResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func sameChatCompletionsResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeChatCompletionsResponse(a), normalizeChatCompletionsResponse(b))
}

// FuzzChatCompletionsStreamMetamorphic asserts the same metamorphic property
// as openaicompat's FuzzOpenAICompatStreamMetamorphic, now driving the real
// (&Protocol{Client: srv.Client()}).Stream through an httptest server instead
// of calling the decoder directly.
//
// Two transforms are applied, each compared against the untransformed
// baseline:
//
//   - Re-chunking: the client reads the exact same bytes one at a time. Read
//     boundaries must not influence the parsed result.
//   - Interstitial comments: insert an SSE comment frame (":...") after every
//     event boundary. Comment lines and empty events carry no data, so the
//     decoded result must be unchanged.
func FuzzChatCompletionsStreamMetamorphic(f *testing.F) {
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

	f.Fuzz(func(t *testing.T, raw []byte) {
		h := newChatCompletionsStreamHarness()
		defer h.close()

		base, baseErr := h.run(raw, false) // Oracle (floor): never panics.

		rechunked, reErr := h.run(raw, true)
		if !sameChatCompletionsResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := h.run(commented, false)
		if !sameChatCompletionsResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
