package chatcompletions

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestStreamMixedReasoningMatchesNonStreaming(t *testing.T) {
	t.Parallel()
	const enc = `{"z":1,"type":"reasoning.encrypted","id":"r1","data":"blob"}`
	const details = `[{"type":"reasoning.text","text":"step one"},` + enc + `]`
	const mixed = `{"reasoning_content":"scalar reasoning","reasoning_details":` + details + `}`
	const plan, i0, ifrag = `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"fore","index":1}]}`, `[{"type":"reasoning.text","text":"step one","index":0}]`, `[{"type":"reasoning.text","text":"step ","index":0}]`
	for _, tc := range []struct{ msg, text, emits, deltas string }{
		{mixed, "step one", "step one", mixed},
		{mixed, "step one", "scalar reasoning|step one", `{"reasoning_content":"scalar reasoning"}` + "\x00" + `{"reasoning_details":` + details + `}`},
		{mixed, "step one", "step one|scalar reasoning", `{"reasoning_details":` + details + `}` + "\x00" + `{"reasoning_content":"scalar reasoning"}`},
		{`{"reasoning_details":` + i0 + `}`, "step one", "step |one", `{"reasoning_details":` + ifrag + `}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"one","index":0}]}`},
		{`{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"forecast","index":1}]}`, "plan\nforecast", "plan\nfore|cast", plan + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"cast","index":1}]}`},
	} {
		resp, err := ExtractRecordedResponse([]byte(`{"choices":[{"message":`+tc.msg+`}]}`), "m")
		if err != nil {
			t.Fatal(err)
		}
		events, terminal := chatStreamEvents(t, `data: {"choices":[{"delta":`+strings.ReplaceAll(tc.deltas, "\x00", "}]}\n\ndata: {\"choices\":[{\"delta\":")+"}]}\n\ndata: [DONE]\n\n", nil)
		if terminal != nil {
			t.Fatalf("stream error: %v", terminal)
		}
		var got *llm.ThinkingData
		var emitted []string
		for _, ev := range events {
			switch ev.Type {
			case llm.StreamEventReasoningDelta:
				emitted = append(emitted, ev.ReasoningDelta)
			case llm.StreamEventFinish:
				got = ev.Response.Message.Content[0].Thinking
			}
		}
		want := resp.Message.Content[0].Thinking
		if strings.Join(emitted, "|") != tc.emits || got == nil || got.Text != tc.text || !reflect.DeepEqual(got, want) {
			t.Fatalf("stream = %+v (deltas %q), want %+v (text %q, deltas %q)", got, emitted, want, tc.text, tc.emits)
		}
	}
}
