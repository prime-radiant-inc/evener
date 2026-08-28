package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCloneThreadOwnsNestedMutableState(t *testing.T) {
	turnStarted, turnCompleted, turnDuration := int64(1), int64(2), int64(3)
	itemStarted, itemCompleted, itemDuration, itemExit := int64(4), int64(5), int64(6), int64(7)
	jobExit, runningFor, quietFor, delegateDuration := 8, int64(9), int64(10), int64(11)
	resumable, structuredValid := true, true
	failedToolCalls := 12
	original := Thread{
		Status:  ThreadStatus{ActiveFlags: []string{"active"}},
		GitInfo: &GitInfo{Branch: "main"},
		Turns: []Turn{{
			StartedAt:   &turnStarted,
			CompletedAt: &turnCompleted,
			DurationMS:  &turnDuration,
			Usage:       &EvenerUsage{InputTokens: 13},
			Error: &TurnError{
				CodexErrorInfo: json.RawMessage(`{"kind":"provider"}`),
				Cause:          &DiagnosticCause{Kind: "provider", Status: 500},
			},
			Items: []ThreadItem{{
				Images:       []InputItem{{Data: []byte("image"), Metadata: map[string]string{"key": "value"}}},
				OutputImages: []OutputImage{{Name: "output"}},
				StartedAt:    &itemStarted,
				CompletedAt:  &itemCompleted,
				DurationMS:   &itemDuration,
				ExitCode:     &itemExit,
				Raw:          json.RawMessage(`{"raw":true}`),
			}},
		}},
		Evener: EvenerThread{
			Diagnostics: &EvenerDiagnostics{
				MCP:        []EvenerMCPServerInfo{{Tools: []string{"tool"}}},
				HookEvents: []EvenerHookEventStatus{{Event: "hook", Count: 1}},
				Jobs:       []EvenerJobInfo{{Resumable: &resumable, ExitCode: &jobExit}},
				Delegates: []EvenerDelegateInfo{{
					RunningForMS:     &runningFor,
					QuietForMS:       &quietFor,
					DurationMS:       &delegateDuration,
					StructuredValid:  &structuredValid,
					Usage:            &EvenerUsage{OutputTokens: 14},
					Worktree:         &JobActivityWorktree{Branch: "feature"},
					Message:          json.RawMessage(`{"message":true}`),
					StructuredResult: json.RawMessage(`{"result":true}`),
					Warnings:         []string{"warning"},
				}},
			},
			Queue: QueueState{
				Preview:           []string{"preview"},
				IDs:               []string{"id"},
				ClientMutationIDs: []string{"mutation"},
				Texts:             []string{"text"},
			},
			PendingMutations: []PendingMutation{{
				Input:         []InputItem{{Data: []byte("pending"), Metadata: map[string]string{"pending": "value"}}},
				QueueEntryIDs: []string{"queued"},
			}},
			Tasks:                 &TaskAggregate{Total: 2, Done: 1},
			Goal:                  &GoalState{Status: "active", Iterations: 1},
			Usage:                 &EvenerUsage{TotalTokens: 15},
			FailedToolCalls:       &failedToolCalls,
			ReasoningEffortLevels: []string{"low", "high"},
			PendingEscalations:    []SandboxEscalationRequested{{EscalationID: "esc"}},
		},
	}

	clone := CloneThread(original)
	if !reflect.DeepEqual(original, clone) {
		t.Fatal("clone changed values while copying")
	}
	if clone.GitInfo == original.GitInfo || clone.Turns[0].StartedAt == original.Turns[0].StartedAt || clone.Turns[0].Usage == original.Turns[0].Usage || clone.Turns[0].Error.Cause == original.Turns[0].Error.Cause || clone.Turns[0].Items[0].StartedAt == original.Turns[0].Items[0].StartedAt || clone.Evener.Tasks == original.Evener.Tasks || clone.Evener.Goal == original.Evener.Goal || clone.Evener.Usage == original.Evener.Usage || clone.Evener.FailedToolCalls == original.Evener.FailedToolCalls {
		t.Fatal("top-level nested pointers were shared")
	}
	if clone.Evener.Diagnostics.Jobs[0].Resumable == original.Evener.Diagnostics.Jobs[0].Resumable || clone.Evener.Diagnostics.Jobs[0].ExitCode == original.Evener.Diagnostics.Jobs[0].ExitCode || clone.Evener.Diagnostics.Delegates[0].RunningForMS == original.Evener.Diagnostics.Delegates[0].RunningForMS || clone.Evener.Diagnostics.Delegates[0].Usage == original.Evener.Diagnostics.Delegates[0].Usage || clone.Evener.Diagnostics.Delegates[0].Worktree == original.Evener.Diagnostics.Delegates[0].Worktree {
		t.Fatal("nested mutable pointers were shared")
	}

	clone.Status.ActiveFlags[0] = "changed"
	clone.GitInfo.Branch = "changed"
	*clone.Turns[0].StartedAt = 101
	*clone.Turns[0].Usage = EvenerUsage{InputTokens: 102}
	*clone.Turns[0].Error.Cause = DiagnosticCause{Status: 501}
	clone.Turns[0].Error.CodexErrorInfo.(json.RawMessage)[0] = '['
	clone.Turns[0].Items[0].Images[0].Data[0] = 'X'
	clone.Turns[0].Items[0].Images[0].Metadata["key"] = "changed"
	*clone.Turns[0].Items[0].StartedAt = 107
	clone.Turns[0].Items[0].Raw[0] = '['
	*clone.Evener.Diagnostics.Jobs[0].Resumable = false
	*clone.Evener.Diagnostics.Jobs[0].ExitCode = 108
	*clone.Evener.Diagnostics.Delegates[0].RunningForMS = 103
	clone.Evener.Diagnostics.Delegates[0].Worktree.Branch = "changed"
	clone.Evener.Diagnostics.Delegates[0].Message[0] = '['
	clone.Evener.Diagnostics.Delegates[0].Warnings[0] = "changed"
	clone.Evener.Queue.Preview[0] = "changed"
	clone.Evener.PendingMutations[0].Input[0].Data[0] = 'X'
	clone.Evener.PendingMutations[0].Input[0].Metadata["pending"] = "changed"
	*clone.Evener.Tasks = TaskAggregate{Total: 104}
	*clone.Evener.Goal = GoalState{Status: "blocked"}
	*clone.Evener.Usage = EvenerUsage{TotalTokens: 105}
	*clone.Evener.FailedToolCalls = 106

	if original.Status.ActiveFlags[0] != "active" || original.GitInfo.Branch != "main" || *original.Turns[0].StartedAt != 1 || original.Turns[0].Usage.InputTokens != 13 {
		t.Fatal("thread state was changed through its clone")
	}
	if string(original.Turns[0].Error.CodexErrorInfo.(json.RawMessage)) != `{"kind":"provider"}` || original.Turns[0].Error.Cause.Status != 500 || original.Turns[0].Items[0].Images[0].Data[0] != 'i' || original.Turns[0].Items[0].Images[0].Metadata["key"] != "value" || *original.Turns[0].Items[0].StartedAt != 4 || original.Turns[0].Items[0].Raw[0] != '{' {
		t.Fatal("turn or item state was changed through its clone")
	}
	if *original.Evener.Diagnostics.Jobs[0].Resumable != true || *original.Evener.Diagnostics.Jobs[0].ExitCode != 8 || *original.Evener.Diagnostics.Delegates[0].RunningForMS != 9 || original.Evener.Diagnostics.Delegates[0].Worktree.Branch != "feature" || string(original.Evener.Diagnostics.Delegates[0].Message) != `{"message":true}` || original.Evener.Diagnostics.Delegates[0].Warnings[0] != "warning" {
		t.Fatal("diagnostic state was changed through its clone")
	}
	if original.Evener.Queue.Preview[0] != "preview" || original.Evener.PendingMutations[0].Input[0].Data[0] != 'p' || original.Evener.PendingMutations[0].Input[0].Metadata["pending"] != "value" || original.Evener.Tasks.Total != 2 || original.Evener.Goal.Status != "active" || original.Evener.Usage.TotalTokens != 15 || *original.Evener.FailedToolCalls != 12 {
		t.Fatal("evener state was changed through its clone")
	}
}
