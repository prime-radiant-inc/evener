package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestCommunicatePreviewResetsWhenCallerAbortsAfterStream(t *testing.T) {
	dir := t.TempDir()
	call := communicateCall("call-1", "preview")
	client := llm.NewClient()
	client.Register(&streamingAdapter{
		name: "openai",
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
			finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var got []events.EventKind
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			got = append(got, ev.Kind)
		}
	}()
	ctx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{
		"abort_after_model": errors.New("abort after model"),
	})
	// TRIPWIRE: scripted in-process stream; this only bounds a lifecycle deadlock.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run", nil); err == nil {
		t.Fatal("ProcessInput succeeded after injected abort_after_model")
	}
	sess.Close()
	<-done

	start, delta, reset, completed := 0, 0, 0, 0
	for _, kind := range got {
		switch kind {
		case events.EventCommunicatePreviewStart:
			start++
		case events.EventCommunicatePreviewDelta:
			delta++
		case events.EventCommunicatePreviewReset:
			reset++
		case events.EventCommunicate:
			completed++
		}
	}
	if start != 1 || delta != 1 || reset != 1 || completed != 0 {
		t.Fatalf("preview lifecycle start=%d delta=%d reset=%d communicate=%d events=%v", start, delta, reset, completed, got)
	}
}
