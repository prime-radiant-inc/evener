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
	// A single unindexed thought split across frames: one item, no separator.
	const unindexedMsg = `{"reasoning_details":[{"type":"reasoning.text","text":"step one"}]}`
	const unindexedSplit = `{"reasoning_details":[{"type":"reasoning.text","text":"step "}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"one"}]}`
	// Scalar and structured frames carrying the same text: the live pane must
	// show it once, and the settled text still comes from the structured item.
	const mirrorMsg = `{"reasoning_content":"hello world","reasoning_details":[{"type":"reasoning.text","text":"hello world"}]}`
	const mirrorScalar = `{"reasoning_content":"hello world"}`
	const mirrorStructured = `{"reasoning_details":[{"type":"reasoning.text","text":"hello "}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"world"}]}`
	for _, tc := range []struct{ name, msg, text, emits, deltas string }{
		{"single-chunk-mixed", mixed, "step one", "step one", mixed},
		{"scalar-then-structured", mixed, "step one", "scalar reasoning|step one", `{"reasoning_content":"scalar reasoning"}` + "\x00" + `{"reasoning_details":` + details + `}`},
		{"structured-then-scalar", mixed, "step one", "step one|scalar reasoning", `{"reasoning_details":` + details + `}` + "\x00" + `{"reasoning_content":"scalar reasoning"}`},
		{"indexed-fragments", `{"reasoning_details":` + i0 + `}`, "step one", "step |one", `{"reasoning_details":` + ifrag + `}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"one","index":0}]}`},
		{"distinct-items-one-chunk", `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"forecast","index":1}]}`, "plan\nforecast", "plan\nfore|cast", plan + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"cast","index":1}]}`},
		// A distinct index that arrives in its own frame still starts a new
		// preview item, so the separator is not lost at the frame boundary.
		{"distinct-index-separator-across-frames", `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"forecast","index":1}]}`, "plan\nforecast", "plan|\nforecast", `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"forecast","index":1}]}`},
		{"unindexed-thought-split", unindexedMsg, "step one", "step |one", unindexedSplit},
		// A newline starts each preview item, including when an earlier
		// index resumes after another index appeared.
		{"out-of-order-index", `{"reasoning_details":[{"type":"reasoning.text","text":"ac","index":0},{"type":"reasoning.text","text":"b","index":1}]}`, "ac\nb", "a|\nb|\nc", `{"reasoning_details":[{"type":"reasoning.text","text":"a","index":0}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"b","index":1}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"c","index":0}]}`},
		{"mirror-scalar-first", mirrorMsg, "hello world", "hello world", mirrorScalar + "\x00" + mirrorStructured},
		{"mirror-structured-first", mirrorMsg, "hello world", "hello |world", mirrorStructured + "\x00" + mirrorScalar},
		// A multi-item structured preview shows its items joined by a newline;
		// a scalar frame mirroring that joined text must not repeat it.
		{"mirror-multi-item-scalar", `{"reasoning_content":"plan\nfore","reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"fore","index":1}]}`, "plan\nfore", "plan\nfore", `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0},{"type":"reasoning.text","text":"fore","index":1}]}` + "\x00" + `{"reasoning_content":"plan\nfore"}`},
		// Differing text is not a mirror: the live pane keeps both, and the
		// settled text comes from the structured item.
		{"near-miss-sends-unseen-suffix", `{"reasoning_content":"hello world","reasoning_details":[{"type":"reasoning.text","text":"hello world!"}]}`, "hello world!", "hello world|!", `{"reasoning_content":"hello world"}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"hello world!"}]}`},
		// The mixed frame's scalar bytes are displayed through the structured
		// candidate, so a later scalar repeat of the same item must not
		// repeat them on the live pane.
		{"mixed-frame-mirror-then-scalar-repeat", `{"reasoning_content":"ab","reasoning_details":[{"type":"reasoning.text","text":"a","index":0},{"type":"reasoning.text","text":"b","index":0}]}`, "ab", "a|b", `{"reasoning_content":"a","reasoning_details":[{"type":"reasoning.text","text":"a","index":0}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"b","index":0}]}` + "\x00" + `{"reasoning_content":"b"}`},
		// Fragments of one explicit index concatenate whether they share a
		// frame or not: the frame boundary is not an item boundary.
		{"adjacent-same-index-one-chunk", `{"reasoning_details":[{"type":"reasoning.text","text":"recheck","index":0}]}`, "recheck", "recheck", `{"reasoning_details":[{"type":"reasoning.text","text":"re","index":0},{"type":"reasoning.text","text":"check","index":0}]}`},
		// A mixed frame's scalar fragment mirrors the pane and extends it:
		// the unseen scalar suffix must join the same preview.
		{"mixed-mirror-extension-joins-preview", `{"reasoning_content":"abc","reasoning_details":[{"type":"reasoning.text","text":"abc","index":0}]}`, "abc", "ab|c", `{"reasoning_content":"ab","reasoning_details":[{"type":"reasoning.text","text":"a","index":0}]}` + "\x00" + `{"reasoning_content":"c"}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"bc","index":0}]}`},
		// Unindexed pieces of one array keep their newline in the implicit
		// item even when an indexed piece arrives between them.
		{"unindexed-pieces-around-indexed", `{"reasoning_details":[{"type":"reasoning.text","text":"step"},{"type":"reasoning.text","text":"other","index":1},{"type":"reasoning.text","text":"one"}]}`, "step\none\nother", "step\nother\none", `{"reasoning_details":[{"type":"reasoning.text","text":"step"},{"type":"reasoning.text","text":"other","index":1},{"type":"reasoning.text","text":"one"}]}`},
		// An indexed item's identity is registered on first appearance even
		// when that appearance carries no text, so settled order follows
		// appearance rather than first non-empty text.
		{"empty-first-index-orders-by-appearance", `{"reasoning_details":[{"type":"reasoning.text","text":"","index":0},{"type":"reasoning.text","text":"forecast","index":1},{"type":"reasoning.text","text":"plan","index":0}]}`, "plan\nforecast", "forecast|\nplan", `{"reasoning_details":[{"type":"reasoning.text","text":"","index":0}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"forecast","index":1}]}` + "\x00" + `{"reasoning_details":[{"type":"reasoning.text","text":"plan","index":0}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
		})
	}
}

// TestStreamMirrorSendsUnseenSuffix pins that a structured delta which extends
// what the scalar preview already showed sends only the unseen suffix, and that
// the suppressed scalar text still reaches the settled thinking part's replay
// fields: only the live preview converges, the replay keeps both.
func TestStreamMirrorSendsUnseenSuffix(t *testing.T) {
	t.Parallel()
	sse := `data: {"choices":[{"delta":{"reasoning_content":"a"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"reasoning_content":"b","reasoning_details":[{"type":"reasoning.text","text":"ab"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	events, terminal := chatStreamEvents(t, sse, nil)
	if terminal != nil {
		t.Fatalf("stream error: %v", terminal)
	}
	var emitted []string
	for _, ev := range events {
		if ev.Type == llm.StreamEventReasoningDelta {
			emitted = append(emitted, ev.ReasoningDelta)
			continue
		}
		if ev.Type != llm.StreamEventFinish {
			continue
		}
		var thinking *llm.ThinkingData
		for _, p := range ev.Response.Message.Content {
			if p.Kind == llm.ContentThinking {
				thinking = p.Thinking
			}
		}
		if thinking == nil {
			t.Fatalf("content = %+v, want a thinking part", ev.Response.Message.Content)
		}
		if strings.Join(emitted, "|") != "a|b" {
			t.Fatalf("live deltas = %q, want the unseen suffix only", emitted)
		}
		if thinking.Text != "ab" {
			t.Fatalf("thinking text = %q, want the structured item's text", thinking.Text)
		}
		if thinking.ScalarReasoningField != "reasoning_content" || thinking.ScalarReasoningText != "ab" {
			t.Fatalf("scalar replay = (%q, %q), want the suppressed scalar stream replayed",
				thinking.ScalarReasoningField, thinking.ScalarReasoningText)
		}
		return
	}
	t.Fatal("no finish event")
}
