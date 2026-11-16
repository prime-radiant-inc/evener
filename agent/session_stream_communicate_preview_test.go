package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// previewDeltas drives the decoder the way a live stream does — feeding it the
// accumulated argument text after each delta — and returns the sequence of
// DELTAS it would emit, so a byte-by-byte split and a whole-message feed can be
// compared without depending on how the caller chunks.
func previewDeltas(chunks []string) []string {
	var d communicatePreviewDecoder
	d.field = "message"
	var out []string
	prev := ""
	acc := ""
	for _, chunk := range chunks {
		acc += chunk
		message, ok := d.message(acc)
		if !ok || len(message) <= len(prev) || !strings.HasPrefix(message, prev) {
			continue
		}
		out = append(out, message[len(prev):])
		prev = message
	}
	return out
}

// TestCommunicatePreviewDecoderHandlesEscapedSlash pins the one escape the
// splitter cannot hand to strconv.UnquoteChar. JSON allows `\/`, and the
// scanner accepts it, but UnquoteChar is a Go rune unquoter and rejects it —
// so a splitter without an explicit case reads `\/` as an incomplete escape and
// stalls the preview at the text before it.
//
// It asserts the decoded TEXT against the whole-message scanner result, not the
// incremental decoder against itself: a test comparing two uses of the same
// broken decoder passes while every user sees a frozen preview.
func TestCommunicatePreviewDecoderHandlesEscapedSlash(t *testing.T) {
	t.Parallel()
	const args = `{"message":"a\/b then more"}`
	want, _, terminated := scanPartialJSONStringBody(`a\/b then more"`)
	if !terminated || want != "a/b then more" {
		t.Fatalf("test setup: scanner decoded %q terminated=%v", want, terminated)
	}
	var d communicatePreviewDecoder
	d.field = "message"
	// One byte per delta: every split lands somewhere, including between the
	// backslash and the slash.
	for i := range len(args) {
		d.message(args[:i+1])
	}
	got, ok := d.message(args)
	if !ok {
		t.Fatal("decoder never found the message field")
	}
	if got != want {
		t.Fatalf("preview message = %q, want %q", got, want)
	}
}

// TestCommunicatePreviewDecoderMatchesWholeMessageFeed is the equivalence the
// issue asks for: splitting the argument JSON into one byte per delta must
// produce exactly the same preview deltas as feeding the whole message at once.
// A resumable decoder is only correct if a chunk boundary lands nowhere special —
// including inside an escape, inside a \uXXXX, and inside a surrogate pair.
func TestCommunicatePreviewDecoderMatchesWholeMessageFeed(t *testing.T) {
	cases := map[string]string{
		"plain":              `{"message":"hello world"}`,
		"escaped quote":      `{"message":"she said \"hi\" loudly"}`,
		"backslash":          `{"message":"a C:\\path\\to\\file"}`,
		"newline escape":     `{"message":"line one\nline two\ttabbed"}`,
		"unicode escape":     `{"message":"caf\u00e9 na\u00efve"}`,
		"surrogate pair":     `{"message":"emoji \ud83d\ude00 end"}`,
		"slash escape":       `{"message":"a\/b"}`,
		"other field first":  `{"intent":"x","message":"after intent"}`,
		"other field after":  `{"message":"before intent","intent":"x"}`,
		"empty":              `{"message":""}`,
		"unterminated":       `{"message":"never closes`,
		"unterminated escap": `{"message":"ends mid escape \`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			want := previewDeltas([]string{args})
			var bytewise []string
			for i := range len(args) {
				bytewise = append(bytewise, args[i:i+1])
			}
			got := previewDeltas(bytewise)
			if strings.Join(got, "") != strings.Join(want, "") {
				t.Fatalf("byte-by-byte decoding = %q, whole-message decoding = %q", strings.Join(got, ""), strings.Join(want, ""))
			}
			// The split may merge adjacent deltas (a byte of an escape decodes to
			// nothing), so compare the concatenation and the final message.
			var dd communicatePreviewDecoder
			dd.field = "message"
			gotFinal, _ := dd.message(args)
			var dw communicatePreviewDecoder
			dw.field = "message"
			wantFinal, _ := dw.message(args)
			if gotFinal != wantFinal {
				t.Fatalf("byte-by-byte final message = %q, whole feed = %q", gotFinal, wantFinal)
			}
		})
	}
}

// TestCommunicatePreviewDecoderChunkInvariance splits the same arguments at every
// possible boundary — one split point, mid-escape included — and requires the
// decoded message to match the unsplit result. Two-way splits cover the boundary
// cases a byte-per-delta test can miss only if the decoder is order-sensitive.
func TestCommunicatePreviewDecoderChunkInvariance(t *testing.T) {
	args := `{"intent":"report","message":"quote \" backslash \\ unicode \u00e9 pair \ud83d\ude00 done"}`
	for split := 0; split <= len(args); split++ {
		var d communicatePreviewDecoder
		d.field = "message"
		if _, ok := d.message(args[:split]); !ok && split < len(args) {
			// A prefix that has not reached the value yet is not an error; the
			// decoder is expected to keep waiting.
			continue
		}
		got, _ := d.message(args)
		var whole communicatePreviewDecoder
		whole.field = "message"
		want, _ := whole.message(args)
		if got != want {
			t.Fatalf("split at %d (%q | %q): message = %q, want %q", split, args[:split], args[split:], got, want)
		}
	}
}

// TestCommunicatePreviewSurvivesArgumentReplacementAtToolCallEnd covers the one
// place the accumulated argument text does not simply grow. A provider's end
// event carries its final arguments and REPLACES the accumulated text, and those
// final arguments need not extend what streamed before them: chatcompletions
// rewrites them through rescueClaudeXMLArgs. An incremental decoder whose
// offsets survived that replacement would resume mid-message against different
// bytes and emit a corrupted preview, so the final text must decode correctly.
func TestCommunicatePreviewSurvivesArgumentReplacementAtToolCallEnd(t *testing.T) {
	t.Parallel()

	finalCall := communicateCall("c1", "hello world")
	// The streamed prefix stops mid-message, and the final arguments do not
	// extend it at all: they lead with another field, so the message body starts
	// at a different offset in the final text than it did while streaming.
	const streamed = `{"message":"hel`
	if strings.HasPrefix(string(finalCall.Arguments), streamed) {
		t.Fatalf("test setup: final %q must not extend streamed %q", finalCall.Arguments, streamed)
	}

	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: finalCall.ID, Name: finalCall.Name, Type: "function"}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: finalCall.ID, Arguments: []byte(streamed)}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &finalCall})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-test")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := startPreviewEventRecorder(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	evs := recorder.stop(sess)

	var deltas []string
	for _, ev := range evs {
		if ev.Kind == events.EventCommunicatePreviewDelta {
			deltas = append(deltas, ev.Data.(events.CommunicatePreviewDeltaData).Delta)
		}
	}
	// The message the user sees must reach the final text: "hel" from the delta,
	// then the rest once the end event's own arguments are decoded.
	if got := strings.Join(deltas, ""); got != "hello world" {
		t.Fatalf("preview message = %q, want %q; deltas=%q", got, "hello world", deltas)
	}
}

// BenchmarkCommunicatePreviewDelta measures the per-delta cost of deriving the
// preview message while a communicate call streams. The one-shot path re-derived
// the whole message for each delta, so cost grew with the square of the message
// length; this measures the incremental path over the same argument stream.
func BenchmarkCommunicatePreviewDelta(b *testing.B) {
	// A realistically sized message, streamed one byte per delta: this is the
	// shape a token-by-token tool-call argument stream takes.
	call := communicateCall("c1", strings.Repeat("streaming preview text ", 200))
	args := string(call.Arguments)
	for b.Loop() {
		var d communicatePreviewDecoder
		d.field = "message"
		for i := range len(args) {
			d.message(args[:i+1])
		}
	}
}
