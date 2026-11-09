package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// launchFailingEnv is a local environment whose streaming executor refuses to
// start any command, so the shell tool returns a failed/start_failed result.
type launchFailingEnv struct {
	*execenv.LocalExecutionEnvironment
}

func (*launchFailingEnv) StreamCommand(context.Context, string, string, map[string]string, io.Writer) (*execenv.StreamHandle, error) {
	return nil, fmt.Errorf("executor refused to launch")
}

// waitFailingEnv is a local environment whose streaming executor launches and
// then fails in Wait, so the shell tool returns failed/wait_failed through the
// complete-or-handle marshal path.
type waitFailingEnv struct {
	*execenv.LocalExecutionEnvironment
}

func (*waitFailingEnv) StreamCommand(context.Context, string, string, map[string]string, io.Writer) (*execenv.StreamHandle, error) {
	return &execenv.StreamHandle{
		Wait:   func() (int, error) { return 1, fmt.Errorf("stream wait failed") },
		Signal: func() {},
	}, nil
}

// toolResultsMadeProgress is the goal no-progress signal: only an executed,
// successful, progress-eligible result counts. Unknown names, read-only tools,
// the result/communicate tool, task_list, failed calls and prevalidation-only
// rejections all stay non-progress (CORE-04).
func TestToolResultsMadeProgressEligibility(t *testing.T) {
	t.Parallel()
	sess, _, _ := newBudgetSession(t, SessionConfig{}, nil)
	result := func(name string) tool.ExecResult { return tool.ExecResult{ToolName: name} }

	tests := []struct {
		name    string
		results []tool.ExecResult
		want    bool
	}{
		{"successful mutation", []tool.ExecResult{result("write_file")}, true},
		{"read-only", []tool.ExecResult{result("read_file")}, false},
		{"failed mutation", []tool.ExecResult{{ToolName: "write_file", IsError: true}}, false},
		{"prevalidation-only rejection", []tool.ExecResult{{ToolName: "write_file", IsError: true, PrevalOnly: true}}, false},
		{"unknown tool", []tool.ExecResult{result("no_such_tool")}, false},
		{"result tool", []tool.ExecResult{result(sess.resultToolName())}, false},
		{"task list", []tool.ExecResult{result("task_list")}, false},
		{
			"one success among failures",
			[]tool.ExecResult{{ToolName: "write_file", IsError: true}, result("write_file")},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sess.toolResultsMadeProgress(tc.results); got != tc.want {
				t.Fatalf("toolResultsMadeProgress(%v) = %t, want %t", tc.results, got, tc.want)
			}
		})
	}
}

// Consecutive continuation turns that only attempt failed or unknown mutations
// must reach the no-progress breaker. Before CORE-04 the proposed calls reset
// the streak every turn and the goal stayed active forever.
func TestGoalNoProgressBreakerIgnoresFailedAndUnknownMutations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(turn int, dir string) (string, []byte)
	}{
		{
			name: "unknown tool names",
			call: func(turn int, _ string) (string, []byte) {
				return fmt.Sprintf("no_such_tool_%d", turn), []byte(`{}`)
			},
		},
		{
			name: "invalid arguments",
			call: func(_ int, _ string) (string, []byte) {
				return "write_file", []byte(`{}`)
			},
		},
		{
			name: "failed writes",
			call: func(_ int, dir string) (string, []byte) {
				missing := filepath.Join(dir, "does-not-exist.txt")
				return "edit_file", []byte(fmt.Sprintf(`{"file_path":%q,"old_string":"a","new_string":"b"}`, missing))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			turn := 0
			responder := func(llm.Request) llm.Response {
				turn++
				if turn%2 == 1 {
					name, args := tc.call((turn+1)/2, dir)
					return llm.Response{
						Model: "gpt-5.2",
						Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
							Kind:     llm.ContentToolCall,
							ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("call-%d", turn), Name: name, Arguments: args},
						}}},
						Finish: llm.FinishReason{Reason: "tool_use"},
					}
				}
				return agenttest.CommunicateResponse(true, "done")
			}
			sess, _, _ := newBudgetSession(t, SessionConfig{MaxToolRoundsPerInput: 5}, responder)
			store := sess.getOrCreateGoalStore()
			store.Set("ship it", sess.sclock().Now())

			continued := true
			for i := 0; continued && i < goal.NeverProgressedLimit+1; i++ {
				_, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
				if err != nil {
					t.Fatalf("continuation turn %d: %v", i, err)
				}
				if progressed {
					t.Fatalf("continuation turn %d credited progress for %s", i, tc.name)
				}
				_, continued = sess.armGoalContinuation(progressed, true)
			}
			snap, ok := store.Snapshot()
			if !ok || snap.Status != goal.StatusBlocked {
				t.Fatalf("goal after failed-mutation turns = %+v ok=%t, want blocked by the no-progress breaker", snap, ok)
			}
		})
	}
}

// A successful mutation must still reset the no-progress streak: the stricter
// signal must not break the intended two-tier allowance.
func TestGoalProgressCreditsAnExecutedSuccessfulMutation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	turn := 0
	responder := func(llm.Request) llm.Response {
		turn++
		if turn == 1 {
			args := []byte(fmt.Sprintf(`{"file_path":%q,"content":"sentinel"}`, filepath.Join(dir, "out.txt")))
			return llm.Response{
				Model: "gpt-5.2",
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: "write-1", Name: "write_file", Arguments: args},
				}}},
				Finish: llm.FinishReason{Reason: "tool_use"},
			}
		}
		return agenttest.CommunicateResponse(true, "done")
	}
	sess, _, _ := newBudgetSession(t, SessionConfig{MaxToolRoundsPerInput: 5}, responder)
	store := sess.getOrCreateGoalStore()
	store.Set("ship it", sess.sclock().Now())

	_, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
	if err != nil {
		t.Fatalf("continuation turn: %v", err)
	}
	if !progressed {
		t.Fatal("an executed successful write_file did not count as goal progress")
	}
	if _, continued := sess.armGoalContinuation(progressed, true); !continued {
		t.Fatal("a progressing goal should continue")
	}
	if snap, _ := store.Snapshot(); snap.NoProgressStreak != 0 {
		t.Fatalf("NoProgressStreak after a successful mutation = %d, want 0", snap.NoProgressStreak)
	}
}

// Long legitimate read-only investigation keeps the larger never-progressed
// allowance; once a real mutation lands, a short stall reaches the stricter
// limit. This pins the two-tier breaker the stricter signal must not skew.
func TestGoalTwoTierAllowanceSurvivesReadOnlyInvestigation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	readPath := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(readPath, []byte("read-only fixture"), 0o644); err != nil {
		t.Fatalf("write read fixture: %v", err)
	}
	writePath := filepath.Join(dir, "out.txt")
	var calls int
	responder := func(llm.Request) llm.Response {
		calls++
		turn := (calls - 1) / 2
		if calls%2 == 1 {
			name, args := "read_file", []byte(fmt.Sprintf(`{"file_path":%q}`, readPath))
			if turn == 4 {
				name, args = "write_file", []byte(fmt.Sprintf(`{"file_path":%q,"content":"done"}`, writePath))
			}
			return llm.Response{
				Model: "gpt-5.2",
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("call-%d", calls), Name: name, Arguments: args},
				}}},
				Finish: llm.FinishReason{Reason: "tool_use"},
			}
		}
		return agenttest.CommunicateResponse(true, "done")
	}
	sess, _, _ := newBudgetSession(t, SessionConfig{MaxToolRoundsPerInput: 5}, responder)
	store := sess.getOrCreateGoalStore()
	store.Set("ship it", sess.sclock().Now())

	for i := range 8 {
		_, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
		if err != nil {
			t.Fatalf("continuation turn %d: %v", i, err)
		}
		_, continued := sess.armGoalContinuation(progressed, true)
		switch i {
		case 3:
			snap, _ := store.Snapshot()
			if progressed || snap.Status != goal.StatusActive || snap.NoProgressStreak != 4 {
				t.Fatalf("after 4 read-only turns: progressed=%t snapshot=%+v, want active streak 4 under NeverProgressedLimit=%d", progressed, snap, goal.NeverProgressedLimit)
			}
		case 4:
			snap, _ := store.Snapshot()
			if !progressed || snap.NoProgressStreak != 0 {
				t.Fatalf("successful mutation: progressed=%t streak=%d, want progress and streak reset", progressed, snap.NoProgressStreak)
			}
		case 7:
			if continued {
				t.Fatal("goal continued past the stricter no-progress limit")
			}
		}
	}
	snap, _ := store.Snapshot()
	if snap.Status != goal.StatusBlocked || snap.NoProgressStreak != goal.NoProgressLimit {
		t.Fatalf("goal after a stall past the mutation = %+v, want blocked at streak %d", snap, goal.NoProgressLimit)
	}
}

// A shell that cannot launch comes back as a structured failed/start_failed
// result. That failed launch is not a successful mutation and must not reset the
// goal no-progress streak (CORE-04).
func TestGoalNoProgressBreakerIgnoresShellLaunchFailures(t *testing.T) {
	t.Parallel()
	var calls int
	responder := func(llm.Request) llm.Response {
		calls++
		if calls%2 == 1 {
			return llm.Response{
				Model: "gpt-5.2",
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("call-%d", calls), Name: "shell", Arguments: []byte(`{"command":"echo hi"}`)},
				}}},
				Finish: llm.FinishReason{Reason: "tool_use"},
			}
		}
		return agenttest.CommunicateResponse(true, "done")
	}
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: responder}
	client := llm.NewClient()
	client.Register(adapter)
	env := &launchFailingEnv{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{
		MaxToolRoundsPerInput: 5,
		clock:                 agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	store := sess.getOrCreateGoalStore()
	store.Set("ship it", sess.sclock().Now())

	for turn := 0; ; turn++ {
		_, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
		if err != nil {
			t.Fatalf("continuation turn %d: %v", turn, err)
		}
		if progressed {
			t.Fatalf("continuation turn %d credited a failed shell launch as progress", turn)
		}
		if _, continued := sess.armGoalContinuation(progressed, true); !continued {
			break
		}
		if turn > goal.NeverProgressedLimit {
			t.Fatal("goal did not reach the no-progress breaker")
		}
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusBlocked {
		t.Fatalf("goal after failed shell launches = %+v, want blocked by the no-progress breaker", snap)
	}
}

// A shell whose Wait fails comes back as a structured failed/wait_failed result
// through the complete-or-handle marshal path. That failed call is not a
// successful mutation and must not reset the goal no-progress streak.
func TestGoalNoProgressBreakerIgnoresShellWaitFailures(t *testing.T) {
	t.Parallel()
	var calls int
	responder := func(llm.Request) llm.Response {
		calls++
		if calls%2 == 1 {
			return llm.Response{
				Model: "gpt-5.2",
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: fmt.Sprintf("call-%d", calls), Name: "shell", Arguments: []byte(`{"command":"echo hi"}`)},
				}}},
				Finish: llm.FinishReason{Reason: "tool_use"},
			}
		}
		return agenttest.CommunicateResponse(true, "done")
	}
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: responder}
	client := llm.NewClient()
	client.Register(adapter)
	env := &waitFailingEnv{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{
		MaxToolRoundsPerInput: 5,
		clock:                 agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	store := sess.getOrCreateGoalStore()
	store.Set("ship it", sess.sclock().Now())

	for turn := 0; ; turn++ {
		_, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
		if err != nil {
			t.Fatalf("continuation turn %d: %v", turn, err)
		}
		if progressed {
			t.Fatalf("continuation turn %d credited a failed shell wait as progress", turn)
		}
		if _, continued := sess.armGoalContinuation(progressed, true); !continued {
			break
		}
		if turn > goal.NeverProgressedLimit {
			t.Fatal("goal did not reach the no-progress breaker")
		}
	}
	if snap, _ := store.Snapshot(); snap.Status != goal.StatusBlocked {
		t.Fatalf("goal after failed shell waits = %+v, want blocked by the no-progress breaker", snap)
	}
}
