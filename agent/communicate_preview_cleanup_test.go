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

func TestProcessInputRetryDiscardsFailedPreviewAndCommitsFinalOnce(t *testing.T) {
	var attempts atomic.Int32
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		if attempts.Add(1) == 1 {
			call := communicateCall("failed-call", "failed preview")
			sendCommunicatePreview(st, call)
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("openai", "retry", nil)})
			return
		}
		call := communicateCall("final-call", "final answer")
		sendCommunicatePreview(st, call)
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	policy := llm.RetryPolicy{MaxRetries: 1}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-test")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := startPreviewEventRecorder(sess)
	out, callErr := sess.ProcessInput(context.Background(), "run", nil)
	evs := recorder.stop(sess)
	if callErr != nil {
		t.Fatalf("ProcessInput: %v", callErr)
	}
	if out != "final answer" {
		t.Fatalf("ProcessInput output=%q want=%q", out, "final answer")
	}

	lifecycle := inspectPreviewLifecycle(evs)
	if lifecycle.maxActive != 1 {
		t.Fatalf("maximum active previews=%d want=1; events=%v", lifecycle.maxActive, evs)
	}
	if !maps.Equal(lifecycle.resets, map[string]int{"failed-call": 1, "final-call": 1}) {
		t.Fatalf("preview reset counts=%v; events=%v", lifecycle.resets, evs)
	}
	if len(lifecycle.active) != 0 {
		t.Fatalf("active previews after completion=%v; events=%v", lifecycle.active, evs)
	}
	var completed []events.CommunicateData
	for _, ev := range evs {
		if ev.Kind == events.EventCommunicate {
			completed = append(completed, ev.Data.(events.CommunicateData))
		}
	}
	if len(completed) != 1 || completed[0].CallID != "final-call" || completed[0].Message != "final answer" {
		t.Fatalf("final communicate events=%v want one matching final-call; events=%v", completed, evs)
	}
}

func TestCallModelRetryResetsReusedPreviewCallIDGeneration(t *testing.T) {
	var attempts atomic.Int32
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		attempt := attempts.Add(1)
		call := communicateCall("reused-call", "first generation")
		if attempt == 2 {
			call = communicateCall("reused-call", "second generation")
		}
		sendCommunicatePreview(st, call)
		if attempt == 1 {
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("openai", "retry", nil)})
			return
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-test"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := startPreviewEventRecorder(sess)
	resp, callErr := sess.callModel(context.Background(), llm.RetryPolicy{MaxRetries: 1}, NewOpenAIProfile("gpt-test"), llm.Request{
		Provider: "openai",
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("run")},
	}, &groupRecord{})
	evs := recorder.stop(sess)
	if callErr != nil {
		t.Fatalf("callModel: %v", callErr)
	}
	if !slices.Equal(resp.CommunicatePreviewCallIDs, []string{"reused-call"}) {
		t.Fatalf("active preview IDs=%v want=[reused-call]", resp.CommunicatePreviewCallIDs)
	}
	lifecycle := inspectPreviewLifecycle(evs)
	if len(lifecycle.startsWhileActive) != 0 || !maps.Equal(lifecycle.active, map[string]struct{}{"reused-call": {}}) {
		t.Fatalf("overlapping starts=%v active=%v; events=%v", lifecycle.startsWhileActive, lifecycle.active, evs)
	}
	if !maps.Equal(lifecycle.resets, map[string]int{"reused-call": 1}) {
		t.Fatalf("preview reset counts=%v; events=%v", lifecycle.resets, evs)
	}
}

func TestContinuationRecoveryDiscardsFailedCommunicatePreview(t *testing.T) {
	var attempts atomic.Int32
	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{"code": "previous_response_not_found", "message": "Previous response not found"},
	}, nil)
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		callID := "recovery-call"
		message := "recovered"
		if attempts.Add(1) == 1 {
			callID = "expired-call"
			message = "expired preview"
		}
		call := communicateCall(callID, message)
		sendCommunicatePreview(st, call)
		if call.ID == "expired-call" {
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: continuationErr})
			return
		}
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}})
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(client, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{LLMRetryPolicy: &policy})
	if err != nil {
		t.Fatal(err)
	}
	recorder := startPreviewEventRecorder(sess)
	resp, _, _, callErr := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), phase8DeltaRequest(), phase8FullHistory(), "", 1)
	evs := recorder.stop(sess)
	if callErr != nil {
		t.Fatalf("callModelWithFallback: %v", callErr)
	}
	if !slices.Equal(resp.CommunicatePreviewCallIDs, []string{"recovery-call"}) {
		t.Fatalf("active preview IDs=%v want=[recovery-call]", resp.CommunicatePreviewCallIDs)
	}
	lifecycle := inspectPreviewLifecycle(evs)
	if lifecycle.maxActive != 1 || !maps.Equal(lifecycle.active, map[string]struct{}{"recovery-call": {}}) {
		t.Fatalf("preview lifecycle max=%d active=%v; events=%v", lifecycle.maxActive, lifecycle.active, evs)
	}
	if !maps.Equal(lifecycle.resets, map[string]int{"expired-call": 1}) {
		t.Fatalf("preview reset counts=%v; events=%v", lifecycle.resets, evs)
	}
}

func TestCallModelPanicAfterConsumeResetsTransferredPreview(t *testing.T) {
	old := sessionCallModelAfterConsumeHook
	sessionCallModelAfterConsumeHook = func() { panic("after consume") }
	t.Cleanup(func() { sessionCallModelAfterConsumeHook = old })
	client := llm.NewClient()
	client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
		call := communicateCall("panic-call", "preview")
		sendCommunicatePreview(st, call)
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

func TestCallModelWithFallbackRetainsOnlyActivePreviewIDs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fallbackID string
		want       []string
	}{
		{name: "fallback without preview"},
		{name: "distinct fallback preview", fallbackID: "fallback-call", want: []string{"fallback-call"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streams atomic.Int32
			client := llm.NewClient()
			client.Register(&streamingAdapter{name: "openai", streamScript: func(st *llm.ChanStream) {
				if streams.Add(1) == 1 {
					call := communicateCall("primary-call", "primary")
					sendCommunicatePreview(st, call)
					st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.ErrorFromHTTPStatus("openai", 403, "fallback", nil, nil)})
					return
				}
				if tc.fallbackID != "" {
					call := communicateCall(tc.fallbackID, "fallback")
					sendCommunicatePreview(st, call)
				}
				finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
			}})
			sess, err := NewSession(client, NewOpenAIProfile("gpt-primary"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{ModelFallbacks: []string{"gpt-fallback"}})
			if err != nil {
				t.Fatal(err)
			}
			recorder := startPreviewEventRecorder(sess)
			resp, _, _, callErr := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("gpt-primary"), llm.Request{Provider: "openai", Model: "gpt-primary", Messages: []llm.Message{llm.User("run")}}, nil, "", 0)
			evs := recorder.stop(sess)
			if callErr != nil {
				t.Fatalf("fallback call: %v", callErr)
			}
			if !slices.Equal(resp.CommunicatePreviewCallIDs, tc.want) {
				t.Fatalf("preview IDs=%v want=%v", resp.CommunicatePreviewCallIDs, tc.want)
			}
			lifecycle := inspectPreviewLifecycle(evs)
			if lifecycle.maxActive != 1 {
				t.Fatalf("maximum active previews=%d want=1; events=%v", lifecycle.maxActive, evs)
			}
			if !slices.Equal(sortedPreviewCallIDs(lifecycle.active), tc.want) || !maps.Equal(lifecycle.resets, map[string]int{"primary-call": 1}) {
				t.Fatalf("preview lifecycle active=%v resets=%v; events=%v", lifecycle.active, lifecycle.resets, evs)
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
			sendCommunicatePreview(st, call)
			st.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.ErrorFromHTTPStatus("openai", 403, "fallback", nil, nil)})
			return
		}
		call := communicateCall("fallback-call", "fallback")
		sendCommunicatePreview(st, call)
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
		_, _, _, _ = sess.callModelWithFallback(context.Background(), NewOpenAIProfile("gpt-primary"), llm.Request{Provider: "openai", Model: "gpt-primary", Messages: []llm.Message{llm.User("run")}}, nil, "", 0)
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
			sendCommunicatePreview(st, call)
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

type previewEventRecorder struct {
	collect func() []events.SessionEvent
}

func startPreviewEventRecorder(sess *Session) *previewEventRecorder {
	return &previewEventRecorder{collect: drainEvents(sess)}
}

func (r *previewEventRecorder) stop(sess *Session) []events.SessionEvent {
	sess.Close()
	return r.collect()
}

func sendCommunicatePreview(st *llm.ChanStream, call llm.ToolCallData) {
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name}})
	st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: call.Arguments}})
}

type previewLifecycle struct {
	active            map[string]struct{}
	resets            map[string]int
	startsWhileActive map[string]int
	maxActive         int
}

func inspectPreviewLifecycle(evs []events.SessionEvent) previewLifecycle {
	result := previewLifecycle{
		active:            map[string]struct{}{},
		resets:            map[string]int{},
		startsWhileActive: map[string]int{},
	}
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventCommunicatePreviewStart:
			callID := ev.Data.(events.CommunicatePreviewStartData).CallID
			if _, active := result.active[callID]; active {
				result.startsWhileActive[callID]++
			}
			result.active[callID] = struct{}{}
			result.maxActive = max(result.maxActive, len(result.active))
		case events.EventCommunicatePreviewReset:
			callID := ev.Data.(events.CommunicatePreviewResetData).CallID
			result.resets[callID]++
			delete(result.active, callID)
		case events.EventCommunicate:
			delete(result.active, ev.Data.(events.CommunicateData).CallID)
		}
	}
	return result
}
