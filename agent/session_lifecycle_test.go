package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSession_FollowUp_ProcessesAfterCompletion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("first") },
			func(req llm.Request) llm.Response { return finalResponse("second") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.FollowUp("do second")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do first", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "first\nsecond" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()
}

func TestSession_LoopDetection_EmitsEventAndInjectsSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func() llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`)}},
				},
			},
		}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	cfg := SessionConfig{LoopDetectionWindow: 3}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "loop", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Spec: loop detection warning is recorded as a SteeringTurn in history.
	sess.mu.Lock()
	turns := append([]schema.Turn{}, sess.history...)
	sess.mu.Unlock()
	foundSteering := false
	for _, tr := range turns {
		if tr.Kind == schema.TurnSteering && tr.Message.Role == llm.RoleUser && strings.Contains(tr.Message.Text(), "stuck") {
			foundSteering = true
		}
	}
	if !foundSteering {
		t.Fatalf("expected loop detection steering turn in history; got %+v", turns)
	}
	sess.Close()

	// Verify loop detection event was emitted.
	loopEv := false
	steerEv := false
	for ev := range sess.Events() {
		if ev.Kind == events.EventLoopDetection {
			loopEv = true
		}
		if d, ok := ev.Data.(events.SteeringInjectedData); ok {
			if strings.Contains(d.Text, "stuck") {
				steerEv = true
			}
		}
	}
	if !loopEv {
		t.Fatalf("expected LOOP_DETECTION event")
	}
	if !steerEv {
		t.Fatalf("expected STEERING_INJECTED event for loop detection")
	}

	// Verify the steering message made it into a subsequent request.
	reqs := f.Requests()
	found := false
	for _, req := range reqs {
		for _, m := range req.Messages {
			if m.Role == llm.RoleUser && strings.Contains(m.Text(), "stuck") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected loop detection steering message in request history")
	}
}

func TestSessionState_Transitions(t *testing.T) {
	t.Parallel()
	// Verify state type and constants exist
	if SessionIdle != SessionState("idle") {
		t.Fatal("SessionIdle wrong")
	}
	if SessionProcessing != SessionState("active") {
		t.Fatal("SessionProcessing wrong")
	}
	if SessionClosed != SessionState("closed") {
		t.Fatal("SessionClosed wrong")
	}
}

func TestSession_SessionEnd_EmittedExactlyOnce(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return finalResponse("done")
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-doneCh

	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, ev := range *eventsPtr {
		if ev.Kind == events.EventSessionEnd {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 SESSION_END event, got %d", count)
	}
}

func TestSession_Close_CancelsInFlightLLMCall(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	c := llm.NewClient()
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "hello", nil)
		done <- err
	}()

	<-blocked    // Wait until the LLM call is in-flight.
	sess.Close() // Should cancel the LLM call.

	select {
	case <-done:
		// ProcessInput returned -- Close() successfully cancelled it.
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after Close() -- in-flight LLM call not cancelled")
	}
}

func TestSession_CloseCannotBeReopenedByLateTurnCompletion(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	release := make(chan struct{})
	c := llm.NewClient()
	c.Register(&closeRaceAdapter{name: "openai", blocked: blocked, release: release})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	processDone := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "hello", nil)
		processDone <- err
	}()

	<-blocked
	sess.Close()
	close(release)

	var processErr error
	select {
	case processErr = <-processDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after releasing adapter")
	}
	if !errors.Is(processErr, context.Canceled) {
		t.Fatalf("ProcessInput error=%v, want context.Canceled", processErr)
	}
	if got := sess.State(); got != SessionClosed {
		t.Fatalf("late ProcessInput completion reopened session: got %s want %s", got, SessionClosed)
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(history) != 1 || history[0].Kind != schema.TurnUserInput {
		t.Fatalf("late model response was processed into history: %+v", history)
	}
}

func TestSession_ExecToolRefusesClosedSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ran := false
	sess.RegisterTool("late_tool", "late tool", map[string]any{}, func(context.Context, any) (any, error) {
		ran = true
		return "ran", nil
	})
	sess.Close()

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "late_call",
		Name:      "late_tool",
		Arguments: json.RawMessage(`{}`),
		Type:      "function",
	})
	if ran {
		t.Fatal("tool executed after session close")
	}
	if !res.IsError || !strings.Contains(res.FullOutput, "session is closing") {
		t.Fatalf("closed execTool result=%+v, want skipped error", res)
	}
}

func TestSession_ExecToolEmitsEndWhenCloseBeginsAfterStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := &blockingCleanupEnv{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir),
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), env, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	sess.RegisterTool("slow_tool", "slow tool", map[string]any{}, func(context.Context, any) (any, error) {
		close(toolStarted)
		<-releaseTool
		return "ran", nil
	})

	eventsDone := make(chan []events.SessionEvent, 1)
	go func() {
		var evs []events.SessionEvent
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
		eventsDone <- evs
	}()

	resultDone := make(chan tool.ExecResult, 1)
	go func() {
		resultDone <- sess.execTool(context.Background(), llm.ToolCallData{
			ID:        "slow_call",
			Name:      "slow_tool",
			Arguments: json.RawMessage(`{}`),
			Type:      "function",
		})
	}()

	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	closeDone := make(chan struct{})
	go func() {
		sess.Close()
		close(closeDone)
	}()
	select {
	case <-env.started:
	case <-time.After(time.Second):
		t.Fatal("Close did not reach cleanup")
	}
	close(releaseTool)

	var res tool.ExecResult
	select {
	case res = <-resultDone:
	case <-time.After(time.Second):
		t.Fatal("execTool did not return after close began")
	}
	if !res.IsError || !strings.Contains(res.FullOutput, "session is closing") {
		t.Fatalf("execTool result=%+v, want skipped closing error", res)
	}

	close(env.release)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish")
	}
	var evs []events.SessionEvent
	select {
	case evs = <-eventsDone:
	case <-time.After(time.Second):
		t.Fatal("events channel did not close")
	}

	starts := 0
	ends := 0
	var end events.ToolCallEndData
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventToolCallStart:
			if data, ok := ev.Data.(events.ToolCallStartData); ok && data.CallID == "slow_call" {
				starts++
			}
		case events.EventToolCallEnd:
			if data, ok := ev.Data.(events.ToolCallEndData); ok && data.CallID == "slow_call" {
				ends++
				end = data
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("tool events starts=%d ends=%d events=%+v", starts, ends, evs)
	}
	if !strings.Contains(end.Error, "session is closing") {
		t.Fatalf("terminal tool end error=%q, want closing error", end.Error)
	}
}

func TestSession_AppendCanceledToolResultsPreservesCompletedStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	calls := []llm.ToolCallData{
		{ID: "ok_call", Name: "ok_tool"},
		{ID: "canceled_call", Name: "slow_tool"},
	}
	results := []tool.ExecResult{{
		ToolName:   "ok_tool",
		CallID:     "ok_call",
		Output:     "ok",
		FullOutput: "ok",
		IsError:    false,
	}}
	sess.appendCanceledToolResults(calls, results, context.Canceled)

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(history) != 1 || history[0].Kind != schema.TurnToolResults {
		t.Fatalf("history=%+v, want one tool-results turn", history)
	}
	parts := history[0].Message.Content
	if len(parts) != 2 {
		t.Fatalf("parts=%+v, want two tool results", parts)
	}
	if parts[0].ToolResult == nil || parts[0].ToolResult.IsError {
		t.Fatalf("completed result was not preserved as success: %+v", parts[0].ToolResult)
	}
	if parts[1].ToolResult == nil || !parts[1].ToolResult.IsError {
		t.Fatalf("synthetic canceled result was not marked error: %+v", parts[1].ToolResult)
	}
}

func TestSession_AppendToolResultsSynthesizesCanceledResultsOnCancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	calls := []llm.ToolCallData{
		{ID: "ok_call", Name: "ok_tool"},
		{ID: "canceled_call", Name: "slow_tool"},
	}
	results := []tool.ExecResult{{
		ToolName:   "ok_tool",
		CallID:     "ok_call",
		Output:     "ok",
		FullOutput: "ok",
		IsError:    false,
	}}
	parts := []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "ok_call",
			Name:       "ok_tool",
			Content:    "ok",
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sess.appendToolResults(ctx, calls, results, parts); !errors.Is(err, context.Canceled) {
		t.Fatalf("appendToolResults err=%v, want context.Canceled", err)
	}

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if len(history) != 1 || history[0].Kind != schema.TurnToolResults {
		t.Fatalf("history=%+v, want one fallback tool-results turn", history)
	}
	gotParts := history[0].Message.Content
	if len(gotParts) != 2 {
		t.Fatalf("parts=%+v, want two tool results", gotParts)
	}
	if gotParts[0].ToolResult == nil || gotParts[0].ToolResult.IsError {
		t.Fatalf("completed result was not preserved as success: %+v", gotParts[0].ToolResult)
	}
	if gotParts[1].ToolResult == nil || !gotParts[1].ToolResult.IsError {
		t.Fatalf("synthetic canceled result was not marked error: %+v", gotParts[1].ToolResult)
	}
}

func TestSession_CloseCancelsInFlightWithoutInterruptMarker(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	c := llm.NewClient()
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	eventsDone := make(chan []events.SessionEvent, 1)
	go func() {
		var evs []events.SessionEvent
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
		eventsDone <- evs
	}()

	processDone := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(context.Background(), "hello", nil)
		processDone <- err
	}()

	<-blocked
	sess.Close()

	select {
	case <-processDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after Close()")
	}

	var evs []events.SessionEvent
	select {
	case evs = <-eventsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("events channel did not close")
	}

	for _, ev := range evs {
		if ev.Kind == events.EventSteeringInjected {
			t.Fatalf("Close emitted interrupt steering marker: %+v", ev)
		}
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		data, ok := ev.Data.(events.SessionEndData)
		if !ok {
			continue
		}
		if data.Reason == "interrupted" || data.Interrupted {
			t.Fatalf("Close emitted interrupted SESSION_END: %+v", data)
		}
	}
}

func TestSession_GracefulShutdown_CorrectOrdering(t *testing.T) {
	t.Parallel()
	// Use a blocking adapter so we can call Close() while the LLM call is
	// in-flight. This ensures Close() is the one that emits SESSION_END
	// (not ProcessInput), exercising the abort/shutdown path.
	blocked := make(chan struct{})
	c := llm.NewClient()
	c.Register(&blockingAdapter{name: "openai", blocked: blocked})
	dir := t.TempDir()

	inner := execenv.NewLocalExecutionEnvironment(dir)
	trackEnv := &cleanupTrackingEnv{ExecutionEnvironment: inner}

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), trackEnv, SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// The event consumer records when SESSION_END is received and when the
	// channel closes. Because Close() is synchronous and we share the log
	// with Cleanup(), we can verify the ordering of operations in Close().
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for ev := range sess.Events() {
			if ev.Kind == events.EventSessionEnd {
				trackEnv.Append("session_end_received")
			}
		}
		trackEnv.Append("channel_closed")
	}()

	// Start ProcessInput in the background; it will block on the LLM call.
	processDone := make(chan struct{})
	go func() {
		defer close(processDone)
		_, _ = sess.ProcessInput(context.Background(), "hello", nil)
	}()

	// Wait until the LLM call is in-flight, then abort via Close().
	<-blocked
	sess.Close()
	<-processDone
	<-doneCh

	log := trackEnv.Log()

	// Find positions of key operations.
	indexOf := func(op string) int {
		for i, v := range log {
			if v == op {
				return i
			}
		}
		return -1
	}

	cleanupStart := indexOf("cleanup_start")
	cleanupEnd := indexOf("cleanup_end")
	sessionEnd := indexOf("session_end_received")
	channelClosed := indexOf("channel_closed")

	if cleanupStart == -1 || cleanupEnd == -1 {
		t.Fatalf("env.Cleanup() was never called; log: %v", log)
	}
	if sessionEnd == -1 {
		t.Fatalf("SESSION_END event was never emitted; log: %v", log)
	}
	if channelClosed == -1 {
		t.Fatalf("events channel was never closed; log: %v", log)
	}

	// Spec Appendix B ordering: Cleanup (kill processes) must complete
	// before SESSION_END is emitted.
	if cleanupEnd >= sessionEnd {
		t.Fatalf("env.Cleanup() must complete before SESSION_END is emitted (spec Appendix B);\n"+
			"cleanup_end at %d, session_end at %d; log: %v", cleanupEnd, sessionEnd, log)
	}

	// SESSION_END must be emitted before the events channel is closed.
	if sessionEnd >= channelClosed {
		t.Fatalf("SESSION_END must be emitted before events channel closes;\n"+
			"session_end at %d, channel_closed at %d; log: %v", sessionEnd, channelClosed, log)
	}

	// State should be CLOSED after Close() returns.
	if sess.State() != SessionClosed {
		t.Fatalf("state after Close(): got %s, want CLOSED", sess.State())
	}
}

func TestSession_LoopDetection_WarningWording(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func() llm.Response {
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`)}},
				},
			},
		}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return toolMsg() },
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LoopDetectionWindow: 3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "loop", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	// Verify the LOOP_DETECTION event message matches the spec wording.
	var loopMsg string
	for _, ev := range evs {
		if d, ok := ev.Data.(events.LoopDetectionData); ok {
			loopMsg = d.Message
			break
		}
	}
	if loopMsg == "" {
		t.Fatal("no LOOP_DETECTION event found")
	}
	if !strings.Contains(loopMsg, "stuck in a loop") {
		t.Fatalf("loop message should contain 'stuck in a loop', got: %q", loopMsg)
	}
	if !strings.Contains(loopMsg, "reasoning effort has been increased") {
		t.Fatalf("first loop detection should mention reasoning escalation, got: %q", loopMsg)
	}
}

func TestLooksLikeQuestion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text     string
		expected bool
	}{
		{"What file should I edit?", true},
		{"Done.", false},
		{"Please provide the API key:", true},
		{"Which approach do you prefer?\n", true},
		{"I need more information.", false},
		{"", false},
		{"Result: success", false},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			got := looksLikeQuestion(tc.text)
			if got != tc.expected {
				t.Errorf("looksLikeQuestion(%q) = %v, want %v", tc.text, got, tc.expected)
			}
		})
	}
}

func TestSession_Meta_PopulatesOriginalPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "write a haiku about goroutines", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	meta := sess.Meta()
	if meta.OriginalPrompt != "write a haiku about goroutines" {
		t.Fatalf("OriginalPrompt: got %q, want %q",
			meta.OriginalPrompt, "write a haiku about goroutines")
	}
}

func TestSession_Meta_OriginalPrompt_EmptyForFreshSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	meta := sess.Meta()
	if meta.OriginalPrompt != "" {
		t.Fatalf("OriginalPrompt: got %q, want empty", meta.OriginalPrompt)
	}
}

func TestSession_ProcessInput_WithImage_BuildsMultiPartUserMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	imgs := []ImageAttachment{
		{MediaType: "image/png", Data: imgBytes, Name: "test.png"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "caption", imgs); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Check the transcript: the user-input turn should carry both a text part
	// and an image part.
	var userTurn *schema.Turn
	for i := range sess.history {
		if sess.history[i].Kind == schema.TurnUserInput {
			userTurn = &sess.history[i]
			break
		}
	}
	if userTurn == nil {
		t.Fatal("no TurnUserInput in history")
	}

	parts := userTurn.Message.Content
	if len(parts) != 2 {
		t.Fatalf("user message parts: got %d, want 2 (text + image)", len(parts))
	}

	var sawText, sawImage bool
	for _, p := range parts {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "caption" {
				t.Errorf("text part: got %q, want %q", p.Text, "caption")
			}
			sawText = true
		case llm.ContentImage:
			if p.Image == nil {
				t.Fatal("image part has nil Image")
			}
			if p.Image.MediaType != "image/png" {
				t.Errorf("image media_type: got %q, want image/png", p.Image.MediaType)
			}
			if !bytes.Equal(p.Image.Data, imgBytes) {
				t.Errorf("image bytes mismatch")
			}
			sawImage = true
		}
	}
	if !sawText || !sawImage {
		t.Fatalf("expected both text and image parts; sawText=%v sawImage=%v", sawText, sawImage)
	}
}

func TestSession_ProcessInput_ImageOnly_OmitsEmptyTextPart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgs := []ImageAttachment{
		{MediaType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}, Name: "p.jpg"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "", imgs); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	var userTurn *schema.Turn
	for i := range sess.history {
		if sess.history[i].Kind == schema.TurnUserInput {
			userTurn = &sess.history[i]
			break
		}
	}
	if userTurn == nil {
		t.Fatal("no TurnUserInput in history")
	}
	parts := userTurn.Message.Content
	if len(parts) != 1 {
		t.Fatalf("parts: got %d, want 1 (image only)", len(parts))
	}
	if parts[0].Kind != llm.ContentImage {
		t.Fatalf("part kind: got %q, want %q", parts[0].Kind, llm.ContentImage)
	}
}

// TestSession_Enqueue_DrainsAfterTurnCompletes verifies that a message
// enqueued during an in-flight turn is processed as a fresh user turn once
// the active turn completes. Kata 111a.
func TestSession_Enqueue_DrainsAfterTurnCompletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	// Two LLM responses: one for the first user input, one for the queued
	// message. Each finalResponse satisfies the communicate result tool, so
	// ProcessInput completes one user turn per LLM call.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("first reply") },
			func(req llm.Request) llm.Response { return finalResponse("second reply") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Enqueue before ProcessInput so the outer loop drains the queue once
	// the first user turn finishes naturally.
	if err := sess.Enqueue(context.Background(), "queued message"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if depth := sess.QueueDepth(); depth != 1 {
		t.Fatalf("QueueDepth after Enqueue: got %d, want 1", depth)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "first user input", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "first reply") || !strings.Contains(out, "second reply") {
		t.Fatalf("ProcessInput output should contain both replies; got %q", out)
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after drain: got %d, want 0", depth)
	}

	// Two user-input turns must appear in history: the original and the
	// queued one, in that order.
	sess.mu.Lock()
	turns := append([]schema.Turn{}, sess.history...)
	sess.mu.Unlock()
	var userTexts []string
	for _, tr := range turns {
		if tr.Kind == schema.TurnUserInput {
			userTexts = append(userTexts, tr.Message.Text())
		}
	}
	if len(userTexts) != 2 {
		t.Fatalf("user turns: got %d (%v), want 2", len(userTexts), userTexts)
	}
	if !strings.Contains(userTexts[0], "first user input") {
		t.Fatalf("first user turn: got %q", userTexts[0])
	}
	if !strings.Contains(userTexts[1], "queued message") {
		t.Fatalf("second user turn: got %q", userTexts[1])
	}
}

func TestSession_QueueMutationWaitsForQueuePublicationSlot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.queueEventsMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- sess.Enqueue(context.Background(), "queued while publisher busy")
	}()

	select {
	case err := <-done:
		t.Fatalf("Enqueue completed while queue publication slot was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth while publication slot held = %d, want 0", depth)
	}

	sess.queueEventsMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not complete after queue publication slot was released")
	}
	if depth := sess.QueueDepth(); depth != 1 {
		t.Fatalf("QueueDepth after Enqueue = %d, want 1", depth)
	}
}

// TestSession_DrainAsSteer_JoinsWithDoubleNewline verifies that
// DrainAsSteer pops every queued message and combines them into a single
// STEERING message joined by "\n\n". Kata 0bq1.
func TestSession_DrainAsSteer_JoinsWithDoubleNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "charlie"); err != nil {
		t.Fatalf("Enqueue charlie: %v", err)
	}
	if depth := sess.QueueDepth(); depth != 3 {
		t.Fatalf("QueueDepth: got %d, want 3", depth)
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	if err := sess.DrainAsSteer(context.Background()); err != nil {
		t.Fatalf("DrainAsSteer: %v", err)
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after drain: got %d, want 0", depth)
	}

	// The combined message must have landed on the steeringQueue with the
	// expected join.
	sess.mu.Lock()
	steering := make([]string, 0, len(sess.steeringQueue))
	for _, m := range sess.steeringQueue {
		steering = append(steering, m.Text)
	}
	sess.mu.Unlock()
	want := "alpha\n\nbravo\n\ncharlie"
	if len(steering) != 1 || steering[0] != want {
		t.Fatalf("steeringQueue: got %#v, want [%q]", steering, want)
	}
}

func TestSession_DrainAsSteer_RejectsIdleWithoutMutatingQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	err = sess.DrainAsSteerWithInput(context.Background(), "bravo", nil)
	if err == nil || !strings.Contains(err.Error(), "no active turn") {
		t.Fatalf("DrainAsSteerWithInput idle err=%v, want no active turn", err)
	}
	if depth := sess.QueueDepth(); depth != 1 {
		t.Fatalf("QueueDepth after rejected drain: got %d, want 1", depth)
	}
	sess.mu.Lock()
	steeringDepth := len(sess.steeringQueue)
	queuedText := ""
	if len(sess.inputQueue) == 1 {
		queuedText = sess.inputQueue[0].Text
	}
	sess.mu.Unlock()
	if steeringDepth != 0 {
		t.Fatalf("steeringQueue after rejected drain: got %d, want 0", steeringDepth)
	}
	if queuedText != "alpha" {
		t.Fatalf("queue after rejected drain: got %q, want original alpha only", queuedText)
	}
}

// TestSession_Enqueue_OnEmptyText_ReturnsError verifies that the queue
// rejects blank input rather than silently dropping it.
func TestSession_Enqueue_OnEmptyText_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if err := sess.Enqueue(context.Background(), "   "); err == nil {
		t.Fatalf("Enqueue blank text: want error, got nil")
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after blank: got %d, want 0", depth)
	}
}

// TestSession_DrainAsSteer_Empty_ReturnsError verifies the no-op case.
func TestSession_DrainAsSteer_Empty_ReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	if err := sess.DrainAsSteer(context.Background()); err == nil {
		t.Fatalf("DrainAsSteer empty queue: want error, got nil")
	}
}

// TestSession_QueuePreview_ReturnsCopy verifies preview is a defensive copy.
func TestSession_QueuePreview_ReturnsCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	_ = sess.Enqueue(context.Background(), "first")
	_ = sess.Enqueue(context.Background(), "second")
	preview := sess.QueuePreview()
	if len(preview) != 2 || preview[0] != "first" || preview[1] != "second" {
		t.Fatalf("preview: %#v", preview)
	}
	preview[0] = "mutated"
	// Underlying queue should not reflect the local mutation.
	fresh := sess.QueuePreview()
	if fresh[0] != "first" {
		t.Fatalf("preview leaked underlying slice: %#v", fresh)
	}
}

// TestSession_QueuePreview_FirstLineTruncated_FIFO verifies that
// QueuePreview returns each queued entry collapsed to its first line in
// FIFO order (kata r80p). Multi-line input must surface only the first
// line so wire consumers can render the preview directly without
// re-parsing.
func TestSession_QueuePreview_FirstLineTruncated_FIFO(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Single-line, multi-line with LF, and CRLF terminator. The CRLF case
	// catches an easy bug where we forget to trim the trailing CR after
	// splitting on the first newline.
	if err := sess.Enqueue(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enqueue alpha: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "bravo line 1\nbravo line 2\nbravo line 3"); err != nil {
		t.Fatalf("Enqueue bravo: %v", err)
	}
	if err := sess.Enqueue(context.Background(), "charlie\r\nwith CR"); err != nil {
		t.Fatalf("Enqueue charlie: %v", err)
	}
	preview := sess.QueuePreview()
	want := []string{"alpha", "bravo line 1", "charlie"}
	if len(preview) != len(want) {
		t.Fatalf("preview len=%d, want %d (%#v)", len(preview), len(want), preview)
	}
	for i, expected := range want {
		if preview[i] != expected {
			t.Fatalf("preview[%d]=%q, want %q", i, preview[i], expected)
		}
	}
}
