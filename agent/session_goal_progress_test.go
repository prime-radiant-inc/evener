package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// toolResultsMadeProgress is the goal no-progress signal: only an executed,
// successful, progress-eligible result counts. Unknown names, read-only tools,
// the result/communicate tool, task_list, failed calls and prevalidation-only
// rejections all stay non-progress (CORE-04).
func TestToolResultsMadeProgressEligibility(t *testing.T) {
	t.Parallel()
	sess, _, _ := newBudgetSession(t, SessionConfig{}, nil)
	call := func(name string) llm.ToolCallData { return llm.ToolCallData{Name: name} }
	ok := tool.ExecResult{}

	tests := []struct {
		name    string
		calls   []llm.ToolCallData
		results []tool.ExecResult
		want    bool
	}{
		{"successful mutation", []llm.ToolCallData{call("write_file")}, []tool.ExecResult{ok}, true},
		{"read-only", []llm.ToolCallData{call("read_file")}, []tool.ExecResult{ok}, false},
		{"failed mutation", []llm.ToolCallData{call("write_file")}, []tool.ExecResult{{IsError: true}}, false},
		{"prevalidation-only rejection", []llm.ToolCallData{call("write_file")}, []tool.ExecResult{{IsError: true, PrevalOnly: true}}, false},
		{"unknown tool", []llm.ToolCallData{call("no_such_tool")}, []tool.ExecResult{ok}, false},
		{"result tool", []llm.ToolCallData{call(sess.resultToolName())}, []tool.ExecResult{ok}, false},
		{"task list", []llm.ToolCallData{call("task_list")}, []tool.ExecResult{ok}, false},
		{
			"one success among failures",
			[]llm.ToolCallData{call("write_file"), call("write_file")},
			[]tool.ExecResult{{IsError: true}, ok},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sess.toolResultsMadeProgress(tc.calls, tc.results); got != tc.want {
				t.Fatalf("toolResultsMadeProgress(%v) = %t, want %t", tc.calls, got, tc.want)
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
