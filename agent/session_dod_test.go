package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// toolCallEndOutput extracts the output from a TOOL_CALL_END event,
// checking both "output" (success) and "error" (failure) keys.
func toolCallEndOutput(ev SessionEvent) string {
	if v, ok := ev.DataMap()["output"]; ok {
		return fmt.Sprint(v)
	}
	if v, ok := ev.DataMap()["error"]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

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
	_, err = sess.ProcessInput(ctx, "loop", nil)
	if err != nil {
		t.Fatalf("round limit should return nil error, got %v", err)
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
	_, err = sess.ProcessInput(ctx, "loop", nil)
	if err != nil {
		t.Fatalf("round limit should return nil error, got %v", err)
	}
	sess.Close()

	turnLimit := false
	for ev := range sess.Events() {
		if ev.Kind == EventTurnLimit {
			turnLimit = true
			if d, ok := ev.Data.(TurnLimitData); !ok || d.MaxToolRoundsPerInput != 2 {
				t.Fatalf("expected max_tool_rounds_per_input=2 in event data, got %v", ev.Data)
			}
		}
	}
	if !turnLimit {
		t.Fatalf("expected TURN_LIMIT event when round limit reached")
	}
}

func TestSession_MaxToolRoundsPerInput_NegativeMeansUnlimited(t *testing.T) {
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

	// 3 rounds of tool calls, then fall through to fakeAdapter's default "done" response.
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg("1") },
			func(req llm.Request) llm.Response { return toolMsg("2") },
			func(req llm.Request) llm.Response { return toolMsg("3") },
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: -1, // unlimited
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := sess.ProcessInput(ctx, "do stuff", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sess.Close()

	if result != "done" {
		t.Fatalf("expected result 'done', got %q", result)
	}
	// 4 total LLM calls: 3 tool rounds + 1 final communicate call.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("expected 4 LLM requests, got %d", got)
	}
}

func TestSessionConfig_ApplyDefaults_MaxToolRounds(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"zero defaults to 200", 0, 200},
		{"positive preserved", 50, 50},
		{"negative preserved (unlimited)", -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SessionConfig{MaxToolRoundsPerInput: tt.input}
			cfg.applyDefaults()
			if cfg.MaxToolRoundsPerInput != tt.want {
				t.Fatalf("MaxToolRoundsPerInput: got %d, want %d", cfg.MaxToolRoundsPerInput, tt.want)
			}
		})
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
			func(req llm.Request) llm.Response { return finalResponse("hello") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "hi", nil); err != nil || strings.TrimSpace(out) != "hello" {
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
		EventAssistantTextEnd,
		EventToolCallStart,
		EventCommunicate,
		EventToolCallEnd,
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

func TestSession_EventSystem_UserInputCarriesTurnIndex(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok-1") },
			func(req llm.Request) llm.Response { return finalResponse("ok-2") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessID := sess.ID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatalf("ProcessInput #2: %v", err)
	}
	sess.Close()

	var emittedTurns []int
	for ev := range sess.Events() {
		if ev.Kind != EventUserInput {
			continue
		}
		d, ok := ev.Data.(UserInputData)
		if !ok {
			t.Fatalf("USER_INPUT data type: %T", ev.Data)
		}
		if d.Turn <= 0 {
			t.Fatalf("USER_INPUT.Turn = %d, want > 0", d.Turn)
		}
		emittedTurns = append(emittedTurns, d.Turn)
	}
	if len(emittedTurns) != 2 {
		t.Fatalf("expected 2 USER_INPUT events, got %d (turns=%v)", len(emittedTurns), emittedTurns)
	}

	tpath := filepath.Join(dir, sessionsSubdir, sessID+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	var transcriptTurns []int
	for i, e := range entries {
		if e.Turn.Kind == TurnUserInput {
			transcriptTurns = append(transcriptTurns, i+1)
		}
	}
	if !reflect.DeepEqual(emittedTurns, transcriptTurns) {
		t.Fatalf("emitted USER_INPUT turns %v != transcript USER_INPUT entry indices %v", emittedTurns, transcriptTurns)
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "write", nil); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("ProcessInput: out=%q err=%v", out, err)
	}
	sess.Close()

	seenStart := false
	seenDelta := false
	seenEnd := false
	for ev := range sess.Events() {
		switch ev.Kind {
		case EventToolCallStart:
			if ev.DataMap()["call_id"] != "c1" || ev.DataMap()["tool_name"] != "write_file" {
				continue
			}
			seenStart = true
			if ev.DataMap()["call_id"] != "c1" || ev.DataMap()["tool_name"] != "write_file" {
				t.Fatalf("TOOL_CALL_START data: %+v", ev.Data)
			}
		case EventToolCallOutputDelta:
			if ev.DataMap()["call_id"] != "c1" {
				continue
			}
			seenDelta = true
			if !seenStart || seenEnd {
				t.Fatalf("TOOL_CALL_OUTPUT_DELTA ordering violated (start=%t end=%t)", seenStart, seenEnd)
			}
		case EventToolCallEnd:
			if ev.DataMap()["call_id"] != "c1" {
				continue
			}
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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}})
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
	_, err = sess.ProcessInput(ctx, "first input", nil)
	if err != nil {
		t.Fatalf("expected first input to succeed, got %v", err)
	}

	// Second input should hit the turn limit but return nil error.
	_, err = sess.ProcessInput(ctx, "second input", nil)
	if err != nil {
		t.Fatalf("turn limit should return nil error, got %v", err)
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
			func(req llm.Request) llm.Response { return finalResponse("first") },
			func(req llm.Request) llm.Response { return finalResponse("second") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := sess.ProcessInput(ctx, "one", nil); err != nil || strings.TrimSpace(out) != "first" {
		t.Fatalf("first: out=%q err=%v", out, err)
	}
	if out, err := sess.ProcessInput(ctx, "two", nil); err != nil || strings.TrimSpace(out) != "second" {
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
					return finalResponse("missing steering")
				}
				return finalResponse("ok")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "slow"}},
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
		out, err := sess.ProcessInput(ctx, "run", nil)
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
			if ev.DataMap()["tool_name"] == "slow" {
				toolEndIdx = i
			}
		case EventSteeringInjected:
			if ev.DataMap()["text"] != "steer: do X" {
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
				return finalResponse("ok")
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "slow"}},
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
		_, err := sess.ProcessInput(ctx, "run", nil)
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

func TestSession_PopulatesModelRequestMetadata(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_, err = sess.ProcessInput(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("requests: got 0 want at least 1")
	}
	req := reqs[0]
	if req.SessionID != sess.ID() {
		t.Fatalf("SessionID = %q, want %q", req.SessionID, sess.ID())
	}
	if req.ThreadID != sess.ID() {
		t.Fatalf("ThreadID = %q, want %q", req.ThreadID, sess.ID())
	}
	if req.PromptCacheKey != sess.ID() {
		t.Fatalf("PromptCacheKey = %q, want %q", req.PromptCacheKey, sess.ID())
	}
	if got := req.ClientMetadata[codexInstallationIDMetadataKey]; got == "" {
		t.Fatalf("client metadata missing %s: %#v", codexInstallationIDMetadataKey, req.ClientMetadata)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "installation_id")); err != nil {
		t.Fatalf("installation_id file: %v", err)
	}
}

func TestSession_RetainsEncryptedReasoningAcrossToolRound(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{
							Kind: llm.ContentThinking,
							Thinking: &llm.ThinkingData{
								EncryptedContent: "enc_reasoning_123",
							},
						},
						{
							Kind: llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{
								ID:        "call_1",
								Name:      "noop",
								Arguments: json.RawMessage(`{}`),
							},
						},
					},
				},
			}
		},
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "noop"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			_ = env
			_ = args
			return "ok", nil
		},
	})

	_, err = sess.ProcessInput(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d want 2", len(reqs))
	}
	for _, msg := range reqs[1].Messages {
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, part := range msg.Content {
			if part.Kind == llm.ContentThinking && part.Thinking != nil && part.Thinking.EncryptedContent == "enc_reasoning_123" {
				return
			}
		}
	}
	t.Fatalf("second request missing encrypted reasoning in history: %#v", reqs[1].Messages)
}

type tinyProfile struct {
	id   string
	cw   int
	mod  string
	opts map[string]any
}

func (p tinyProfile) ID() string                            { return p.id }
func (p tinyProfile) Model() string                         { return p.mod }
func (p tinyProfile) ToolDefinitions() []llm.ToolDefinition { return nil }
func (p tinyProfile) SupportsParallelToolCalls() bool       { return false }
func (p tinyProfile) ContextWindowSize() int                { return p.cw }
func (p tinyProfile) ProjectDocFiles() []string             { return nil }
func (p tinyProfile) CheapModel() string                    { return p.mod }
func (p tinyProfile) WithModel(model string) ProviderProfile {
	return tinyProfile{id: p.id, cw: p.cw, mod: model}
}
func (p tinyProfile) ProviderOptions() map[string]any { return p.opts }
func (p tinyProfile) SupportsReasoning() bool         { return false }
func (p tinyProfile) ReasoningEffortLevels() []string { return nil }
func (p tinyProfile) SupportsStreaming() bool         { return false }
func (p tinyProfile) SupportsWebSearch() bool         { return false }
func (p tinyProfile) DefaultCommandTimeoutMS() int    { return 10_000 }
func (p tinyProfile) KnowledgeCutoff() string         { return "2025-01-01" }
func (p tinyProfile) ToolNameMap() map[string]string  { return nil }
func (p tinyProfile) NewToolRegistry() *ToolRegistry  { return NewToolRegistry() }

func TestSession_ContextWindowAwareness_EmitsWarningOver80Percent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	// With cw=100 and ~110 tokens of content (system prompt agents section + user input),
	// warning should emit since usage exceeds the 80% threshold.
	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 100}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40), nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	warn := ""
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			if msg, ok := ev.DataMap()["message"].(string); ok {
				warn = msg
			}
		}
	}
	if warn == "" {
		t.Fatalf("expected WARNING event")
	}
	if !strings.Contains(warn, "of context window") {
		t.Fatalf("warning message: %q", warn)
	}
}

func TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "tiny",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, tinyProfile{id: "tiny", mod: "m", cw: 1_000_000}, NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, strings.Repeat("a", 40), nil)
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

// TestSession_AbortSignal_KeepsSessionAliveAndEmitsInterruptedSessionEnd
// asserts the kata 0ax1 semantics: aborting an in-flight turn cancels the
// current LLM/tool round but leaves the session in IDLE so the caller can
// immediately submit a follow-up. SESSION_END must fire with reason
// "interrupted" and Interrupted=true so consumers can render the turn as
// canceled while the thread status returns to idle.
func TestSession_AbortSignal_KeepsSessionAliveAndEmitsInterruptedSessionEnd(t *testing.T) {
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "slow"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})

	// Per-turn cancel modeled on cmd/serf/serve.go: outer ctx stays alive,
	// only the turn ctx is cancelled.
	outerCtx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()
	turnCtx, cancelTurn := context.WithCancel(outerCtx)

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(turnCtx, "run", nil)
		done <- err
	}()

	select {
	case <-started:
	case <-outerCtx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	cancelTurn()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected abort error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProcessInput did not abort promptly")
	}

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after abort: got %q want %q (session must stay alive)", got, SessionIdle)
	}

	// Drain events asynchronously so Close() can complete; we'll inspect
	// them after the session shuts down.
	var (
		evMu              sync.Mutex
		gotEnd            bool
		gotErr            bool
		gotToolEnd        bool
		gotSteering       bool
		sawInterruptedEnd bool
	)
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			evMu.Lock()
			switch ev.Kind {
			case EventError:
				gotErr = true
			case EventSessionEnd:
				gotEnd = true
				if d, ok := ev.Data.(SessionEndData); ok && d.Interrupted && d.Reason == "interrupted" && d.State == string(SessionIdle) {
					sawInterruptedEnd = true
				}
			case EventToolCallEnd:
				gotToolEnd = true
			case EventSteeringInjected:
				gotSteering = true
			}
			evMu.Unlock()
		}
	}()

	sess.Close()
	<-evDone

	evMu.Lock()
	defer evMu.Unlock()
	if !gotEnd {
		t.Fatalf("expected SESSION_END event")
	}
	if !gotErr {
		t.Fatalf("expected ERROR event on abort signal")
	}
	if !gotToolEnd {
		t.Fatalf("expected TOOL_CALL_END event on abort signal")
	}
	if !sawInterruptedEnd {
		t.Fatalf("expected SESSION_END with Reason=interrupted, Interrupted=true, State=IDLE")
	}
	if !gotSteering {
		t.Fatalf("expected STEERING_INJECTED event for the interrupt transcript marker")
	}
}

// TestSession_AbortThenFollowup verifies kata 0ax1's primary user-facing
// promise: after an interrupt the session is immediately ready to accept
// another ProcessInput, and that follow-up turn runs to completion.
func TestSession_AbortThenFollowup(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	started := make(chan struct{}, 1)

	// Turn 1: model issues one tool call to "slow" (blocks on ctx).
	// Turn 2: model finishes with plain text.
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
				return finalResponse("hello after interrupt")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "slow"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	outerCtx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()

	// Turn 1: start and interrupt.
	turn1Ctx, cancel1 := context.WithCancel(outerCtx)
	done1 := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(turn1Ctx, "run the slow thing", nil)
		done1 <- err
	}()
	select {
	case <-started:
	case <-outerCtx.Done():
		t.Fatalf("turn 1: timed out waiting for tool to start")
	}
	cancel1()
	select {
	case err := <-done1:
		if err == nil {
			t.Fatalf("turn 1: expected abort error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("turn 1: ProcessInput did not abort promptly")
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after interrupt: got %q want %q", got, SessionIdle)
	}

	// Turn 2: follow-up runs to completion under a fresh per-turn ctx.
	turn2Ctx, cancel2 := context.WithCancel(outerCtx)
	defer cancel2()
	out, err := sess.ProcessInput(turn2Ctx, "what about now?", nil)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if !strings.Contains(out, "hello after interrupt") {
		t.Fatalf("turn 2 output: %q (want it to contain follow-up reply)", out)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after turn 2: got %q want %q", got, SessionIdle)
	}
}

func TestSession_AbortDrainsQueuedInputWithFreshContext(t *testing.T) {
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
			func(req llm.Request) llm.Response {
				return finalResponse("queued turn completed")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{Name: "slow"}},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
			_ = env
			_ = args
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	var (
		evMu     sync.Mutex
		endKinds []string
	)
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			if ev.Kind == EventSessionEnd {
				if d, ok := ev.Data.(SessionEndData); ok {
					evMu.Lock()
					endKinds = append(endKinds, d.Reason)
					evMu.Unlock()
				}
			}
		}
	}()

	outerCtx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()
	turnCtx, cancelTurn := context.WithCancel(outerCtx)
	turnCtx = WithQueuedInputDrainOnInterrupt(turnCtx, outerCtx)
	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, err := sess.ProcessInput(turnCtx, "run the slow thing", nil)
		done <- struct {
			out string
			err error
		}{out: out, err: err}
	}()

	select {
	case <-started:
	case <-outerCtx.Done():
		t.Fatalf("timed out waiting for tool to start")
	}
	if err := sess.Enqueue(context.Background(), "queued after interrupt"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cancelTurn()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ProcessInput: %v", got.err)
		}
		if !strings.Contains(got.out, "queued turn completed") {
			t.Fatalf("ProcessInput output=%q, want queued turn completion", got.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProcessInput did not drain queued input after interrupt")
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after interrupted drain=%d, want 0", depth)
	}
	sess.Close()
	<-evDone
	evMu.Lock()
	defer evMu.Unlock()
	if len(endKinds) < 2 || endKinds[0] != "interrupted" || endKinds[1] != "input_complete" {
		t.Fatalf("SESSION_END reasons=%v, want interrupted then input_complete", endKinds)
	}
}

func TestSession_AbortErrorDrainsQueuedInputWithFreshContext(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	started := make(chan struct{}, 1)
	releaseAbort := make(chan struct{})

	c.Register(&fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				started <- struct{}{}
				<-releaseAbort
				return llm.Response{}, llm.NewAbortError("user canceled")
			},
			func(req llm.Request) (llm.Response, error) {
				return finalResponse("queued turn completed after abort error"), nil
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	outerCtx, outerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer outerCancel()
	turnCtx, cancelTurn := context.WithCancel(outerCtx)
	turnCtx = WithQueuedInputDrainOnInterrupt(turnCtx, outerCtx)
	done := make(chan struct {
		out string
		err error
	}, 1)
	go func() {
		out, err := sess.ProcessInput(turnCtx, "run the slow thing", nil)
		done <- struct {
			out string
			err error
		}{out: out, err: err}
	}()

	select {
	case <-started:
	case <-outerCtx.Done():
		t.Fatalf("timed out waiting for model call to start")
	}
	if err := sess.Enqueue(context.Background(), "queued after abort error"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cancelTurn()
	close(releaseAbort)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ProcessInput: %v", got.err)
		}
		if !strings.Contains(got.out, "queued turn completed after abort error") {
			t.Fatalf("ProcessInput output=%q, want queued abort-error turn completion", got.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProcessInput did not drain queued input after abort error")
	}
	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth after abort-error drain=%d, want 0", depth)
	}
}

func TestQueuedInputDrainRootAcceptsAbortErrorForCanceledMarkedTurn(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	turnCtx, cancelTurn := context.WithCancel(rootCtx)
	markedCtx := WithQueuedInputDrainOnInterrupt(turnCtx, rootCtx)
	cancelTurn()

	got, ok := queuedInputDrainContext(markedCtx, llm.NewAbortError("user canceled"))
	if !ok {
		t.Fatal("queuedInputDrainContext rejected AbortError from canceled marked turn")
	}
	if got != rootCtx {
		t.Fatal("queuedInputDrainContext did not return the root context")
	}
}

func TestQueuedInputDrainContextUsesFreshCancelableTurnContext(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	turnCtx, cancelTurn := context.WithCancel(rootCtx)
	var cancelDrain context.CancelFunc
	markedCtx := WithQueuedInputDrainOnInterruptHandler(turnCtx, rootCtx, func(root context.Context) (context.Context, context.CancelFunc) {
		drainCtx, cancel := context.WithCancel(root)
		cancelDrain = cancel
		return drainCtx, cancel
	})
	cancelTurn()

	got, ok := queuedInputDrainContext(markedCtx, context.Canceled)
	if !ok {
		t.Fatal("queuedInputDrainContext rejected canceled marked turn")
	}
	if got == rootCtx {
		t.Fatal("queuedInputDrainContext returned root context, want fresh turn context")
	}
	if got.Err() != nil {
		t.Fatalf("fresh drain context is already canceled: %v", got.Err())
	}
	cancelDrain()
	if !errors.Is(got.Err(), context.Canceled) {
		t.Fatalf("fresh drain context err=%v, want context.Canceled after cancelDrain", got.Err())
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name: "read_file",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []string{"file_path"},
			},
		}},
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
	return nil, llm.ErrStreamUnsupported
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
	return finalResponse("ok"), nil
}
func (a *flaky429Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected error")
	}

	warn := false
	end := false
	for ev := range sess.Events() {
		if ev.Kind == EventWarning {
			data := ev.DataMap()
			if msg, ok := data["message"].(string); ok && strings.Contains(msg, "Context length") {
				if data["source"] != "provider" || data["title"] != "Provider error" {
					t.Fatalf("warning diagnostic=%+v", data)
				}
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()

	errEv := false
	for ev := range sess.Events() {
		if ev.Kind == EventError {
			data := ev.DataMap()
			if s, _ := data["error"].(string); strings.Contains(s, "openai") {
				if data["source"] != "provider" || data["title"] != "Provider error" {
					t.Fatalf("error diagnostic=%+v", data)
				}
				errEv = true
			}
		}
	}
	if !errEv {
		t.Fatalf("expected ERROR event")
	}
}

func TestSession_LLMError_WritesStructuredAPICallDiagnostic(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: llm.ErrorFromHTTPStatus("openai", 500, "boom", nil, nil)}
	c.Register(a)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       stateDir,
		LLMRetryPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()
	for range sess.Events() {
	}

	lines := readTranscriptLines(t, sess.TranscriptPath())
	var got TranscriptAPICall
	for _, line := range lines {
		if strings.Contains(line, `"kind":"api_call"`) {
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("unmarshal api call: %v", err)
			}
			break
		}
	}
	if got.Kind != "api_call" {
		t.Fatalf("api_call not found in transcript: %v", lines)
	}
	if got.Source != "provider" || got.Title != "Provider error" {
		t.Fatalf("api_call diagnostic=%+v", got)
	}
}

func TestSession_ConfigurationError_EmitsSerfDiagnosticEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: &llm.ConfigurationError{Message: "unknown provider: openrouter"}}
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()

	for ev := range sess.Events() {
		if ev.Kind != EventError {
			continue
		}
		data := ev.DataMap()
		if data["source"] != "serf" || data["title"] != "Serf configuration error" {
			t.Fatalf("error diagnostic=%+v", data)
		}
		return
	}
	t.Fatalf("expected ERROR event")
}

func TestSession_RuntimeError_EmitsSerfDiagnosticEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	a := &errAdapter{name: "openai", err: errors.New("session runtime exploded")}
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	sess.Close()

	for ev := range sess.Events() {
		if ev.Kind != EventError {
			continue
		}
		data := ev.DataMap()
		if data["source"] != "serf" || data["title"] != "Serf error" {
			t.Fatalf("error diagnostic=%+v", data)
		}
		return
	}
	t.Fatalf("expected ERROR event")
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
	out, err := sess.ProcessInput(ctx, "hi", nil)
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
			// Auto-nudge: default subagents get nudged to call communicate.
			func(req llm.Request) llm.Response { return finalResponse("subok") },
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

	// Subagent management is top-level only: child sessions cannot spawn further subagents.
	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing subagent session for %q", agentID)
	}
	if _, err := sub.sess.spawnAgent(context.Background(), "nested", "", "", 0, "", "", nil, nil); err == nil {
		t.Fatalf("expected top-level-only error, got nil")
	} else if !strings.Contains(err.Error(), "top-level only") {
		t.Fatalf("error = %q, want top-level-only error", err)
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
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
			// Auto-nudge: default subagents get nudged to call communicate.
			func(req llm.Request) llm.Response { return finalResponse("result text") },
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
		t.Fatal(err)
	}
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
	if result.Status != SubAgentCompleted {
		t.Fatalf("expected status=%q, got %q", SubAgentCompleted, result.Status)
	}
}

func TestSession_WaitAgent_FailedSubagentReturnsResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	ad := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, fmt.Errorf("simulated child failure")
			},
		},
	}
	c.Register(ad)

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
		t.Fatal(err)
	}
	agentID := fmt.Sprint(spawned["agent_id"])

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait should return a failed result, not a tool error: %s", waitRes.Output)
	}

	var result SubAgentResult
	if err := json.Unmarshal([]byte(waitRes.Output), &result); err != nil {
		t.Fatalf("unmarshal failed SubAgentResult: %v (out=%q)", err, waitRes.Output)
	}
	if result.Success {
		t.Fatalf("expected success=false, got true")
	}
	if result.Status != SubAgentFailed {
		t.Fatalf("expected status=%q, got %q", SubAgentFailed, result.Status)
	}
}

func TestSession_SpawnAgent_BlockingFailureReturnsResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	ad := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, fmt.Errorf("simulated child failure")
			},
		},
	}
	c.Register(ad)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do it","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking spawn should return a failed result, not a tool error: %s", res.Output)
	}

	var result SubAgentResult
	if err := json.Unmarshal([]byte(res.Output), &result); err != nil {
		t.Fatalf("unmarshal blocking result: %v (out=%q)", err, res.Output)
	}
	if result.Success {
		t.Fatalf("expected success=false, got true")
	}
	if result.Status != SubAgentFailed {
		t.Fatalf("expected status=%q, got %q", SubAgentFailed, result.Status)
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
				return finalResponse("first")
			},
			func(req llm.Request) llm.Response {
				callNum++
				return finalResponse("second")
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
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatal(err)
	}
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
		Name:      "resume_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"message":"do more"}`, agentID)),
	})
	if sendRes.IsError {
		t.Fatalf("resume_agent error: %s", sendRes.Output)
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
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
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatal(err)
	}
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
			func(req llm.Request) llm.Response { return finalResponse("sub done") },
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	if _, err := sess.ProcessInput(ctx, "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	got, ok := env.TimeoutForCommand("echo hi")
	if !ok {
		t.Fatal("expected ExecCommand call with 'echo hi'")
	}
	if got != 120_000 {
		t.Fatalf("default shell timeout: got %d want %d", got, 120_000)
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	if _, err := sess2.ProcessInput(ctx2, "run", nil); err != nil {
		t.Fatalf("ProcessInput2: %v", err)
	}
	sess2.Close()
	got2, ok2 := env2.TimeoutForCommand("echo hi")
	if !ok2 {
		t.Fatal("expected ExecCommand call with 'echo hi'")
	}
	if got2 != 1234 {
		t.Fatalf("override shell timeout: got %d want %d", got2, 1234)
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	if _, err := sess.ProcessInput(ctx, "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	got, ok := env.TimeoutForCommand("echo hi")
	if !ok {
		t.Fatal("expected ExecCommand call with 'echo hi'")
	}
	if got != 5000 {
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
			func(req llm.Request) llm.Response { return finalResponse("ok") },
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
	if _, err := sess.ProcessInput(ctx, "run", nil); err != nil {
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
		"Command timed out after 120000ms",
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
	calls     []captureCall // all ExecCommand invocations
}

type captureCall struct {
	Command   string
	TimeoutMS int
	WorkDir   string
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
func (e *captureEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
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
	e.calls = append(e.calls, captureCall{Command: command, TimeoutMS: timeoutMS, WorkDir: workingDir})
	e.mu.Unlock()
	return ExecResult{Stdout: "ok", Stderr: "", ExitCode: 0, TimedOut: false, DurationMS: 1}, nil
}

func (e *captureEnv) LastTimeoutMS() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTOms
}

// TimeoutForCommand returns the timeout used for the first ExecCommand call matching cmd.
func (e *captureEnv) TimeoutForCommand(cmd string) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.calls {
		if strings.Contains(c.Command, cmd) {
			return c.TimeoutMS, true
		}
	}
	return 0, false
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
func (e *timeoutEnv) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
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

func TestProcessInput_ToolChoiceIsRequired(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.ToolChoice == nil || req.ToolChoice.Mode != "required" {
					t.Fatalf("expected tool_choice required, got %+v", req.ToolChoice)
				}
				resp := finalResponse("done")
				resp.Finish = llm.FinishReason{Reason: "stop"}
				return resp
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
	go func() {
		for range sess.Events() {
		}
	}()
	_, _ = sess.ProcessInput(context.Background(), "hello", nil)
}

func TestProcessInput_DrainsSteeringBeforeFirstLLMCall(t *testing.T) {
	c := llm.NewClient()
	var firstReqMessages []llm.Message
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				firstReqMessages = req.Messages
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Assistant("done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				})
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
	go func() {
		for range sess.Events() {
		}
	}()

	// Queue steering BEFORE ProcessInput
	sess.Steer("do it differently")
	_, _ = sess.ProcessInput(context.Background(), "hello", nil)

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
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	enableLoop := true
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir),
		SessionConfig{
			EnableLoopDetection:   &enableLoop,
			LoopDetectionWindow:   6,
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
	_, err = sess.ProcessInput(ctx, "test", nil)
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
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Assistant("done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				})
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
	go func() {
		for range sess.Events() {
		}
	}()

	_, err = sess.ProcessInput(context.Background(), "hello", nil)
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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("done1"), Finish: llm.FinishReason{Reason: "stop"}})
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				// Second input: text only
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("done2"), Finish: llm.FinishReason{Reason: "stop"}})
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("done3"), Finish: llm.FinishReason{Reason: "stop"}})
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
	go func() {
		for range sess.Events() {
		}
	}()

	// First input (turn 1): should work (even with tool round)
	_, err = sess.ProcessInput(context.Background(), "first", nil)
	if err != nil {
		t.Fatalf("first input: %v", err)
	}
	// Second input (turn 2): should work
	_, err = sess.ProcessInput(context.Background(), "second", nil)
	if err != nil {
		t.Fatalf("second input: %v", err)
	}
	// Third input (turn 3): should hit limit but return nil error
	_, err = sess.ProcessInput(context.Background(), "third", nil)
	if err != nil {
		t.Fatalf("turn limit should return nil error, got: %v", err)
	}
}

func TestAssistantTurn_CapturesUsageAndResponseID(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{
					ID:      "resp-123",
					Message: llm.Assistant("hello"),
					Finish:  llm.FinishReason{Reason: "stop"},
					Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5},
				})
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
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hi", nil)
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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("done"), Finish: llm.FinishReason{Reason: "stop"}})
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	// Create a sub-session and manually register it as a subagent.
	subSess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession (sub): %v", err)
	}
	go func() {
		for range subSess.Events() {
		}
	}()

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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("hello"), Finish: llm.FinishReason{Reason: "stop"}})
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
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	var endPayload *SessionEndData
	for ev := range sess.Events() {
		if ev.Kind == EventSessionEnd {
			if d, ok := ev.Data.(SessionEndData); ok {
				endPayload = &d
			}
		}
	}
	if endPayload == nil {
		t.Fatal("expected SESSION_END event")
	}
	// SESSION_END is emitted exactly once (dedup). When ProcessInput completes
	// successfully, it emits with the current state (IDLE); when only Close()
	// fires it, the state is CLOSED. Either is valid.
	if endPayload.State == "" {
		t.Fatalf("SESSION_END state: got %q", endPayload.State)
	}
	if endPayload.Turns < 1 {
		t.Fatalf("SESSION_END turns: got %d", endPayload.Turns)
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write two files", nil)
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
			hasNonCommunicate := false
			for _, p := range turn.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.Name != "communicate" {
					hasNonCommunicate = true
					break
				}
			}
			if hasNonCommunicate {
				toolResultsTurns++
			}
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewAnthropicProfile("claude-test"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write two files", nil)
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
					if p.ToolResult.Name == "communicate" {
						continue
					}
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "write file", nil)
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
				return toolCallResponse(communicateCallArgs("ask1", map[string]any{
					"await_reply": true,
					"message":     "What file would you like me to edit?",
				}))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "hello", nil)
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
				return toolCallResponse(communicateCallArgs("msg1", map[string]any{
					"await_reply": false,
					"message":     "I have completed the task.",
				}))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = sess.ProcessInput(ctx, "do something", nil)
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
				return toolCallResponse(communicateCallArgs("ask2", map[string]any{
					"await_reply": true,
					"message":     "What language?",
				}))
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(communicateCallArgs("msg2", map[string]any{
					"await_reply": false,
					"message":     "Done writing Go code.",
				}))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First input: question → AWAITING_INPUT
	_, err = sess.ProcessInput(ctx, "write code", nil)
	if err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	if got := sess.State(); got != SessionAwaitingInput {
		t.Fatalf("state after question: got %q want %q", got, SessionAwaitingInput)
	}

	// Second input: AWAITING_INPUT → PROCESSING → IDLE
	_, err = sess.ProcessInput(ctx, "Go", nil)
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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{MaxTurns: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx := context.Background()
	// First input succeeds (turn 1 of 1).
	_, err = sess.ProcessInput(ctx, "first", nil)
	if err != nil {
		t.Fatalf("first input: %v", err)
	}

	// Second input hits the turn limit but returns nil error.
	_, err = sess.ProcessInput(ctx, "second", nil)
	if err != nil {
		t.Fatalf("turn limit should return nil error, got %v", err)
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
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("hello"), Finish: llm.FinishReason{Reason: "stop"}})
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
	_, err = sess.ProcessInput(ctx, "hi", nil)
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
			if r, _ := ev.DataMap()["reason"].(string); r == "input_complete" {
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
				return finalResponse("done")
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
	_, err = sess.ProcessInput(ctx, "run a command", nil)
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
			toolStartNames = append(toolStartNames, fmt.Sprint(ev.DataMap()["tool_name"]))
		}
		if ev.Kind == EventToolCallEnd {
			toolEndNames = append(toolEndNames, fmt.Sprint(ev.DataMap()["tool_name"]))
		}
	}
	filteredStartNames := slices.DeleteFunc(append([]string(nil), toolStartNames...), func(name string) bool { return name == "communicate" })
	filteredEndNames := slices.DeleteFunc(append([]string(nil), toolEndNames...), func(name string) bool { return name == "communicate" })
	if len(filteredStartNames) != 1 || filteredStartNames[0] != "shell" {
		t.Fatalf("TOOL_CALL_START tool_name: got %v, want [shell]", toolStartNames)
	}
	if len(filteredEndNames) != 1 || filteredEndNames[0] != "shell" {
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
				return finalResponse("done")
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
	_, err = sess.ProcessInput(ctx, "search files", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallStart || ev.Kind == EventToolCallEnd {
			name := fmt.Sprint(ev.DataMap()["tool_name"])
			if name == "grep_files" {
				t.Fatalf("event %s should use canonical name 'grep', got provider name 'grep_files'", ev.Kind)
			}
			if name != "grep" {
				continue // other tools (from other events)
			}
		}
	}
}

func TestSession_ToolPurpose_IncludedInToolCallStartEvent(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				call := llm.ToolCallData{
					ID:        "call-sh",
					Name:      "exec_command",
					Arguments: json.RawMessage(`{"command":"ls","purpose":"List project files"}`),
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
				return finalResponse("done")
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
	_, err = sess.ProcessInput(ctx, "list files", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallStart {
			desc, ok := ev.DataMap()["description"].(string)
			if ok && desc == "List project files" {
				return // success
			}
		}
	}
	t.Fatal("TOOL_CALL_START event should include description field from tool purpose")
}

func TestSession_ReadBeforeWrite_WarnsOnUnreadFile(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Pre-create the file so it's not a new file.
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

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
				return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "write the file", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	// Check that the tool output contains a warning about writing to an unread file.
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.DataMap()["tool_name"]) == "write_file" {
			output := toolCallEndOutput(ev)
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
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

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
				return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "read then write", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.DataMap()["tool_name"]) == "write_file" {
			output := toolCallEndOutput(ev)
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
				return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "create new file", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range events {
		if ev.Kind == EventToolCallEnd && fmt.Sprint(ev.DataMap()["tool_name"]) == "write_file" {
			output := toolCallEndOutput(ev)
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
				return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "medium" {
		t.Fatalf("reasoning_effort: got %#v, want 'medium'", reqs[0].ReasoningEffort)
	}
}

func TestSession_TaskListUpdateEscalatesReasoningEffort(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	taskListCall := func(id string, args string) llm.Response {
		return llm.Response{Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: id, Name: "task_list", Arguments: json.RawMessage(args)},
			}},
		}}
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Step 1: create a task.
			func(req llm.Request) llm.Response {
				return taskListCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"work","prompt":"do stuff"}]}`)
			},
			// Step 2: start it (in_progress, no effort override yet).
			func(req llm.Request) llm.Response {
				return taskListCall("c2", `{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`)
			},
			// Step 3: escalate reasoning_effort to high.
			func(req llm.Request) llm.Response {
				return taskListCall("c3", `{"action":"update","updates":[{"id":1,"status":"in_progress","reasoning_effort":"high"}]}`)
			},
			// Step 4: done — this request should carry effort=high.
			func(req llm.Request) llm.Response {
				return finalResponse("done")
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
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	reqs := f.Requests()
	if len(reqs) < 4 {
		t.Fatalf("expected >=4 requests, got %d", len(reqs))
	}

	// Request 0-2 should have effort=low (session default, no task override yet or task has no effort set).
	for i := 0; i < 3; i++ {
		if reqs[i].ReasoningEffort == nil || *reqs[i].ReasoningEffort != "low" {
			t.Fatalf("req[%d] reasoning_effort: got %#v, want 'low'", i, reqs[i].ReasoningEffort)
		}
	}

	// Request 3 (after task escalated to high) should have effort=high.
	if reqs[3].ReasoningEffort == nil || *reqs[3].ReasoningEffort != "high" {
		t.Fatalf("req[3] reasoning_effort after escalation: got %#v, want 'high'", reqs[3].ReasoningEffort)
	}
}

func TestSession_TaskList_AppendAndUpdate_EmitToolStateSnapshots(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	taskListCall := func(id, args string) llm.Response {
		return llm.Response{Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: id, Name: "task_list", Arguments: json.RawMessage(args)},
			}},
		}}
	}
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return taskListCall("c1", `{"action":"append","tasks":[{"type":"research","description":"Map criteria","prompt":"look at specs"},{"type":"implement","description":"Write tests","prompt":"add cases"}]}`)
			},
			func(req llm.Request) llm.Response {
				return taskListCall("c2", `{"action":"update","updates":[{"id":1,"status":"done","notes":"reviewed specs"}]}`)
			},
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "go", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	type endCall struct {
		callID string
		state  []Task
	}
	var ends []endCall
	for ev := range sess.Events() {
		if ev.Kind != EventToolCallEnd {
			continue
		}
		d := ev.DataMap()
		if d["tool_name"] != "task_list" {
			continue
		}
		raw, ok := d["tool_state"]
		if !ok {
			t.Fatalf("TOOL_CALL_END for task_list missing tool_state; data=%+v", d)
		}
		// DataMap() returns a map[string]any — tool_state is a
		// json.RawMessage which comes through as a string or []byte depending
		// on round-trip. Re-marshal and unmarshal to normalize.
		js, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal tool_state: %v", err)
		}
		// If it came through as a JSON string containing JSON (double-encoded),
		// peel one layer.
		var unquoted string
		if err := json.Unmarshal(js, &unquoted); err == nil && strings.HasPrefix(strings.TrimSpace(unquoted), "[") {
			js = []byte(unquoted)
		}
		var tasks []Task
		if err := json.Unmarshal(js, &tasks); err != nil {
			t.Fatalf("tool_state not a []Task: %v; raw=%s", err, js)
		}
		ends = append(ends, endCall{callID: fmt.Sprint(d["call_id"]), state: tasks})
	}

	if len(ends) < 2 {
		t.Fatalf("expected >=2 task_list TOOL_CALL_END events, got %d", len(ends))
	}

	// After append (c1): both tasks present, no statuses yet in "done".
	appendState := ends[0]
	if appendState.callID != "c1" {
		t.Errorf("first task_list end: call_id %q, want c1", appendState.callID)
	}
	if len(appendState.state) != 2 {
		t.Fatalf("append state should have 2 tasks, got %d: %+v", len(appendState.state), appendState.state)
	}
	if appendState.state[0].Description != "Map criteria" {
		t.Errorf("append state[0].Description: %q, want %q", appendState.state[0].Description, "Map criteria")
	}
	if appendState.state[0].ID != 1 {
		t.Errorf("append state[0].ID: %d, want 1", appendState.state[0].ID)
	}

	// After update (c2): task 1 marked done, description preserved.
	updateState := ends[1]
	if updateState.callID != "c2" {
		t.Errorf("second task_list end: call_id %q, want c2", updateState.callID)
	}
	if len(updateState.state) < 1 || updateState.state[0].Status != TaskDone {
		t.Errorf("update state[0].Status: %+v, want done", updateState.state)
	}
	if len(updateState.state) >= 1 && updateState.state[0].Description != "Map criteria" {
		t.Errorf("update state[0] description should be preserved; got %q", updateState.state[0].Description)
	}
}

func TestSession_ReasoningEffort_EmptyMeansNoOverride(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("done")
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
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
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
				return finalResponse("subagent done")
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
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("hello from parent"), 0644); err != nil {
		t.Fatal(err)
	}

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
				return finalResponse("read it")
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
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatal(err)
	}
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

func TestSubagent_MaxTurns_DefaultsTo500_NotInheritedFromParent(t *testing.T) {
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return finalResponse("done")
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

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}

	// Check the subagent's MaxTurns.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, sub := range sess.subagents {
		sub.sess.mu.Lock()
		mt := sub.sess.cfg.MaxTurns
		sub.sess.mu.Unlock()
		if mt != 500 {
			t.Fatalf("subagent MaxTurns=%d, want 500 (should not inherit parent's 100)", mt)
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
				return finalResponse("subagent output text")
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

	// Verify close result is a SubAgentResult and removes the agent from the session.
	var result SubAgentResult
	if err := json.Unmarshal([]byte(closeRes.Output), &result); err != nil {
		t.Fatalf("close_agent result is not JSON: %q (err: %v)", closeRes.Output, err)
	}
	if result.Status != SubAgentCompleted {
		t.Errorf("close_agent status=%v, want %q", result.Status, SubAgentCompleted)
	}
	if result.Output != "subagent output text" {
		t.Errorf("close_agent output=%q, want %q", result.Output, "subagent output text")
	}
	if got := sess.getSub(agentID); got != nil {
		t.Fatalf("expected close_agent to remove subagent from session, found %+v", got)
	}
}

func TestSession_SubagentEndEvent_EmittedOnce(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("subagent done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	var events []SessionEvent
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			events = append(events, ev)
		}
	}()

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

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
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
	<-evDone

	endCount := 0
	for _, ev := range events {
		if ev.Kind == EventSubagentEnd {
			endCount++
		}
	}
	if endCount != 1 {
		t.Fatalf("expected exactly 1 SUBAGENT_END event, got %d", endCount)
	}
}

func TestSubagent_WorkingDir_SharesParentPIDTracking(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	env := NewLocalExecutionEnvironment(dir)
	defer env.Cleanup()

	childEnv := env.WithWorkingDirectory(subDir)
	if childEnv.WorkingDirectory() != subDir {
		t.Fatalf("child working dir: %q, want %q", childEnv.WorkingDirectory(), subDir)
	}

	// Store a PID in the child and verify it's visible from the parent.
	childEnv.runningPIDs.Store(12345, struct{}{})
	_, ok := env.runningPIDs.Load(12345)
	if !ok {
		t.Fatal("PID stored in child env not visible in parent env — PID tracking is not shared")
	}

	// Clean up fake PID to avoid Cleanup() trying to signal it.
	env.runningPIDs.Delete(12345)
}

func TestSendInput_SteersRunningAgent(t *testing.T) {
	// Create a minimal subagent entry with a running session to verify
	// sendInput uses Steer() on running agents instead of rejecting them.
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Create a subagent session manually.
	subSess, err := NewSession(c, NewOpenAIProfile("test-model"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	sub := &subagent{
		id:      subSess.id,
		sess:    subSess,
		status:  SubAgentRunning,
		running: true,
		done:    make(chan struct{}),
	}
	sess.mu.Lock()
	sess.subagents[sub.id] = sub
	sess.mu.Unlock()

	// sendInput on a running agent should Steer instead of erroring.
	_, err = sess.sendInput(context.Background(), sub.id, "steered message")
	if err != nil {
		t.Fatalf("sendInput on running agent should not error, got: %v", err)
	}

	// Verify the steering message was queued.
	subSess.mu.Lock()
	queue := make([]string, 0, len(subSess.steeringQueue))
	for _, m := range subSess.steeringQueue {
		queue = append(queue, m.Text)
	}
	subSess.mu.Unlock()

	if len(queue) != 1 || queue[0] != "steered message" {
		t.Fatalf("expected steering queue=[\"steered message\"], got %v", queue)
	}

	// Clean up.
	sub.mu.Lock()
	sub.running = false
	close(sub.done)
	sub.mu.Unlock()
	subSess.Close()
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

// GAP-2.16: Round limit returns nil error and accumulated text (not an error).
func TestSession_RoundLimit_ReturnsNilError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	toolMsg := func(id, text string) llm.Response {
		call := llm.ToolCallData{
			ID:        id,
			Name:      "glob",
			Arguments: json.RawMessage(`{"pattern":"*.go","path":"."}`),
			Type:      "function",
		}
		return llm.Response{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: text},
					{Kind: llm.ContentToolCall, ToolCall: &call},
				},
			},
		}
	}

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolMsg("1", "round one") },
			func(req llm.Request) llm.Response { return toolMsg("2", "round two") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "loop", nil)
	if err != nil {
		t.Fatalf("round limit should return nil error, got: %v", err)
	}
}

// GAP-2.17: Turn limit uses >= so MaxTurns=2 allows exactly 2 inputs.
func TestSession_TurnLimit_UsesGreaterEqual(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok1"), Finish: llm.FinishReason{Reason: "stop"}})
			},
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok2"), Finish: llm.FinishReason{Reason: "stop"}})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Turn 1 succeeds.
	_, err = sess.ProcessInput(ctx, "first", nil)
	if err != nil {
		t.Fatalf("first input should succeed: %v", err)
	}

	// Turn 2 succeeds.
	_, err = sess.ProcessInput(ctx, "second", nil)
	if err != nil {
		t.Fatalf("second input should succeed: %v", err)
	}

	// Turn 3 should be blocked (turns=3 >= MaxTurns=2).
	_, err = sess.ProcessInput(ctx, "third", nil)
	// Under GAP-2.04, the turn limit returns nil error, not an error.
	if err != nil {
		t.Fatalf("turn limit should return nil error, got: %v", err)
	}
}

// GAP-2.04: Turn limit returns nil error (not an error).
func TestSession_TurnLimit_ReturnsNilError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}})
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
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Turn 1 succeeds.
	_, err = sess.ProcessInput(ctx, "first", nil)
	if err != nil {
		t.Fatalf("first input should succeed: %v", err)
	}

	// Turn 2 should be blocked but return nil error.
	_, err = sess.ProcessInput(ctx, "second", nil)
	if err != nil {
		t.Fatalf("turn limit should return nil error, got: %v", err)
	}
}

func TestSession_Subagent_DefaultGetsSubagentInstructions(t *testing.T) {
	// Bug: default subagents (no agent_type) inherit the parent's full system prompt
	// including "MUST spawn a research subagent" — but subagents can't delegate further.
	// They should get a UserInstructionOverride that tells them to do work directly.
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
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
		Arguments: json.RawMessage(`{"task":"survey this project"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatal(err)
	}
	agentID := fmt.Sprint(spawned["agent_id"])

	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing subagent session for %q", agentID)
	}

	if sub.sess.cachedSystemPrompt == "" {
		t.Fatal("default subagent should have a rendered system prompt")
	}

	if !strings.Contains(sub.sess.cachedSystemPrompt, "communicate") {
		t.Errorf("subagent prompt should mention communicate, got: %s", sub.sess.cachedSystemPrompt)
	}

	// Wait for completion so goroutine doesn't leak.
	sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})
}

func TestSession_SetsGenerousRequestTimeout(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = sess.ProcessInput(context.Background(), "hello", nil)

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("expected at least one request")
	}
	req := f.requests[0]
	if req.AdapterTimeout == nil {
		t.Fatal("Session should set AdapterTimeout on the LLM request")
	}
	if req.AdapterTimeout.Request < 5*time.Minute {
		t.Errorf("Request timeout should be >= 5 minutes for agentic workloads, got %v", req.AdapterTimeout.Request)
	}
}

func TestSession_Subagent_DoesNotGetParentDelegationPrompt(t *testing.T) {
	// Default subagents should NOT see the parent's "spawn a research subagent"
	// instructions. Their rendered system prompt should contain only focused
	// subagent instructions.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Subagent needs one response to complete.
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Spawn a default subagent.
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task"}`),
	})
	if res.IsError {
		t.Fatal(res.Output)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(res.Output), &parsed)
	agentID := fmt.Sprint(parsed["agent_id"])

	sub := sess.getSub(agentID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("missing subagent session for %q", agentID)
	}

	if sub.sess.cachedSystemPrompt == "" {
		t.Fatal("default subagent should have a rendered system prompt")
	}

	if strings.Contains(sub.sess.cachedSystemPrompt, "You are a coordinator") {
		t.Error("subagent prompt should not contain the coordinator persona")
	}
	if strings.Contains(sub.sess.cachedSystemPrompt, "### CRITICAL: You must spawn an implementer") {
		t.Error("subagent prompt should not contain coordinator delegation instructions")
	}

	// It should contain communicate guidance.
	if !strings.Contains(sub.sess.cachedSystemPrompt, "communicate") {
		t.Error("subagent prompt should mention communicate")
	}

	// Wait for the subagent to complete so we can check the adapter's recorded requests.
	sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":2000}`, agentID)),
	})

	// Verify the subagent's LLM request does NOT include delegation tools.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("expected subagent to make at least one LLM request")
	}
	// The subagent's request is the LAST one (after the parent's spawn_agent turn).
	subReq := f.requests[len(f.requests)-1]

	// Check API-level tool definitions — spawn_agent should NOT be callable.
	toolNames := make(map[string]bool)
	for _, td := range subReq.Tools {
		toolNames[td.Name] = true
	}
	for _, forbidden := range []string{"spawn_agent", "resume_agent", "wait", "close_agent"} {
		if toolNames[forbidden] {
			t.Errorf("subagent should not have %q in its API tool list", forbidden)
		}
	}
	// communicate should still be available.
	if !toolNames["communicate"] {
		t.Error("subagent should have communicate in its API tool list")
	}

	// Verify the base prompt (in system message) mentions communicate.
	sysMsg := subReq.Messages[0]
	if sysMsg.Role != "system" {
		t.Fatalf("expected first message to be system, got %s", sysMsg.Role)
	}
	sysTxt := sysMsg.Content[0].Text
	if !strings.Contains(sysTxt, "communicate") {
		t.Error("subagent system prompt should mention communicate")
	}
	// Base prompt should NOT contain the parent's delegation section.
	if strings.Contains(sysTxt, "Subagent delegation") {
		t.Error("subagent system prompt should NOT contain the parent's 'Subagent delegation' section")
	}
}

func TestSession_Wait_ClampsShortTimeout(t *testing.T) {
	// The wait tool should clamp very short timeouts to minWaitTimeoutMS,
	// preventing the model from burning rounds with rapid 1-second retries.
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Subagent needs one response to complete.
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Spawn a subagent.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task"}`),
	})
	if spawnRes.IsError {
		t.Fatal(spawnRes.Output)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &parsed)
	agentID := fmt.Sprint(parsed["agent_id"])

	// Wait with a 1-second timeout — should be clamped to minWaitTimeoutMS.
	// Since the subagent completes immediately (fakeAdapter), the clamped
	// timeout should be long enough to succeed. Without clamping, a 1s
	// timeout would fail if the subagent takes >1s.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":1000}`, agentID)),
	})
	if waitRes.IsError {
		t.Errorf("wait with 1s timeout should succeed (clamped to %dms), got error: %s", minWaitTimeoutMS, waitRes.Output)
	}
	if !strings.Contains(waitRes.Output, `"success"`) {
		t.Errorf("expected successful wait result, got: %s", waitRes.Output)
	}

	// Verify the constant is at least 30s.
	if minWaitTimeoutMS < 30_000 {
		t.Errorf("minWaitTimeoutMS should be at least 30000, got %d", minWaitTimeoutMS)
	}
}

func TestSession_WaitAgent_ErrorsOnReWait(t *testing.T) {
	// After a subagent's results have been consumed via wait, subsequent
	// wait calls should return an error instead of silently returning
	// empty success (closed-channel polling bug).
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("subagent done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Spawn a subagent.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task"}`),
	})
	if spawnRes.IsError {
		t.Fatal(spawnRes.Output)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &parsed)
	agentID := fmt.Sprint(parsed["agent_id"])

	// First wait should succeed.
	wait1 := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":120000}`, agentID)),
	})
	if wait1.IsError {
		t.Fatalf("first wait should succeed, got error: %s", wait1.Output)
	}

	// Second wait should return an error.
	wait2 := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":120000}`, agentID)),
	})
	if !wait2.IsError {
		t.Errorf("second wait should error (results already consumed), got success: %s", wait2.Output)
	}
	if !strings.Contains(wait2.Output, "already") {
		t.Errorf("error should mention results already consumed, got: %s", wait2.Output)
	}
}

func TestSession_Subagent_AutoNudgeOnMissingCommunicate(t *testing.T) {
	// When a subagent stops without calling communicate, the runner
	// should automatically send a nudge message and let it try again once.
	dir := t.TempDir()
	c := llm.NewClient()

	var mu sync.Mutex
	callCount := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// First call: subagent returns text without communicate.
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return llm.Response{Message: llm.Assistant("I found some stuff")}
			},
			// Second call (after nudge): subagent calls communicate.
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return finalResponse("Here are my findings")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	// Spawn a subagent.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"survey the project"}`),
	})
	if spawnRes.IsError {
		t.Fatal(spawnRes.Output)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &parsed)
	agentID := fmt.Sprint(parsed["agent_id"])

	// Wait for the subagent.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":120000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	var result SubAgentResult
	json.Unmarshal([]byte(waitRes.Output), &result)

	// The auto-nudge should have caused a second ProcessInput call,
	// resulting in the communicate output.
	if !strings.Contains(result.Output, "Here are my findings") {
		t.Errorf("expected nudged subagent to report findings via communicate, got: %q", result.Output)
	}
	mu.Lock()
	cc := callCount
	mu.Unlock()
	if cc < 2 {
		t.Errorf("expected at least 2 LLM calls (original + nudge), got %d", cc)
	}
}

func TestSession_Subagent_AutoNudgeOnEmptyResponseExhaustion(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	var mu sync.Mutex
	callCount := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return emptyResponse()
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return emptyResponse()
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return emptyResponse()
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return emptyResponse()
			},
			func(req llm.Request) llm.Response {
				mu.Lock()
				callCount++
				mu.Unlock()
				return finalResponse("Recovered after nudge")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	defer sess.Close()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"survey the project"}`),
	})
	if spawnRes.IsError {
		t.Fatal(spawnRes.Output)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(spawnRes.Output), &parsed)
	agentID := fmt.Sprint(parsed["agent_id"])

	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":120000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	var result SubAgentResult
	json.Unmarshal([]byte(waitRes.Output), &result)
	if !strings.Contains(result.Output, "Recovered after nudge") {
		t.Errorf("expected nudged subagent to report findings via communicate, got: %q", result.Output)
	}
	mu.Lock()
	cc := callCount
	mu.Unlock()
	if cc != 5 {
		t.Errorf("expected 5 LLM calls (4 empty + nudge), got %d", cc)
	}
}
