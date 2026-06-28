package google

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
// forces the Gemini SSE decoder through a different read-segmentation than a
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

// accumulateGeminiSSE drives the streamGenerateContent SSE decoder over sse and
// folds the emitted stream events back into a final llm.Response, exactly as the
// live completion path does. When chunkOneByte is set, the bytes are delivered
// one at a time to vary read boundaries without changing the logical stream.
func accumulateGeminiSSE(a *Adapter, sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go a.decodeStream(ctx, cancel, resp, s, llm.Request{Model: "fuzz-model"}, nil, "http://fuzz.local/endpoint")

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

// normalizeGeminiResponse clears the framing-dependent raw-body fields (the
// semantics-preserving transforms legitimately alter the verbatim SSE bytes) and
// blanks every synthetic tool-call ID. Gemini does not carry a tool-call ID on
// the wire, so the decoder mints a random ULID per call ("call_" + ulid.Make());
// that value is random by construction and cannot be compared across two parses
// of the same bytes. Everything else (text, tool name/args, reasoning, finish,
// usage, Raw) must match.
func normalizeGeminiResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
	cp.RawRequestBody = ""
	cp.RawResponseBody = ""
	parts := make([]llm.ContentPart, len(cp.Message.Content))
	for i, p := range cp.Message.Content {
		if p.ToolCall != nil {
			tc := *p.ToolCall
			tc.ID = ""
			p.ToolCall = &tc
		}
		parts[i] = p
	}
	cp.Message.Content = parts
	return &cp
}

func sameGeminiResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeGeminiResponse(a), normalizeGeminiResponse(b))
}

// FuzzGeminiStreamMetamorphic asserts the same metamorphic property as the
// OpenAI Responses decoder, for the Gemini streamGenerateContent (alt=sse)
// decoder: re-chunking the bytes and inserting SSE comment frames are
// semantics-preserving, so the accumulated llm.Response must be unchanged. The
// random per-call synthetic tool-call ID is normalized away (see
// normalizeGeminiResponse), since it is random by construction, not a function
// of the bytes.
func FuzzGeminiStreamMetamorphic(f *testing.F) {
	textStream := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}` + "\n\n" +
		`data: {"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}` + "\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}]}` + "\n\n"

	toolStream := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"shell","args":{"cmd":"ls"}}}]}}]}` + "\n\n" +
		`data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"

	thinkingStream := `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"hmm"}]}}]}` + "\n\n" +
		`data: {"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}` + "\n\n"

	seeds := []string{
		textStream,
		toolStream,
		thinkingStream,
		"data: not-json\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
		`data: {"candidates":[{"finishReason":"MAX_TOKENS"}]}` + "\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := accumulateGeminiSSE(a, raw, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateGeminiSSE(a, raw, true)
		if !sameGeminiResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateGeminiSSE(a, commented, false)
		if !sameGeminiResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
