package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/internal/llm"
)

func TestSession_MaxToolRoundsPerInput_StopsLoop(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func(id string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "glob",
			Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
			},
		}
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg("1") },
			func(req llm.Request) llm.Response { return toolMsg("2") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "loop")
	if err == nil || !strings.Contains(err.Error(), "max tool rounds") {
		t.Fatalf("expected max tool rounds error, got %v", err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_MaxToolRoundsPerInput_EmitsTurnLimitEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func(id string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "glob",
			Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
			},
		}
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg("1") },
			func(req llm.Request) llm.Response { return toolMsg("2") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "loop")
	if err == nil || !strings.Contains(err.Error(), "max tool rounds") {
		t.Fatalf("expected max tool rounds error, got %v", err)
	}
	sess.Close()

	turnLimit := false
	for ev := range sess.Events() {
		if ev.Kind == EventTurnLimit {
			turnLimit = true
			if v, ok := ev.Data["max_tool_rounds_per_input"].(int); !ok || v != 2 {
				t.Fatalf("expected max_tool_rounds_per_input=2 in event data, got %v", ev.Data)
			}
		}
	}
	if !turnLimit {
		t.Fatalf("expected TURN_LIMIT event when round limit reached")
	}
}

func TestSession_LifecycleEvents_BracketSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.Close()

	var kinds []EventKind
	for ev := range sess.Events() {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) < 2 {
		t.Fatalf("expected at least 2 events, got %v", kinds)
	}
	if kinds[0] != EventSessionStart {
		t.Fatalf("first event: got %q want %q (kinds=%v)", kinds[0], EventSessionStart, kinds)
	}
	if kinds[len(kinds)-1] != EventSessionEnd {
		t.Fatalf("last event: got %q want %q (kinds=%v)", kinds[len(kinds)-1], EventSessionEnd, kinds)
	}
}

func TestSession_EventSystem_NaturalCompletion_EmitsUserAndAssistantTextEventsInOrder(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("hello")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "hi"); err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	var kinds []EventKind
	for ev := range sess.Events() {
		kinds = append(kinds, ev.Kind)
	}

	// Assert ordered subsequence.
	want := []EventKind{
		EventSessionStart,
		EventUserInput,
		EventAssistantTextStart,
		EventAssistantTextDelta,
		EventAssistantTextEnd,
		EventSessionEnd,
	}
	at := 0
	for _, k := range kinds {
		if at < len(want) && k == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("event order missing; got kinds=%v want subsequence=%v", kinds, want)
	}
}

func TestSession_EventSystem_ToolCall_EmitsStartDeltaEnd(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"file_path":"a.txt","content":"hello"}`),
		Type:      "function",
	}
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "write"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	seenStart := false
	seenDelta := false
	seenEnd := false
	for ev := range sess.Events() {
		switch ev.Kind {
		case EventToolCallStart:
			seenStart = true
			if ev.Data["call_id"] != "c1" || ev.Data["tool_name"] != "write_file" {
				t.Fatalf("TOOL_CALL_START data: %+v", ev.Data)
			}
		case EventToolCallOutputDelta:
			seenDelta = true
			if !seenStart || seenEnd {
				t.Fatalf("TOOL_CALL_OUTPUT_DELTA ordering violated (start=%t end=%t)", seenStart, seenEnd)
			}
		case EventToolCallEnd:
			seenEnd = true
			if !seenStart {
				t.Fatalf("TOOL_CALL_END before TOOL_CALL_START")
			}
		}
	}
	if !seenStart || !seenDelta || !seenEnd {
		t.Fatalf("expected TOOL_CALL_START/DELTA/END, got start=%t delta=%t end=%t", seenStart, seenDelta, seenEnd)
	}
}

func TestSession_MaxTurns_StopsAcrossInputsAndEmitsEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First input should succeed (turn 1 of 1).
	_, err = sess.ProcessInput(ctx, "first input")
	if err != nil {
		t.Fatalf("expected first input to succeed, got %v", err)
	}

	// Second input should hit the turn limit.
	_, err = sess.ProcessInput(ctx, "second input")
	if err == nil || !strings.Contains(err.Error(), "turn limit") {
		t.Fatalf("expected turn limit error, got %v", err)
	}
	sess.Close()

	turnLimit := false
	for ev := range sess.Events() {
		if ev.Kind == EventTurnLimit {
			turnLimit = true
		}
	}
	if !turnLimit {
		t.Fatalf("expected TURN_LIMIT event")
	}
}

func TestSession_MultipleSequentialInputs_Work(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("first")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("second")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "one"); err != nil || strings.TrimSpace(out) != "first" {
		t.Fatalf("first: out=%q err=%v", out, err)
	}
	if out, err := sess.ProcessInput(ctx, "two"); err != nil || strings.TrimSpace(out) != "second" {
		t.Fatalf("second: out=%q err=%v", out, err)
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestSession_Steer_IsInjectedAfterCurrentToolRound(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)
	release := make(chan struct{})

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				found := false
				for _, m := range req.Messages {
					if m.Role == llm.RoleUser && strings.Contains(m.Text(), "steer: do X") {
						found = true
					}
				}
				if !found {
					return llm.Response{Message: llm.Assistant("missing steering")}
				}
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "ok", nil
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.ProcessInput(ctx, "run")
		done <- result{out: out, err: err}
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	sess.Steer("steer: do X")
	close(release)

	r := <-done
	if r.err != nil {
		t.Fatalf("ProcessInput: %v", r.err)
	}
	if strings.TrimSpace(r.out) != "ok" {
		t.Fatalf("out: %q", r.out)
	}

	// Spec: steering messages appear as SteeringTurn in history (converted to user-role messages for the LLM).
	sess.mu.Lock()
	turns := append([]Turn{}, sess.history...)
	sess.mu.Unlock()
	foundSteering := false
	for _, tr := range turns {
		if tr.Kind == TurnSteering && tr.Message.Role == llm.RoleUser && strings.Contains(tr.Message.Text(), "steer: do X") {
			foundSteering = true
		}
	}
		if !foundSteering {
			t.Fatalf("expected steering turn in history; got %+v", turns)
		}
		sess.Close()

		toolEndIdx := -1
		steerIdx := -1
		i := 0
		for ev := range sess.Events() {
			switch ev.Kind {
			case EventToolCallEnd:
				toolEndIdx = i
			case EventSteeringInjected:
				if ev.Data["text"] != "steer: do X" {
					t.Fatalf("STEERING_INJECTED data: %+v", ev.Data)
				}
				steerIdx = i
			}
			i++
		}
		if toolEndIdx == -1 {
			t.Fatalf("expected TOOL_CALL_END event")
		}
		if steerIdx == -1 {
			t.Fatalf("expected STEERING_INJECTED event")
		}
		if steerIdx <= toolEndIdx {
			t.Fatalf("expected steering injection after tool round; TOOL_CALL_END=%d STEERING_INJECTED=%d", toolEndIdx, steerIdx)
		}
	}

func TestSession_ReasoningEffort_PassedThroughAndCanChange(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)
	release := make(chan struct{})

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "ok", nil
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "run")
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	sess.SetReasoningEffort("high")
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "low" {
		t.Fatalf("req1 reasoning_effort: %#v", reqs[0].ReasoningEffort)
	}
	if reqs[1].ReasoningEffort == nil || *reqs[1].ReasoningEffort != "high" {
		t.Fatalf("req2 reasoning_effort: %#v", reqs[1].ReasoningEffort)
	}
}

type tinyProfile struct {
	id   string
	cw   int
	mod  string
	opts map[string]any
}

func (p tinyProfile) ID() string                               { return p.id }
func (p tinyProfile) Model() string                            { return p.mod }
func (p tinyProfile) ToolDefinitions() []llm.ToolDefinition     { return nil }
func (p tinyProfile) SupportsParallelToolCalls() bool           { return false }
func (p tinyProfile) ContextWindowSize() int                    { return p.cw }
func (p tinyProfile) ProjectDocFiles() []string                 { return nil }
func (p tinyProfile) BuildSystemPrompt(EnvironmentInfo, []ProjectDoc, []SkillMeta) string { return "" }
func (p tinyProfile) CheapModel() string                                     { return p.mod }
func (p tinyProfile) WithModel(model string) ProviderProfile {
	return tinyProfile{id: p.id, cw: p.cw, mod: model}
}
func (p tinyProfile) WithBasePrompt(string) ProviderProfile { return p }
func (p tinyProfile) ProviderOptions() map[string]any       { return p.opts }
func (p tinyProfile) SupportsReasoning() bool               { return false }
func (p tinyProfile) SupportsStreaming() bool                { return false }
func (p tinyProfile) DefaultCommandTimeoutMS() int           { return 10_000 }
func (p tinyProfile) KnowledgeCutoff() string                { return "2025-01-01" }
func (p tinyProfile) ToolNameMap() map[string]string          { return nil }

func TestSession_ContextWindowAwareness_EmitsWarningOver80Percent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	// 40 chars => approxTokens=10. With cw=10, warning should emit at ~100% usage (>80% threshold).
	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 10}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	warn := ""
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			if msg, ok := ev.Data["message"].(string); ok {
				warn = msg
			}
		}
	}
	if warn == "" {
		t.Fatalf("expected WARNING event")
	}
	if !strings.Contains(warn, "~100% of context window") {
		t.Fatalf("warning message: %q", warn)
	}
}

func TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 1000}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	warned := false
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			warned = true
		}
	}
	if warned {
		t.Fatalf("did not expect WARNING event")
	}
}

func TestSession_AbortSignal_ClosesSessionAndEmitsSessionEnd(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)}},
						},
					},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{Name: "slow"},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "run")
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected abort error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProcessInput did not abort promptly")
	}

	sess.mu.Lock()
	closed := sess.state == SessionClosed
	sess.mu.Unlock()
	if !closed {
		t.Fatalf("expected session to be closed on abort signal")
	}

	gotEnd := false
	gotErr := false
	gotToolEnd := false
	errIdx := -1
	endIdx := -1
	i := 0
	for ev := range sess.Events() {
		if ev.Kind == EventError {
			gotErr = true
			errIdx = i
		}
		if ev.Kind == EventSessionEnd {
			gotEnd = true
			endIdx = i
		}
		if ev.Kind == EventToolCallEnd {
			gotToolEnd = true
		}
		i++
	}
	if !gotEnd {
		t.Fatalf("expected SESSION_END event")
	}
	if !gotErr {
		t.Fatalf("expected ERROR event on abort signal")
	}
	if !gotToolEnd {
		t.Fatalf("expected TOOL_CALL_END event on abort signal")
	}
	if errIdx != -1 && endIdx != -1 && errIdx > endIdx {
		t.Fatalf("expected ERROR event before SESSION_END on abort (err=%d end=%d)", errIdx, endIdx)
	}
}

func TestSession_CustomToolRegistration_OverridesExistingTool(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Override a built-in tool implementation.
	if err := sess.reg.Register(RegisteredTool{
		Definition: llm.ToolDefinition{
			Name: "read_file",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []string{"file_path"},
			},
		},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return "OVERRIDE", nil
		},
	}); err != nil {
		t.Fatalf("Register override: %v", err)
	}
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"x"}`),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if strings.TrimSpace(res.Output) != "OVERRIDE" {
		t.Fatalf("output: %q", res.Output)
	}
	sess.Close()
}

type errAdapter struct {
	name  string
	err   error
	calls int
}

func (a *errAdapter) Name() string { return a.name }
func (a *errAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	return llm.Response{}, a.err
}
func (a *errAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, fmt.Errorf("stream not implemented in errAdapter")
}

type flaky429Adapter struct {
	name      string
	failCount int
	calls     int
}

func (a *flaky429Adapter) Name() string { return a.name }
func (a *flaky429Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	a.calls++
	if a.calls <= a.failCount {
		return llm.Response{}, llm.ErrorFromHTTPStatus(a.name, 429, "rate limited", nil, nil)
	}
	return llm.Response{Message: llm.Assistant("ok")}, nil
}
func (a *flaky429Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, fmt.Errorf("stream not implemented in flaky429Adapter")
}

func TestSession_AuthenticationError_ClosesSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)}
	c.Register(a)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}

	sess.mu.Lock()
	closed := sess.state == SessionClosed
	sess.mu.Unlock()
	if !closed {
		t.Fatalf("expected session to be closed on authentication error")
	}
	if a.calls != 1 {
		t.Fatalf("adapter calls: got %d want 1", a.calls)
	}

	gotEnd := false
	for ev := range sess.Events() {
		if ev.Kind == EventSessionEnd {
			gotEnd = true
		}
	}
	if !gotEnd {
		t.Fatalf("expected SESSION_END event")
	}
}

func TestSession_ContextLengthError_EmitsWarningAndClosesSession(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 413, "too large", nil, nil)}
	c.Register(a)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}

	warn := false
	end := false
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			if msg, ok := ev.Data["message"].(string); ok && strings.Contains(msg, "Context length") {
				warn = true
			}
		}
		if ev.Kind == EventSessionEnd {
			end = true
		}
	}
	if !warn {
		t.Fatalf("expected WARNING event for context length overflow")
	}
	if !end {
		t.Fatalf("expected SESSION_END event")
	}
}

func TestSession_LLMError_EmitsErrorEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 500, "boom", nil, nil)}
	c.Register(a)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()

	errEv := false
	for ev := range sess.Events() {
		if ev.Kind == EventError {
			if s, _ := ev.Data["error"].(string); strings.Contains(s, "openai") {
				errEv = true
			}
		}
	}
	if !errEv {
		t.Fatalf("expected ERROR event")
	}
}

func TestSession_LLMTransientErrors_RetryWithBackoff(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	ad := &flaky429Adapter{name: "openai", failCount: 2}
	c.Register(ad)

	var sleeps []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		_ = ctx
		sleeps = append(sleeps, d)
		return nil
	}

	policy := llm.RetryPolicy{
		MaxRetries:        5,
		BaseDelay:         1 * time.Millisecond,
		MaxDelay:          1 * time.Second,
		BackoffMultiplier: 2.0,
		Jitter:            false,
	}

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		LLMSleep:       sleep,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("out: %q", out)
	}
	sess.Close()

	if got, want := ad.calls, 3; got != want {
		t.Fatalf("adapter calls: got %d want %d", got, want)
	}
	if got, want := len(sleeps), 2; got != want {
		t.Fatalf("sleep calls: got %d want %d (%v)", got, want, sleeps)
	}
	if sleeps[0] != 1*time.Millisecond {
		t.Fatalf("sleep[0]: got %s want %s", sleeps[0], 1*time.Millisecond)
	}
	if sleeps[1] != 2*time.Millisecond {
		t.Fatalf("sleep[1]: got %s want %s", sleeps[1], 2*time.Millisecond)
	}
}

func TestSession_Subagents_SpawnWaitClose_AndDepthLimit(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("subok")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn_agent output: %v (out=%q)", err, spawnRes.Output)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))
	if agentID == "" {
		t.Fatalf("missing agent_id in spawn output: %v", spawned)
	}

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}
	var waitResult SubAgentResult
	if err := json.Unmarshal([]byte(waitRes.Output), &waitResult); err != nil {
		t.Fatalf("unmarshal wait result: %v (out=%q)", err, waitRes.Output)
	}
	if waitResult.Output != "subok" {
		t.Fatalf("wait output: got %q want %q", waitResult.Output, "subok")
	}
	if !waitResult.Success {
		t.Fatalf("expected success=true")
	}

	// Depth limiting: subagent cannot spawn further subagents when MaxSubagentDepth=1.
	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing subagent session for %q", agentID)
	}
	if _, err := sub.sess.spawnAgent(context.Background(), "nested", "", "", 0); err == nil {
		t.Fatalf("expected depth limit error, got nil")
	}

	closeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}
	sess.Close()
}

func TestSession_SpawnAgent_ReturnsStatus(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if spawned["status"] != "running" {
		t.Fatalf("expected status 'running', got %v", spawned["status"])
	}
}

func TestSession_WaitAgent_ReturnsSubAgentResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("result text")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &spawned)
	agentID := fmt.Sprint(spawned["agent_id"])

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	var result SubAgentResult
	if err := json.Unmarshal([]byte(waitRes.Output), &result); err != nil {
		t.Fatalf("unmarshal SubAgentResult: %v (out=%q)", err, waitRes.Output)
	}
	if !result.Success {
		t.Fatalf("expected success=true, got false")
	}
	if result.Output != "result text" {
		t.Fatalf("expected output='result text', got %q", result.Output)
	}
	if result.TurnsUsed < 1 {
		t.Fatalf("expected turns_used >= 1, got %d", result.TurnsUsed)
	}
}

func TestSession_SendInput_UsesMessageParam(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	callNum := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{Message: llm.Assistant("first")}
			},
			func(req llm.Request) llm.Response {
				callNum++
				return llm.Response{Message: llm.Assistant("second")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Spawn agent.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"start"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &spawned)
	agentID := fmt.Sprint(spawned["agent_id"])

	// Wait for first task to finish.
	sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})

	// Send input using "message" parameter (not "input").
	sendRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "send_input",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"message":"do more"}`, agentID)),
	})
	if sendRes.IsError {
		t.Fatalf("send_input error: %s", sendRes.Output)
	}

	// Wait again and verify it ran.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c4",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}
}

func TestSession_SpawnAgent_MaxTurns(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it","max_turns":10}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}

	var spawned map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &spawned)
	agentID := fmt.Sprint(spawned["agent_id"])

	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatalf("missing subagent")
	}

	// Wait for it to finish so we can inspect the config.
	sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})

	if sub.sess.cfg.MaxTurns != 10 {
		t.Fatalf("expected MaxTurns=10, got %d", sub.sess.cfg.MaxTurns)
	}
}

func TestSubAgentStatus_Values(t *testing.T) {
	if SubAgentRunning != "running" {
		t.Fatalf("SubAgentRunning = %q, want 'running'", SubAgentRunning)
	}
	if SubAgentCompleted != "completed" {
		t.Fatalf("SubAgentCompleted = %q, want 'completed'", SubAgentCompleted)
	}
	if SubAgentFailed != "failed" {
		t.Fatalf("SubAgentFailed = %q, want 'failed'", SubAgentFailed)
	}
}

func TestSession_SpawnAgent_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("sub done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Spawn with model override.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"hello","model":"gpt-4.1-nano"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	agentID := fmt.Sprint(spawned["agent_id"])

	// Wait for sub-agent.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	// The fakeAdapter records all requests. The sub-agent's request should use overridden model.
	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatalf("no requests recorded")
	}
	subReq := reqs[len(reqs)-1]
	if subReq.Model != "gpt-4.1-nano" {
		t.Fatalf("sub-agent model: got %q want %q", subReq.Model, "gpt-4.1-nano")
	}
}

func TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &captureEnv{wd: "/tmp"}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if got := env.LastTimeoutMS(); got != 10_000 {
		t.Fatalf("default shell timeout: got %d want %d", got, 10_000)
	}

	// Override per-call timeout_ms.
	env2 := &captureEnv{wd: "/tmp"}
	f2 := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi","timeout_ms":1234}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c2 := llm.NewClient()
	c2.Register(f2)
	sess2, err := NewSession(c2, NewOpenAIProfile("gpt-5.2"), env2, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession2: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := sess2.ProcessInput(ctx2, "run"); err != nil {
		t.Fatalf("ProcessInput2: %v", err)
	}
	sess2.Close()
	if got := env2.LastTimeoutMS(); got != 1234 {
		t.Fatalf("override shell timeout: got %d want %d", got, 1234)
	}
}

func TestSession_ShellTool_CapsTimeoutToMaxCommandTimeoutMS(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo hi","timeout_ms":999999}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &captureEnv{wd: "/tmp"}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		MaxCommandTimeoutMS: 5000,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	if got := env.LastTimeoutMS(); got != 5000 {
		t.Fatalf("capped shell timeout: got %d want %d", got, 5000)
	}
}

func TestSession_ShellTool_TimeoutAppendsMessageToToolResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "shell", Arguments: json.RawMessage(`{"command":"sleep 30"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("ok")} },
		},
	}
	c.Register(f)

	env := &timeoutEnv{wd: dir}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "run"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	toolResult := ""
	for _, m := range reqs[1].Messages {
		if m.Role != llm.RoleTool {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
				if s, ok := p.ToolResult.Content.(string); ok {
					toolResult = s
				}
			}
		}
	}
	if toolResult == "" {
		t.Fatalf("expected tool result content in second request")
	}
	for _, want := range []string{
		"timed_out=true",
		"Command timed out after 10000ms",
		"You can retry with a longer timeout",
	} {
		if !strings.Contains(toolResult, want) {
			t.Fatalf("tool result missing %q:\n%s", want, toolResult)
		}
	}
}

type captureEnv struct {
	wd string

	mu        sync.Mutex
	lastCmd   string
	lastTOms  int
	lastWdArg string
}

func (e *captureEnv) Initialize() error { return nil }
func (e *captureEnv) Cleanup()          {}

func (e *captureEnv) WorkingDirectory() string { return e.wd }
func (e *captureEnv) Platform() string         { return "linux" }
func (e *captureEnv) OSVersion() string        { return "test" }

func (e *captureEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) WriteFile(path string, content string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) FileExists(path string) bool { return false }
func (e *captureEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *captureEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *captureEnv) ListDirectory(path string, depth int) ([]DirEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *captureEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	_ = ctx
	_ = envVars
	e.mu.Lock()
	e.lastCmd = command
	e.lastTOms = timeoutMS
	e.lastWdArg = workingDir
	e.mu.Unlock()
	return ExecResult{Stdout: "ok", Stderr: "", ExitCode: 0, TimedOut: false, DurationMS: 1}, nil
}

func (e *captureEnv) LastTimeoutMS() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTOms
}

type timeoutEnv struct {
	wd string
}

func (e *timeoutEnv) Initialize() error { return nil }
func (e *timeoutEnv) Cleanup()          {}

func (e *timeoutEnv) WorkingDirectory() string { return e.wd }
func (e *timeoutEnv) Platform() string         { return "linux" }
func (e *timeoutEnv) OSVersion() string        { return "test" }
func (e *timeoutEnv) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) WriteFile(path string, content string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) FileExists(path string) bool { return false }
func (e *timeoutEnv) Glob(pattern string, basePath string) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *timeoutEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (e *timeoutEnv) ListDirectory(path string, depth int) ([]DirEntry, error) {
	return nil, fmt.Errorf("not implemented")
}
func (e *timeoutEnv) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	_ = ctx
	_ = workingDir
	_ = envVars
	// Pretend git isn't available for this environment (session snapshot + doc discovery fall back cleanly).
	if strings.HasPrefix(strings.TrimSpace(command), "git ") {
		return ExecResult{ExitCode: 1}, fmt.Errorf("not a git repo")
	}
	return ExecResult{
		Stdout:     "partial output\n",
		Stderr:     "",
		ExitCode:   124,
		TimedOut:   true,
		DurationMS: int64(timeoutMS),
	}, context.DeadlineExceeded
}

func TestProcessInput_ToolChoiceIsAuto(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.ToolChoice == nil || req.ToolChoice.Mode != "auto" {
					t.Fatalf("expected tool_choice auto, got %+v", req.ToolChoice)
				}
				return llm.Response{
					Message: llm.Assistant("done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() { for range sess.Events() {} }()
	_, _ = sess.ProcessInput(context.Background(), "hello")
}

func TestProcessInput_DrainsSteeringBeforeFirstLLMCall(t *testing.T) {
	c := llm.NewClient()
	var firstReqMessages []llm.Message
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				firstReqMessages = req.Messages
				return llm.Response{
					Message: llm.Assistant("done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() { for range sess.Events() {} }()

	// Queue steering BEFORE ProcessInput
	sess.Steer("do it differently")
	_, _ = sess.ProcessInput(context.Background(), "hello")

	// The steering message should appear in the first LLM request
	found := false
	for _, m := range firstReqMessages {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "do it differently") {
			found = true
		}
	}
	if !found {
		t.Fatal("steering message not in first LLM request")
	}
}

func TestLoopDetection_PatternLength2(t *testing.T) {
	c := llm.NewClient()

	toolA := func(id string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "read_file",
			Arguments: json.RawMessage(`{"file_path":"a.txt"}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
			},
		}
	}
	toolB := func(id string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "glob",
			Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
			},
		}
	}

	// 6 alternating tool calls (A-B-A-B-A-B) then auto-"done" on step exhaustion.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolA("c1") },
			func(req llm.Request) llm.Response { return toolB("c2") },
			func(req llm.Request) llm.Response { return toolA("c3") },
			func(req llm.Request) llm.Response { return toolB("c4") },
			func(req llm.Request) llm.Response { return toolA("c5") },
			func(req llm.Request) llm.Response { return toolB("c6") },
		},
	}
	c.Register(f)

	enableLoop := true
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir),
		SessionConfig{
			EnableLoopDetection: &enableLoop,
			LoopDetectionWindow: 6,
			MaxToolRoundsPerInput: 20,
		})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var loopDetected bool
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == EventLoopDetection {
				loopDetected = true
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	time.Sleep(50 * time.Millisecond)

	if !loopDetected {
		t.Fatal("expected loop detection for A-B-A-B pattern of length 2")
	}
}

func TestProviderOptions_PassedToLLMRequest(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	profile := tinyProfile{id: "tiny", mod: "m", cw: 100_000}
	profile.opts = map[string]any{
		"anthropic": map[string]any{"beta": "test-beta"},
	}

	dir := t.TempDir()
	sess, err := NewSession(c, profile, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() { for range sess.Events() {} }()

	_, err = sess.ProcessInput(context.Background(), "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	if reqs[0].ProviderOptions == nil {
		t.Fatal("expected ProviderOptions to be set on request")
	}
	anth, ok := reqs[0].ProviderOptions["anthropic"].(map[string]any)
	if !ok {
		t.Fatalf("expected anthropic key in ProviderOptions, got %v", reqs[0].ProviderOptions)
	}
	if anth["beta"] != "test-beta" {
		t.Fatalf("expected beta=test-beta, got %v", anth["beta"])
	}
}

func TestMaxTurns_CountsConversationTurns(t *testing.T) {
	c := llm.NewClient()
	callNum := 0
	var mu sync.Mutex
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				// First input: return a tool call (round 1 of input 1)
				call := llm.ToolCallData{
					ID:        "c1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":"a.txt"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				// First input: return text (round 2 of input 1)
				return llm.Response{Message: llm.Assistant("done1"), Finish: llm.FinishReason{Reason: "stop"}}
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				// Second input: text only
				return llm.Response{Message: llm.Assistant("done2"), Finish: llm.FinishReason{Reason: "stop"}}
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				return llm.Response{Message: llm.Assistant("done3"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir),
		SessionConfig{MaxTurns: 2})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() { for range sess.Events() {} }()

	// First input (turn 1): should work (even with tool round)
	_, err = sess.ProcessInput(context.Background(), "first")
	if err != nil {
		t.Fatalf("first input: %v", err)
	}
	// Second input (turn 2): should work
	_, err = sess.ProcessInput(context.Background(), "second")
	if err != nil {
		t.Fatalf("second input: %v", err)
	}
	// Third input (turn 3): should hit limit
	_, err = sess.ProcessInput(context.Background(), "third")
	if err == nil {
		t.Fatal("expected turn limit error on third input")
	}
}

func TestAssistantTurn_CapturesUsageAndResponseID(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					ID:      "resp-123",
					Message: llm.Assistant("hello"),
					Finish:  llm.FinishReason{Reason: "stop"},
					Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
				}
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.Kind == TurnAssistant {
			if turn.ResponseID == "" {
				t.Fatal("expected non-empty ResponseID on assistant turn")
			}
			if turn.Usage.InputTokens == 0 {
				t.Fatal("expected non-zero usage on assistant turn")
			}
			return
		}
	}
	t.Fatal("no assistant turn found")
}

func TestSession_GracefulShutdown_ClosesSubagents(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	// Create a sub-session and manually register it as a subagent.
	subSess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession (sub): %v", err)
	}
	go func() { for range subSess.Events() {} }()

	sub := &subagent{
		id:   "test-sub",
		sess: subSess,
		done: make(chan struct{}),
	}
	sess.mu.Lock()
	sess.subagents["test-sub"] = sub
	sess.mu.Unlock()

	sess.Close()

	// Verify subagent was cleaned up from the map.
	sess.mu.Lock()
	remaining := len(sess.subagents)
	sess.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected 0 subagents after Close, got %d", remaining)
	}

	// Verify sub-session was closed.
	if subSess.State() != SessionClosed {
		t.Fatalf("subagent session state: got %q want %q", subSess.State(), SessionClosed)
	}
}

func TestSession_GracefulShutdown_SessionEndIncludesStateAndTurns(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("hello"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	})

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	var endData map[string]any
	for ev := range sess.Events() {
		if ev.Kind == EventSessionEnd {
			endData = ev.Data
		}
	}
	if endData == nil {
		t.Fatal("expected SESSION_END event")
	}
	// SESSION_END is emitted exactly once (dedup). When ProcessInput completes
	// successfully, it emits with the current state (IDLE); when only Close()
	// fires it, the state is CLOSED. Either is valid.
	if state, ok := endData["state"].(string); !ok || state == "" {
		t.Fatalf("SESSION_END state: got %v", endData["state"])
	}
	if turns, ok := endData["turns"].(int); !ok || turns < 1 {
		t.Fatalf("SESSION_END turns: got %v", endData["turns"])
	}
}

func TestSession_ToolResults_AggregatedIntoSingleTurn(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Model returns two parallel tool calls, then text.
	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{"file_path":"a.txt","content":"A"}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c2", Name: "write_file", Arguments: json.RawMessage(`{"file_path":"b.txt","content":"B"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write two files")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// After parallel tool calls, history should contain exactly one TurnToolResults turn (not two TurnTool turns).
	sess.mu.Lock()
	var toolResultTurns int
	var toolResultsTurns int
	for _, turn := range sess.history {
		if turn.Kind == TurnTool {
			toolResultTurns++
		}
		if turn.Kind == TurnToolResults {
			toolResultsTurns++
		}
	}
	sess.mu.Unlock()
	sess.Close()

	if toolResultTurns != 0 {
		t.Fatalf("expected 0 individual TurnTool turns, got %d", toolResultTurns)
	}
	if toolResultsTurns != 1 {
		t.Fatalf("expected 1 TurnToolResults turn, got %d", toolResultsTurns)
	}
}

func TestSession_ToolResults_ContainsAllCallIDs(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "anthropic",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{"file_path":"a.txt","content":"A"}`)}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c2", Name: "write_file", Arguments: json.RawMessage(`{"file_path":"b.txt","content":"B"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write two files")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The aggregated turn should contain both call IDs.
	sess.mu.Lock()
	var callIDs []string
	for _, turn := range sess.history {
		if turn.Kind == TurnToolResults {
			for _, p := range turn.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					callIDs = append(callIDs, p.ToolResult.ToolCallID)
				}
			}
		}
	}
	sess.mu.Unlock()
	sess.Close()

	if len(callIDs) != 2 {
		t.Fatalf("expected 2 call IDs in aggregated turn, got %d: %v", len(callIDs), callIDs)
	}
	foundC1, foundC2 := false, false
	for _, id := range callIDs {
		if id == "c1" {
			foundC1 = true
		}
		if id == "c2" {
			foundC2 = true
		}
	}
	if !foundC1 || !foundC2 {
		t.Fatalf("expected call IDs c1 and c2, got %v", callIDs)
	}
}

func TestSession_ToolResults_SingleCallAlsoAggregated(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{"file_path":"a.txt","content":"A"}`)}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("done")} },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write file")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Even a single tool call should use TurnToolResults.
	sess.mu.Lock()
	var foundToolResults bool
	for _, turn := range sess.history {
		if turn.Kind == TurnToolResults {
			foundToolResults = true
		}
		if turn.Kind == TurnTool {
			t.Fatalf("found individual TurnTool; expected TurnToolResults")
		}
	}
	sess.mu.Unlock()
	sess.Close()

	if !foundToolResults {
		t.Fatalf("expected TurnToolResults turn in history")
	}
}

// WS2a: AWAITING_INPUT state
func TestSession_AwaitingInput_QuestionMarkResponse(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("What file would you like me to edit?"),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionAwaitingInput {
		t.Fatalf("state after question: got %q want %q", got, SessionAwaitingInput)
	}
	sess.Close()
}

func TestSession_AwaitingInput_DeclarativeResponse_GoesIdle(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("I have completed the task."),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "do something")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after declarative response: got %q want %q", got, SessionIdle)
	}
	sess.Close()
}

func TestSession_AwaitingInput_TransitionsToProcessing(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("What language?"),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Assistant("Done writing Go code."),
					Finish:  llm.FinishReason{Reason: "stop"},
				}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First input: question → AWAITING_INPUT
	_, err = sess.ProcessInput(ctx, "write code")
	if err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	if got := sess.State(); got != SessionAwaitingInput {
		t.Fatalf("state after question: got %q want %q", got, SessionAwaitingInput)
	}

	// Second input: AWAITING_INPUT → PROCESSING → IDLE
	_, err = sess.ProcessInput(ctx, "Go")
	if err != nil {
		t.Fatalf("ProcessInput #2: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after answer: got %q want %q", got, SessionIdle)
	}
	sess.Close()
}

// WS2b: MaxTurns → IDLE transition
func TestSession_MaxTurns_SetsStateToIdle(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{MaxTurns: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	ctx := context.Background()
	// First input succeeds (turn 1 of 1).
	_, err = sess.ProcessInput(ctx, "first")
	if err != nil {
		t.Fatalf("first input: %v", err)
	}

	// Second input hits the turn limit.
	_, err = sess.ProcessInput(ctx, "second")
	if err == nil {
		t.Fatalf("expected error on turn limit")
	}

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after MaxTurns: got %q want %q", got, SessionIdle)
	}
	sess.Close()
}

// WS2c: SESSION_END after process_input
func TestSession_SessionEnd_AfterProcessInput(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("hello"), Finish: llm.FinishReason{Reason: "stop"}}
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var events []SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			events = append(events, ev)
		}
	}()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "hi")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// After ProcessInput returns, exactly one SESSION_END with reason "input_complete" should be emitted.
	// Close() must not emit a second SESSION_END (dedup via sessionEndEmitted flag).
	sess.Close()
	<-done

	endCount := 0
	var inputCompleteEnd bool
	for _, ev := range events {
		if ev.Kind == EventSessionEnd {
			endCount++
			if r, _ := ev.Data["reason"].(string); r == "input_complete" {
				inputCompleteEnd = true
			}
		}
	}
	if !inputCompleteEnd {
		t.Fatalf("expected SESSION_END with reason=input_complete")
	}
	if endCount != 1 {
		t.Fatalf("expected exactly 1 SESSION_END event, got %d", endCount)
	}
}

func TestSession_ToolNameMapping_ReverseDispatch(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Fake adapter returns a tool call using the OpenAI provider-specific name "exec_command".
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "call-1",
					Name:      "exec_command",
					Arguments: json.RawMessage(`{"command":"echo hello"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "run a command")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	// Verify tool call events use the canonical name "shell" (not "exec_command").
	mu.Lock()
	defer mu.Unlock()
	var toolStartNames, toolEndNames []string
	for _, ev := range events {
		if ev.Kind == EventToolCallStart {
			toolStartNames = append(toolStartNames, fmt.Sprint(ev.Data["tool_name"]))
		}
		if ev.Kind == EventToolCallEnd {
			toolEndNames = append(toolEndNames, fmt.Sprint(ev.Data["tool_name"]))
		}
	}
	if len(toolStartNames) != 1 || toolStartNames[0] != "shell" {
		t.Fatalf("TOOL_CALL_START tool_name: got %v, want [shell]", toolStartNames)
	}
	if len(toolEndNames) != 1 || toolEndNames[0] != "shell" {
		t.Fatalf("TOOL_CALL_END tool_name: got %v, want [shell]", toolEndNames)
	}

	// Verify the LLM request contained "exec_command" (mapped name) in tool definitions.
	reqs := f.Requests()
	if len(reqs) < 1 {
		t.Fatal("expected at least 1 request")
	}
	foundExecCommand := false
	for _, td := range reqs[0].Tools {
		if td.Name == "exec_command" {
			foundExecCommand = true
		}
		if td.Name == "shell" {
			t.Fatal("LLM request should not contain canonical 'shell' — should be mapped to 'exec_command'")
		}
	}
	if !foundExecCommand {
		t.Fatal("LLM request tools should contain 'exec_command'")
	}
}

func TestSession_ToolNameMapping_EventsUseCanonicalName(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Fake adapter returns grep_files (OpenAI provider name for grep).
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "call-g",
					Name:      "grep_files",
					Arguments: json.RawMessage(`{"pattern":"foo"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "search files")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallStart || ev.Kind == EventToolCallEnd {
			name := fmt.Sprint(ev.Data["tool_name"])
			if name == "grep_files" {
				t.Fatalf("event %s should use canonical name 'grep', got provider name 'grep_files'", ev.Kind)
			}
			if name != "grep" {
				continue // other tools (from other events)
			}
		}
	}
}

func TestSession_ShellDescription_IncludedInToolCallStartEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "call-sh",
					Name:      "exec_command",
					Arguments: json.RawMessage(`{"command":"ls","description":"List project files"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "list files")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallStart {
			desc, ok := ev.Data["description"].(string)
			if ok && desc == "List project files" {
				return // success
			}
		}
	}
	t.Fatal("TOOL_CALL_START event should include 'description' field from shell args")
}

func TestSession_ReadBeforeWrite_WarnsOnUnreadFile(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Pre-create the file so it's not a new file.
	os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("original"), 0644)

	// LLM writes to existing.txt without reading it first.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "c1",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"file_path":"existing.txt","content":"new content"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "write the file")
	sess.Close()
	<-done

	// Check that the tool output contains a warning about writing to an unread file.
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.Data["tool_name"]) == "write_file" {
			output := fmt.Sprint(ev.Data["full_output"])
			if strings.Contains(output, "WARNING") && strings.Contains(output, "not been read") {
				return // success
			}
		}
	}
	t.Fatal("expected WARNING about writing to unread file")
}

func TestSession_ReadBeforeWrite_NoWarningAfterRead(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("original"), 0644)

	// LLM reads the file first, then writes to it.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "c1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":"existing.txt"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "c2",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"file_path":"existing.txt","content":"new content"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "read then write")
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.Data["tool_name"]) == "write_file" {
			output := fmt.Sprint(ev.Data["full_output"])
			if strings.Contains(output, "WARNING") {
				t.Fatal("should not warn about write after read")
			}
		}
	}
}

func TestSession_ReadBeforeWrite_NewFileNoWarning(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Do NOT pre-create the file — it's a new file.

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "c1",
					Name:      "write_file",
					Arguments: json.RawMessage(`{"file_path":"brand_new.txt","content":"new content"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	var events []SessionEvent
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "create new file")
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.Data["tool_name"]) == "write_file" {
			output := fmt.Sprint(ev.Data["full_output"])
			if strings.Contains(output, "WARNING") {
				t.Fatal("should not warn when creating a new file")
			}
		}
	}
}

func TestSession_ReasoningEffort_MediumPassedThrough(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "hello")
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort: got %#v, want 'medium'", reqs[0].ReasoningEffort)
	}
}

func TestSession_ReasoningEffort_EmptyMeansNoOverride(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		// ReasoningEffort left empty — no override.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "hello")
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	if reqs[0].ReasoningEffort != nil {
		t.Fatalf("expected nil ReasoningEffort (no override), got %#v", reqs[0].ReasoningEffort)
	}
}

func TestSession_Subagent_IndependentHistory(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	callCount := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Subagent gets a multi-tool conversation.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "s1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo sub"}`), Type: "function"}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{Message: llm.Assistant("subagent done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Parent starts with just the initial state — record history length.
	parentHistBefore := len(sess.history)

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do something"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &spawned)
	agentID := fmt.Sprint(spawned["agent_id"])

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	// Parent history should be unchanged — subagent history is separate.
	parentHistAfter := len(sess.history)
	if parentHistAfter != parentHistBefore {
		t.Fatalf("parent history changed: before=%d after=%d; subagent history leaked into parent", parentHistBefore, parentHistAfter)
	}
}

func TestSession_Subagent_SharedFilesystem(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	// Write a file in the parent's working directory.
	os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("hello from parent"), 0644)

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Subagent reads the shared file.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "s1", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"shared.txt"}`), Type: "function"}},
						},
					},
				}
			},
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("read it")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"read shared.txt"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &spawned)
	agentID := fmt.Sprint(spawned["agent_id"])

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	// The subagent's LLM should have received the file content in a tool result.
	reqs := f.Requests()
	found := false
	for _, req := range reqs {
		for _, msg := range req.Messages {
			for _, p := range msg.Content {
				if p.Kind == llm.ContentToolResult && strings.Contains(fmt.Sprint(p.ToolResult.Content), "hello from parent") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("subagent did not receive parent-written file content via shared filesystem")
	}
}

func TestSubagent_MaxTurns_DefaultsTo50_NotInheritedFromParent(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID: "c1", Name: "spawn_agent", Type: "function",
						Arguments: json.RawMessage(`{"task":"test task"}`),
					}}},
				},
			}
		},
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		},
	}}
	c.Register(f)
	dir := t.TempDir()
	// Parent has MaxTurns=100.
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{MaxTurns: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.ProcessInput(ctx, "spawn something")

	// Check the subagent's MaxTurns.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, sub := range sess.subagents {
		sub.sess.mu.Lock()
		mt := sub.sess.cfg.MaxTurns
		sub.sess.mu.Unlock()
		if mt != 50 {
			t.Fatalf("subagent MaxTurns=%d, want 50 (should not inherit parent's 100)", mt)
		}
	}
}

func TestCloseAgent_ReturnsStructuredStatus(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Subagent completes with a result.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("subagent output text")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Spawn agent directly via registry.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do something"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := fmt.Sprint(spawned["agent_id"])

	// Wait for subagent to finish.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	// Close the agent.
	closeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}

	// Verify close result is structured JSON with expected fields.
	var result map[string]any
	if err := json.Unmarshal([]byte(closeRes.Output), &result); err != nil {
		t.Fatalf("close_agent result is not JSON: %q (err: %v)", closeRes.Output, err)
	}
	if _, ok := result["status"]; !ok {
		t.Error("close_agent result missing 'status' field")
	}
	if _, ok := result["output"]; !ok {
		t.Error("close_agent result missing 'output' field")
	}
	if _, ok := result["turns_used"]; !ok {
		t.Error("close_agent result missing 'turns_used' field")
	}
	if result["status"] != "completed" {
		t.Errorf("close_agent status=%v, want 'completed'", result["status"])
	}
}

func TestDetectLoop_Patterns(t *testing.T) {
	tests := []struct {
		name   string
		sigs   []string
		window int
		want   bool
	}{
		{"len1_match", []string{"A", "A", "A", "A"}, 4, true},
		{"len2_match", []string{"A", "B", "A", "B", "A", "B"}, 6, true},
		{"len3_match", []string{"A", "B", "C", "A", "B", "C"}, 6, true},
		{"no_pattern", []string{"A", "B", "C", "D", "E", "F"}, 6, false},
		{"too_short", []string{"A", "A"}, 4, false},
		{"len1_window4", []string{"X", "X", "A", "A", "A", "A"}, 4, true},
		{"len2_window6", []string{"X", "A", "B", "A", "B", "A", "B"}, 6, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectLoop(tt.sigs, tt.window); got != tt.want {
				t.Fatalf("detectLoop(%v, %d) = %v, want %v", tt.sigs, tt.window, got, tt.want)
			}
		})
	}
}
