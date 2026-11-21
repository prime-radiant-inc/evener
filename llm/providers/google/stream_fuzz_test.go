package google

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

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

func accumulateInteractionsSSE(sse []byte, chunkOneByte bool) (*llm.Response, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var body io.Reader = bytes.NewReader(sse)
	if chunkOneByte {
		body = &byteAtATimeReader{data: sse}
	}
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}
	s := llm.NewChanStream(cancel)
	go decodeInteractionStream(ctx, nil, cancel, resp, s, llm.Request{Model: "fuzz-model"}, "google-fuzz", "fuzz-model", "http://fuzz.local/endpoint", llm.APILogCredentialMaterial{}, nil, false, false, nil)

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

func normalizeInteractionsResponse(r *llm.Response) *llm.Response {
	if r == nil {
		return nil
	}
	cp := *r
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

func sameInteractionsResponse(a *llm.Response, aErr bool, b *llm.Response, bErr bool) bool {
	if aErr != bErr {
		return false
	}
	return reflect.DeepEqual(normalizeInteractionsResponse(a), normalizeInteractionsResponse(b))
}

func FuzzGeminiStreamMetamorphic(f *testing.F) {
	textStream := "event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"int_1","model":"gemini-3.8-flash","status":"in_progress"}}` + "\n\n" +
		"event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hello"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":" world"}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0,"usage":{"total_input_tokens":3,"total_output_tokens":2,"total_tokens":5}}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_1","status":"completed"}}` + "\n\n"

	toolStream := "event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"c1","name":"shell","arguments":{}}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"cmd\":\"ls\"}"}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_2","status":"requires_action"}}` + "\n\n"

	thinkingStream := "event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"hmm"}}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig1"}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: step.start\n" +
		`data: {"event_type":"step.start","index":1,"step":{"type":"model_output"}}` + "\n\n" +
		"event: step.delta\n" +
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"text","text":"answer"}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":1}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_3","status":"completed"}}` + "\n\n"

	seeds := []string{
		textStream,
		toolStream,
		thinkingStream,
		"data: not-json\n\n",
		": just a comment\n\n",
		"\n\n\n",
		"",
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_4","status":"incomplete"}}` + "\n\n",
		`data: {"event_type":"error","error":{"code":"resource_exhausted","message":"m"}}` + "\n\n",
		`data: {"event_type":"error","error":{"code":"gateway_timeout","message":"m"}}` + "\n\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		base, baseErr := accumulateInteractionsSSE(raw, false)

		rechunked, reErr := accumulateInteractionsSSE(raw, true)
		if !sameInteractionsResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n input=%q",
				base, baseErr, rechunked, reErr, raw)
		}

		commented := bytes.ReplaceAll(raw, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateInteractionsSSE(commented, false)
		if !sameInteractionsResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n input=%q",
				base, baseErr, withComments, cErr, raw)
		}
	})
}
