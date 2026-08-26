package agent

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

func TestCallModelUnionsPreviewIDsAcrossRetryAttempts(t *testing.T) {
	tests := []struct {
		name   string
		second string
		want   []string
	}{
		{name: "success without preview", want: []string{"call-a"}},
		{name: "success with distinct preview", second: "call-b", want: []string{"call-a", "call-b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			client := llm.NewClient()
			client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
				if attempts.Add(1) == 1 {
					call := communicateCall("call-a", "first")
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("openai", "retry", nil)})
					return
				}
				if tc.second != "" {
					call := communicateCall(tc.second, "second")
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
				}
				finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
			}})
			sess, err := NewSession(client, NewOpenAIProfile("gpt-test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
			if err != nil {
				t.Fatal(err)
			}
			policy := llm.RetryPolicy{MaxRetries: 1}
			resp, err := sess.callModel(context.Background(), policy, NewOpenAIProfile("gpt-test"), llm.Request{Provider: "openai", Model: "gpt-test", Messages: []llm.Message{llm.User("run")}}, &groupRecord{})
			if err != nil {
				t.Fatalf("callModel: %v", err)
			}
			if !slices.Equal(resp.CommunicatePreviewCallIDs, tc.want) {
				t.Fatalf("preview IDs=%v want=%v", resp.CommunicatePreviewCallIDs, tc.want)
			}
		})
	}
}

func TestCallModelPanicAfterConsumeResetsTransferredPreview(t *testing.T) {
	old := sessionCallModelAfterConsumeHook
	sessionCallModelAfterConsumeHook = func() { panic("after consume") }
	t.Cleanup(func() { sessionCallModelAfterConsumeHook = old })
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		call := communicateCall("panic-call", "preview")
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var got []events.EventKind
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			got = append(got, ev.Kind)
		}
	}()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _ = sess.callModel(context.Background(), llm.RetryPolicy{}, NewOpenAIProfile("gpt-test"), llm.Request{Provider: "openai", Model: "gpt-test", Messages: []llm.Message{llm.User("run")}}, &groupRecord{})
	}()
	sess.Close()
	<-done
	if !panicked {
		t.Fatal("callModel did not panic")
	}
	reset := 0
	for _, kind := range got {
		if kind == events.EventCommunicatePreviewReset {
			reset++
		}
	}
	if reset != 1 {
		t.Fatalf("panic cleanup reset count=%d events=%v", reset, got)
	}
}

func TestCallModelWithFallbackUnionsPrimaryAndFallbackPreviewIDs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fallbackID string
		want       []string
	}{
		{name: "fallback without preview", want: []string{"primary-call"}},
		{name: "distinct fallback preview", fallbackID: "fallback-call", want: []string{"fallback-call", "primary-call"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streams atomic.Int32
			client := llm.NewClient()
			client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
				if streams.Add(1) == 1 {
					call := communicateCall("primary-call", "primary")
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.ErrorFromHTTPStatus("openai", 403, "fallback", nil, nil)})
					return
				}
				if tc.fallbackID != "" {
					call := communicateCall(tc.fallbackID, "fallback")
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
					st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
				}
				finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
			}})
			sess, err := NewSession(client, NewOpenAIProfile("gpt-primary"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{ModelFallbacks: []string{"gpt-fallback"}})
			if err != nil {
				t.Fatal(err)
			}
			resp, _, _, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("gpt-primary"), llm.Request{Provider: "openai", Model: "gpt-primary", Messages: []llm.Message{llm.User("run")}}, "", 0)
			if err != nil {
				t.Fatalf("fallback call: %v", err)
			}
			if !slices.Equal(resp.CommunicatePreviewCallIDs, tc.want) {
				t.Fatalf("preview IDs=%v want=%v", resp.CommunicatePreviewCallIDs, tc.want)
			}
		})
	}
}

func TestCallModelWithFallbackPanicResetsTransferredPrimaryExactlyOnce(t *testing.T) {
	var streams, hooks atomic.Int32
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		if streams.Add(1) == 1 {
			call := communicateCall("primary-call", "primary")
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.ErrorFromHTTPStatus("openai", 403, "fallback", nil, nil)})
			return
		}
		call := communicateCall("fallback-call", "fallback")
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-primary"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{ModelFallbacks: []string{"gpt-fallback"}})
	if err != nil {
		t.Fatal(err)
	}
	old := sessionCallModelAfterConsumeHook
	sessionCallModelAfterConsumeHook = func() {
		if hooks.Add(1) == 2 {
			panic("fallback transfer")
		}
	}
	t.Cleanup(func() { sessionCallModelAfterConsumeHook = old })
	var got []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			got = append(got, ev)
		}
	}()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		_, _, _, _ = sess.callModelWithFallback(context.Background(), NewOpenAIProfile("gpt-primary"), llm.Request{Provider: "openai", Model: "gpt-primary", Messages: []llm.Message{llm.User("run")}}, "", 0)
	}()
	sess.Close()
	<-done
	if !panicked {
		t.Fatal("fallback call did not panic")
	}
	resets := map[string]int{}
	for _, ev := range got {
		if ev.Kind == events.EventCommunicatePreviewReset {
			resets[ev.Data.(events.CommunicatePreviewResetData).CallID]++
		}
	}
	if !maps.Equal(resets, map[string]int{"primary-call": 1, "fallback-call": 1}) {
		t.Fatalf("exact reset counts=%v events=%v", resets, got)
	}
}

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
