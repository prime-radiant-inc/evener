package llm

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// partialStreamEvents scripts one complete text response of n distinct deltas
// and returns its events with the assembled text.
func partialStreamEvents(n int) ([]StreamEvent, string) {
	evs := []StreamEvent{
		{Type: StreamEventStreamStart},
		{Type: StreamEventTextStart, TextID: "t"},
	}
	var text strings.Builder
	for i := range n {
		delta := "chunk" + strconv.Itoa(i)
		text.WriteString(delta)
		evs = append(evs, StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: delta})
	}
	evs = append(evs, StreamEvent{Type: StreamEventTextEnd, TextID: "t"})
	fin := Response{
		Provider: "openai",
		Model:    "m",
		Message:  Assistant(text.String()),
		Finish:   FinishReason{Reason: "stop"},
	}
	evs = append(evs, finishEvent(fin))
	return evs, text.String()
}

// TestStreamGenerate_PartialResponseTracksDeliveredText pins what a caller
// observes through PartialResponse while the high-level stream runs: every read
// returns the text delivered so far in arrival order, never shrinks, and the
// read taken after the stream completes carries the whole response. Reads run
// on the consuming goroutine, so -race covers projecting a partial while the
// producer is still accumulating.
func TestStreamGenerate_PartialResponseTracksDeliveredText(t *testing.T) {
	t.Parallel()
	evs, want := partialStreamEvents(64)
	c, _ := newScriptedStreamClient(replayEvents(evs...))
	prompt := "hi"

	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	var last string
	reads := 0
	for range res.Events() {
		partial := res.PartialResponse()
		if partial == nil {
			continue
		}
		reads++
		got := partial.Text()
		if !strings.HasPrefix(want, got) {
			t.Fatalf("partial text %q is not a prefix of the full response %q", got, want)
		}
		if !strings.HasPrefix(got, last) {
			t.Fatalf("partial text went backwards: %q then %q", last, got)
		}
		last = got
	}
	if reads == 0 {
		t.Fatal("no partial response was ever visible while the stream ran")
	}

	if _, err := res.Response(); err != nil {
		t.Fatalf("Response: %v", err)
	}
	final := res.PartialResponse()
	if final == nil {
		t.Fatal("PartialResponse() = nil after the stream completed")
	}
	if got := final.Text(); got != want {
		t.Fatalf("final partial text = %q, want %q", got, want)
	}
	if final.Finish.Reason != FinishReasonStop {
		t.Fatalf("final partial finish reason = %q, want %q", final.Finish.Reason, FinishReasonStop)
	}

	// The returned response is a copy: rewriting it must not reach the result's
	// own state.
	final.ID = "rewritten"
	final.Finish.Reason = "rewritten"
	again := res.PartialResponse()
	if again.ID == "rewritten" || again.Finish.Reason == "rewritten" {
		t.Fatalf("PartialResponse returned aliased state: %+v", again)
	}
}

// TestStreamGenerate_PartialResponseSurvivesMidStreamError pins the partial
// after a provider error: the caller keeps the text delivered before the
// error, and Response() reports that error.
func TestStreamGenerate_PartialResponseSurvivesMidStreamError(t *testing.T) {
	t.Parallel()
	boom := errors.New("provider dropped the stream")
	evs := []StreamEvent{
		{Type: StreamEventStreamStart},
		{Type: StreamEventTextStart, TextID: "t"},
		{Type: StreamEventTextDelta, TextID: "t", Delta: "half"},
		{Type: StreamEventTextDelta, TextID: "t", Delta: "-done"},
		{Type: StreamEventError, Err: boom},
	}
	c, _ := newScriptedStreamClient(replayEvents(evs...))
	prompt := "hi"

	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	for range res.Events() {
	}

	if _, rerr := res.Response(); rerr == nil {
		t.Fatal("Response() = nil error after a mid-stream provider error")
	}
	partial := res.PartialResponse()
	if partial == nil {
		t.Fatal("PartialResponse() = nil after a mid-stream provider error")
	}
	if got := partial.Text(); got != "half-done" {
		t.Fatalf("partial text = %q, want %q", got, "half-done")
	}
	if partial.Finish.Reason != "" || partial.Usage.OutputTokens != 0 {
		t.Fatalf("partial carries finish metadata %+v / %+v, want content only", partial.Finish, partial.Usage)
	}
}

// TestStreamGenerate_PartialResponseSurvivesCancellation pins the partial after
// cancellation: text delivered before the cancel stays readable, and Response()
// reports the abort.
func TestStreamGenerate_PartialResponseSurvivesCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The stream delivers its deltas, then blocks until the caller cancels.
	delivered := make(chan struct{})
	c, _ := newScriptedStreamClient(func(ctx context.Context, _ Request) (Stream, error) {
		_, stCancel := context.WithCancel(ctx)
		st := NewChanStream(stCancel)
		go func() {
			defer st.CloseSend()
			st.Send(StreamEvent{Type: StreamEventStreamStart})
			st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "t"})
			st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "kept"})
			close(delivered)
			<-ctx.Done()
		}()
		return st, nil
	})
	prompt := "hi"

	res, err := StreamGenerate(ctx, GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	<-delivered
	cancel()
	for range res.Events() {
	}

	_, rerr := res.Response()
	if _, ok := errors.AsType[*AbortError](rerr); !ok {
		t.Fatalf("Response error = %T (%v), want *AbortError", rerr, rerr)
	}
	partial := res.PartialResponse()
	if partial == nil {
		t.Fatal("PartialResponse() = nil after cancellation")
	}
	if got := partial.Text(); got != "kept" {
		t.Fatalf("partial text = %q, want %q", got, "kept")
	}
}

// TestStreamGenerate_DoesNotRebuildPartialPerDelta pins the cost the streaming
// path pays per delta for a caller that never reads PartialResponse. The
// accumulator builds its partial response lazily and counts those builds, so the
// count is the cost oracle: append-only accumulation leaves it at zero here
// because the finish event carries the whole response, while publishing a
// rebuilt partial on every text delta (issue #6411) builds one per delta.
func TestStreamGenerate_DoesNotRebuildPartialPerDelta(t *testing.T) {
	t.Parallel()
	const deltas = 100
	evs, want := partialStreamEvents(deltas)
	c, _ := newScriptedStreamClient(replayEvents(evs...))
	prompt := "hi"

	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:   c,
		Model:    "m",
		Provider: "openai",
		Prompt:   &prompt,
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	for range res.Events() {
	}
	if _, err := res.Response(); err != nil {
		t.Fatalf("Response: %v", err)
	}

	res.mu.Lock()
	acc := res.acc
	res.mu.Unlock()
	if acc == nil {
		t.Fatal("stream result kept no accumulator to inspect")
	}
	if acc.buildCount != 0 {
		t.Fatalf("accumulator built the partial response %d times for %d deltas with no reader, want 0", acc.buildCount, deltas)
	}
	if got := res.PartialResponse().Text(); got != want {
		t.Fatalf("partial text = %q, want %q", got, want)
	}
}

// TestStreamGenerate_PartialSurvivesToolStepBoundary pins the partial across a
// tool round: text from a completed step stays readable while the next step has
// not produced text yet, so a step boundary never blanks a partial the caller
// could already see.
func TestStreamGenerate_PartialSurvivesToolStepBoundary(t *testing.T) {
	t.Parallel()
	call := ToolCallData{ID: "c1", Name: "t1", Arguments: []byte(`{"x":1}`), Type: "function"}
	step1 := Response{
		Provider: "openai",
		Model:    "m",
		Message: Message{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentText, Text: "step-one"},
			{Kind: ContentToolCall, ToolCall: &call},
		}},
		Finish: FinishReason{Reason: "tool_calls"},
	}
	step2 := Response{Provider: "openai", Model: "m", Message: Assistant("step-two"), Finish: FinishReason{Reason: "stop"}}

	// The second step's events are held behind this channel until the test has
	// read the partial at the first STEP_FINISH, so the assertion holds on any
	// schedule instead of relying on producer timing.
	releaseStep2 := make(chan struct{})

	c, _ := newScriptedStreamClient(
		replayEvents(
			StreamEvent{Type: StreamEventStreamStart},
			StreamEvent{Type: StreamEventTextStart, TextID: "t"},
			StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "step-one"},
			finishEvent(step1),
		),
		func(ctx context.Context, _ Request) (Stream, error) {
			sctx, cancel := context.WithCancel(ctx)
			st := NewChanStream(cancel)
			go func() {
				defer st.CloseSend()
				// Abandon the hold when the stream is closed, so a failing
				// assertion turns res.Close() into a return instead of a
				// deadlock against a producer parked on releaseStep2.
				select {
				case <-releaseStep2:
				case <-sctx.Done():
					return
				}
				st.Send(StreamEvent{Type: StreamEventStreamStart})
				st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "t"})
				st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "step-two"})
				st.Send(finishEvent(step2))
				cancel()
			}()
			return st, nil
		},
	)
	prompt := "hi"
	rounds := 1

	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Provider:      "openai",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute: func(context.Context, any) (any, error) {
				return "done", nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	// Read the partial at each step boundary: the value must never blank once
	// text has been delivered. The first boundary arrives before the second
	// step streams anything, so this read is deterministic.
	var seen []string
	released := false
	for ev := range res.Events() {
		if ev.Type != StreamEventStepFinish {
			continue
		}
		partial := res.PartialResponse()
		if partial == nil {
			t.Fatalf("PartialResponse() = nil at a step boundary after text was delivered (seen %v)", seen)
		}
		seen = append(seen, partial.Text())
		if partial.Finish.Reason != "" {
			t.Fatalf("partial at a step boundary carries finish reason %q, want none: the old snapshot was taken before the FINISH event was folded in", partial.Finish.Reason)
		}
		// The first STEP_FINISH is the boundary under test: release the second
		// step only after asserting here.
		if !released {
			released = true
			close(releaseStep2)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no step boundary was observed")
	}
	if seen[0] != "step-one" {
		t.Fatalf("partial at the first step boundary = %q, want %q", seen[0], "step-one")
	}

	if _, err := res.Response(); err != nil {
		t.Fatalf("Response: %v", err)
	}
	if got := res.PartialResponse().Text(); got != "step-two" {
		t.Fatalf("final partial text = %q, want %q", got, "step-two")
	}
}

// A step that produces only a tool call must not blank the partial a caller is
// reading: the last attempt that produced text stays visible across it.
func TestPartialResponseKeepsTextAcrossATextLessStep(t *testing.T) {
	t.Parallel()
	call1 := ToolCallData{ID: "c1", Name: "t1", Arguments: []byte(`{"x":1}`), Type: "function"}
	call2 := ToolCallData{ID: "c2", Name: "t1", Arguments: []byte(`{"y":2}`), Type: "function"}
	// Each step before the last carries a tool call, which is what keeps the
	// loop going to the next step.
	first := Response{
		Provider: "openai", Model: "m",
		Message: Message{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentText, Text: "step-one"},
			{Kind: ContentToolCall, ToolCall: &call1},
		}},
		Finish: FinishReason{Reason: "tool_calls"},
	}
	// The middle step produces a tool call and no text at all.
	middle := Response{
		Provider: "openai", Model: "m",
		Message: Message{Role: RoleAssistant, Content: []ContentPart{
			{Kind: ContentToolCall, ToolCall: &call2},
		}},
		Finish: FinishReason{Reason: "tool_calls"},
	}
	last := Response{Provider: "openai", Model: "m", Message: Assistant("step-three"), Finish: FinishReason{Reason: "stop"}}

	// The third step's events are held behind this channel once it has sent its
	// TextStart, so the partial read below lands after the promotion decision
	// for this attempt and before it accumulates any text. Without the hold the
	// read races the producer, which free-runs past the buffered STEP_FINISH
	// into this script.
	releaseStep3 := make(chan struct{})

	c, _ := newScriptedStreamClient(
		replayEvents(
			StreamEvent{Type: StreamEventStreamStart},
			StreamEvent{Type: StreamEventTextStart, TextID: "t"},
			StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "step-one"},
			finishEvent(first),
		),
		replayEvents(
			StreamEvent{Type: StreamEventStreamStart},
			finishEvent(middle),
		),
		func(ctx context.Context, _ Request) (Stream, error) {
			sctx, cancel := context.WithCancel(ctx)
			st := NewChanStream(cancel)
			go func() {
				defer st.CloseSend()
				st.Send(StreamEvent{Type: StreamEventStreamStart})
				st.Send(StreamEvent{Type: StreamEventTextStart, TextID: "t"})
				// Abandon the hold when the stream closes, so a failing
				// assertion turns res.Close() into a return instead of a
				// deadlock against a producer parked here.
				select {
				case <-releaseStep3:
				case <-sctx.Done():
					return
				}
				st.Send(StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "step-three"})
				st.Send(finishEvent(last))
				cancel()
			}()
			return st, nil
		},
	)
	prompt := "hi"
	rounds := 3

	res, err := StreamGenerate(context.Background(), GenerateOptions{
		Client:        c,
		Model:         "m",
		Provider:      "openai",
		Prompt:        &prompt,
		MaxToolRounds: &rounds,
		Tools: []Tool{{
			Definition: ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}},
			Execute: func(context.Context, any) (any, error) {
				return "done", nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer res.Close() //nolint:errcheck

	// textStarts counts the TextStart events forwarded so far. The middle step
	// emits none, so the second one belongs to the third step, and reading the
	// partial there is deterministic: the attempt has been promoted and has not
	// yet accumulated text.
	textStarts := 0
	asserted := false
	stepBoundaries := 0
	for ev := range res.Events() {
		switch ev.Type {
		case StreamEventTextStart:
			textStarts++
			if textStarts != 2 {
				continue
			}
			if asserted {
				continue
			}
			asserted = true
			partial := res.PartialResponse()
			if partial == nil {
				t.Fatalf("PartialResponse() = nil at the text start of the step after a text-less step: the text-less step blanked the visible partial (step boundaries seen: %d)", stepBoundaries)
			}
			if got := partial.Text(); got != "step-one" {
				t.Fatalf("partial text at the text start of the step after a text-less step = %q, want %q", got, "step-one")
			}
			close(releaseStep3)
		case StreamEventStepFinish:
			stepBoundaries++
		}
	}
	if !asserted {
		// Release the parked producer so res.Close() cannot hang behind it.
		close(releaseStep3)
		t.Fatalf("never observed the second TextStart (step boundaries seen: %d): the third step never streamed", stepBoundaries)
	}
	if stepBoundaries < 2 {
		t.Fatalf("observed %d step boundaries, want at least 2", stepBoundaries)
	}

	if _, err := res.Response(); err != nil {
		t.Fatalf("Response: %v", err)
	}
	if got := res.PartialResponse().Text(); got != "step-three" {
		t.Fatalf("final partial text = %q, want %q", got, "step-three")
	}
}
