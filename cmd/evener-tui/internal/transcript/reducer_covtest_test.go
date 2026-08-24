package transcript

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

// --- ApplyReasoningSummaryDelta: empty delta is no-op ---

func TestCovApplyReasoningSummaryDeltaEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyReasoningSummaryDelta("t1", "i1", "")
	if len(r.Messages()) != 0 {
		t.Fatalf("empty delta should not append, got %d", len(r.Messages()))
	}
}

// --- ApplyReasoningSummaryDelta: active reasoning with empty itemID ---

func TestCovActiveReasoningIndexEmptyItemID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if _, ok := r.activeReasoningIndex(""); ok {
		t.Fatal("empty itemID should return false")
	}
}

// --- ApplyThreadItem: systemMessage empty text ---

func TestCovApplyThreadItemSystemMessageEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{Type: "systemMessage", ID: "s1"}, 1, true)
	if len(r.Messages()) != 0 {
		t.Fatal("empty system message should not append")
	}
}

// --- ApplyThreadItem: systemMessage with description ---

func TestCovApplyThreadItemSystemMessageWithDescription(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "s1",
		Text:        "hello",
		Description: "Custom",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "Custom\nhello" {
		t.Fatalf("system message = %+v", msgs)
	}
}

// --- ApplyThreadItem: systemMessage with Hook description ---

func TestCovApplyThreadItemSystemMessageHook(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "s1",
		Text:        "hook fired  with  spaces",
		Description: "Hook",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "hook fired with spaces" {
		t.Fatalf("hook system message = %q", msgs[0].Text)
	}
}

// --- ApplyThreadItem: userMessage empty text and no images ---

func TestCovApplyThreadItemUserMessageEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{Type: "userMessage", ID: "u1"}, 1, true)
	if len(r.Messages()) != 0 {
		t.Fatal("empty user message should not append")
	}
}

// --- ApplyThreadItem: userMessage multiple images ---

func TestCovApplyThreadItemUserMessageMultipleImages(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "u1",
		Images: []appwire.InputItem{{Type: "image"}, {Type: "image"}, {Type: "image"}},
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "[3 images]" {
		t.Fatalf("multi-image user message = %+v", msgs)
	}
}

// --- ApplyThreadItem: userMessage reconcile with existing by itemID ---

func TestCovApplyThreadItemUserMessageReconcileByItemID(t *testing.T) {
	r := NewTranscriptReducer([]ChatMessage{{Kind: MsgUser, ItemID: "u1", Text: "old", TurnID: "turn_1", TurnIndex: 1}}, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "userMessage",
		ID:     "u1",
		TurnID: "turn_1",
		Text:   "new text",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "new text" {
		t.Fatalf("reconciled user message = %+v", msgs)
	}
}

// --- ApplyThreadItem: reasoning existing active not completed ---

func TestCovApplyThreadItemReasoningActiveNotCompleted(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// First, create a live reasoning
	r.ApplyReasoningSummaryDelta("turn_1", "think-1", "pondering")
	// Then ApplyThreadItem for same item, not completed
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "reasoning",
		ID:     "think-1",
		TurnID: "turn_1",
	}, 1, false)
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should still have 1 message, got %d", len(msgs))
	}
	if msgs[0].Done {
		t.Fatal("reasoning should not be done")
	}
}

// --- ApplyThreadItem: reasoning existing active completed ---

func TestCovApplyThreadItemReasoningActiveCompleted(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyReasoningSummaryDelta("turn_1", "think-1", "pondering")
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "reasoning",
		ID:     "think-1",
		TurnID: "turn_1",
	}, 1, true)
	msgs := r.Messages()
	if !msgs[0].Done {
		t.Fatal("reasoning should be done after completed")
	}
}

// --- ApplyThreadItem: reasoning new, completed ---

func TestCovApplyThreadItemReasoningNewCompleted(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "reasoning",
		ID:     "think-new",
		TurnID: "turn_1",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
	if !msgs[0].Done {
		t.Fatal("new completed reasoning should be done")
	}
}

// --- ApplyThreadItem: agentMessage with itemID match (completed) ---

func TestCovApplyThreadItemAgentMessageItemIDCompleted(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// Seed an active assistant message
	r.ApplyAgentMessageDelta("turn_1", "item-a", "partial")
	// Then ApplyThreadItem with same itemID, completed
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "item-a",
		TurnID: "turn_1",
		Text:   "full text",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "full text" {
		t.Fatalf("agent message = %+v", msgs)
	}
}

// --- ApplyThreadItem: agentMessage with active message match (not by itemID) ---

func TestCovApplyThreadItemAgentMessageActiveMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// Seed an active assistant message via delta (this remembers it)
	r.ApplyAgentMessageDelta("turn_1", "item-a", "partial")
	// ApplyThreadItem with same item but different text, not completed
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "item-a",
		TurnID: "turn_1",
		Text:   "replaced",
	}, 1, false)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "replaced" {
		t.Fatalf("agent message = %+v", msgs)
	}
}

// --- ApplyThreadItem: agentMessage trailing assistant same turn ---

func TestCovApplyThreadItemAgentMessageTrailingSameTurn(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgAssistant, Text: "old", TurnID: "turn_1", TurnIndex: 1}}
	r := NewTranscriptReducer(seed, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "",
		TurnID: "turn_1",
		Text:   "new",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "new" {
		t.Fatalf("trailing agent = %+v", msgs)
	}
}

// --- ApplyThreadItem: agentMessage new, not completed ---

func TestCovApplyThreadItemAgentMessageNewNotCompleted(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "item-new",
		TurnID: "turn_1",
		Text:   "hello",
	}, 1, false)
	msgs := r.Messages()
	if len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Fatalf("new agent message = %+v", msgs)
	}
	if _, ok := r.ActiveMessages()["item-new"]; !ok {
		t.Fatal("item-new should be in activeMessages")
	}
}

// --- ApplyThreadItem: agentMessage empty text ---

func TestCovApplyThreadItemAgentMessageEmptyText(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:   "agentMessage",
		ID:     "item-x",
		TurnID: "turn_1",
		Text:   "  ",
	}, 1, true)
	if len(r.Messages()) != 0 {
		t.Fatal("empty agent message should not append")
	}
}

// --- ApplyThreadItem: commandExecution with existing tool match by ID ---

func TestCovApplyThreadItemCommandExecutionExistingByID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// Create initial tool
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "tool-1",
		TurnID:   "turn_1",
		ToolName: "read_file",
		CallID:   "call-1",
	}, 1, false)
	// Update same tool
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "tool-1",
		TurnID:   "turn_1",
		ToolName: "read_file",
		CallID:   "call-1",
		Output:   "file contents",
	}, 1, true)
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
	if msgs[0].Tool == nil || !msgs[0].Tool.Done {
		t.Fatal("tool should be done")
	}
}

// --- ApplyThreadItem: commandExecution existing tool with nil Tool info ---
// When the active tool entry has a nil Tool (shouldn't normally happen), the
// code returns early from the "if info == nil" branch without updating.
func TestCovApplyThreadItemCommandExecutionExistingNilToolInfo(t *testing.T) {
	// Seed a MsgTool with nil Tool, but the activeToolIndex match will find it.
	// The toolIndex code path (not activeToolIndex) will find it by ID match
	// in the scan loop, but the "if info == nil" branch returns early.
	seed := []ChatMessage{{Kind: MsgTool, ItemID: "tool-1", TurnID: "turn_1", TurnIndex: 1}}
	activeTools := map[string]int{"tool-1": 0}
	r := NewTranscriptReducer(seed, activeTools, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "tool-1",
		TurnID:   "turn_1",
		ToolName: "read_file",
	}, 1, false)
	// The activeToolIndex match returns idx=0, but info is nil so it returns
	// early without creating a new message. The Tool stays nil.
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should still have 1 message, got %d", len(msgs))
	}
	if msgs[0].Tool != nil {
		t.Fatalf("tool with nil info should stay nil when matched via activeTools: %+v", msgs[0].Tool)
	}
}

// --- ApplyEvenerJob: non-background job is ignored ---

func TestCovApplyEvenerJobNonBackground(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{
		JobID:      "job-1",
		JobType:    "shell",
		Background: false,
	})
	if len(r.Messages()) != 0 {
		t.Fatal("non-background shell job should be ignored")
	}
}

// --- ApplyEvenerJob: background non-shell job is ignored ---

func TestCovApplyEvenerJobBackgroundNonShell(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{
		JobID:      "job-1",
		JobType:    "delegate",
		Background: true,
	})
	if len(r.Messages()) != 0 {
		t.Fatal("background non-shell job should be ignored")
	}
}

// --- ApplyEvenerJob: empty job is ignored ---

func TestCovApplyEvenerJobEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{})
	if len(r.Messages()) != 0 {
		t.Fatal("empty job should be ignored")
	}
}

// --- ApplyEvenerJob: background shell with task creates message ---

func TestCovApplyEvenerJobBackgroundShellWithTask(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{
		JobID:      "job-1",
		JobType:    "shell",
		Background: true,
		Task:       "run tests",
	})
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
	if msgs[0].Tool.Description != "run tests" {
		t.Fatalf("description = %q, want 'run tests'", msgs[0].Tool.Description)
	}
}

// --- ApplyEvenerJob: background shell with command (no task) ---

func TestCovApplyEvenerJobBackgroundShellNoTask(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{
		JobID:      "job-1",
		JobType:    "shell",
		Background: true,
		Command:    "echo hi",
	})
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
	if msgs[0].Tool.Description != "echo hi" {
		t.Fatalf("description = %q, want 'echo hi'", msgs[0].Tool.Description)
	}
}

// --- ApplyEvenerDelegate: empty delegateID ignored ---

func TestCovApplyEvenerDelegateEmptyDelegateID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{})
	if len(r.Messages()) != 0 {
		t.Fatal("empty delegateID should be ignored")
	}
}

// --- ApplyEvenerDelegate: match with existing lower projection revision ---

func TestCovApplyEvenerDelegateMatchLowerRevision(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// First delegate
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:         "dlg-1",
		ProjectionRevision: 2,
		Task:               "first",
	})
	// Second update with lower projection revision
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:         "dlg-1",
		ProjectionRevision: 1,
		LatestActivityAt:   "2025-01-01T00:00:00Z",
	})
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
}

// --- ApplyEvenerDelegate: match with higher projection revision ---

func TestCovApplyEvenerDelegateMatchHigherRevision(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:         "dlg-1",
		ProjectionRevision: 1,
		Task:               "first",
	})
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:         "dlg-1",
		ProjectionRevision: 2,
		Task:               "updated",
		Terminal:           true,
	})
	msgs := r.Messages()
	if len(msgs) != 1 {
		t.Fatalf("should have 1 message, got %d", len(msgs))
	}
	if msgs[0].Tool.Subagent.Task != "updated" {
		t.Fatalf("task = %q, want 'updated'", msgs[0].Tool.Subagent.Task)
	}
}

// --- ApplyTieHeadline: empty jobID ---

func TestCovApplyTieHeadlineEmptyJobID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("", "headline", false) {
		t.Fatal("empty jobID should return false")
	}
}

// --- ApplyTieHeadline: empty headline ---

func TestCovApplyTieHeadlineEmptyHeadline(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("job-1", "", false) {
		t.Fatal("empty headline should return false")
	}
}

// --- ApplyTieHeadline: no match ---

func TestCovApplyTieHeadlineNoMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyTieHeadline("job-1", "headline", false) {
		t.Fatal("no match should return false")
	}
}

// --- ApplyTieHeadline: match ---

func TestCovApplyTieHeadlineMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerJob(appwire.EvenerJobInfo{
		JobID:      "job-1",
		JobType:    "shell",
		Background: true,
		Task:       "run tests",
	})
	if !r.ApplyTieHeadline("job-1", "tests passed", false) {
		t.Fatal("should match existing job")
	}
	msgs := r.Messages()
	if msgs[0].Tool.Subagent.Headline != "tests passed" {
		t.Fatalf("headline = %q", msgs[0].Tool.Subagent.Headline)
	}
}

// --- ApplyChildActivity: empty ref ---

func TestCovApplyChildActivityEmptyRef(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("", "activity") {
		t.Fatal("empty ref should return false")
	}
}

// --- ApplyChildActivity: empty activity ---

func TestCovApplyChildActivityEmptyActivity(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("ref", "") {
		t.Fatal("empty activity should return false")
	}
}

// --- ApplyChildActivity: no match ---

func TestCovApplyChildActivityNoMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	if r.ApplyChildActivity("ref", "activity") {
		t.Fatal("no match should return false")
	}
}

// --- ApplyChildActivity: match terminal ---

func TestCovApplyChildActivityMatchTerminal(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:    "dlg-1",
		Task:          "work",
		TranscriptRef: "local:ref1",
		Terminal:      true,
	})
	// Terminal delegate should not be updated
	if r.ApplyChildActivity("local:ref1", "new activity") {
		t.Fatal("terminal delegate should not be updated by child activity")
	}
}

// --- ApplyChildActivity: match updates steps ---

func TestCovApplyChildActivityMatchUpdatesSteps(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:    "dlg-1",
		Task:          "work",
		TranscriptRef: "local:ref1",
	})
	if !r.ApplyChildActivity("local:ref1", "step 1") {
		t.Fatal("should match and update")
	}
	if !r.ApplyChildActivity("local:ref1", "step 2") {
		t.Fatal("should match and update again")
	}
	msgs := r.Messages()
	if msgs[0].Tool.Subagent.Steps != 2 {
		t.Fatalf("steps = %d, want 2", msgs[0].Tool.Subagent.Steps)
	}
	if msgs[0].Tool.Subagent.Activity != "step 2" {
		t.Fatalf("activity = %q, want 'step 2'", msgs[0].Tool.Subagent.Activity)
	}
}

// --- ApplyChildActivity: same activity does not increment steps ---

func TestCovApplyChildActivitySameActivityNoIncrement(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyEvenerDelegate(appwire.EvenerDelegateInfo{
		DelegateID:    "dlg-1",
		Task:          "work",
		TranscriptRef: "local:ref1",
	})
	r.ApplyChildActivity("local:ref1", "same")
	r.ApplyChildActivity("local:ref1", "same")
	msgs := r.Messages()
	if msgs[0].Tool.Subagent.Steps != 1 {
		t.Fatalf("steps = %d, want 1 (same activity)", msgs[0].Tool.Subagent.Steps)
	}
}

// --- subagentMessageIndex: match by originItemID ---

func TestCovSubagentMessageIndexByOriginItemID(t *testing.T) {
	seed := []ChatMessage{{
		Kind:   MsgTool,
		ItemID: "item-1",
		Tool:   &ToolCallInfo{Name: "delegate"},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	run := SubagentRunInfo{OriginItemID: "item-1"}
	idx, matched := r.subagentMessageIndex(run)
	if !matched {
		t.Fatal("should match by originItemID")
	}
	if idx != 0 {
		t.Fatalf("idx = %d, want 0", idx)
	}
}

// --- subagentMessageIndex: match by originToolCallID ---

func TestCovSubagentMessageIndexByOriginToolCallID(t *testing.T) {
	seed := []ChatMessage{{
		Kind:       MsgTool,
		ToolCallID: "call-1",
		Tool:       &ToolCallInfo{Name: "delegate"},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	run := SubagentRunInfo{OriginToolCallID: "call-1"}
	_, matched := r.subagentMessageIndex(run)
	if !matched {
		t.Fatal("should match by originToolCallID")
	}
}

// --- subagentMessageIndex: existing subagent with different jobID ---

func TestCovSubagentMessageIndexDifferentJobID(t *testing.T) {
	seed := []ChatMessage{{
		Kind: MsgTool,
		Tool: &ToolCallInfo{
			Name:     "shell",
			Subagent: &SubagentRunInfo{JobID: "job-A"},
		},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	// Run with a different JobID and no matching origin -> no match
	run := SubagentRunInfo{JobID: "job-B"}
	_, matched := r.subagentMessageIndex(run)
	if matched {
		t.Fatal("different jobID should not match")
	}
}

// --- stableDelegateMessageIndex: match by originItemID ---

func TestCovStableDelegateMessageIndexByOriginItemID(t *testing.T) {
	seed := []ChatMessage{{
		Kind:   MsgTool,
		ItemID: "item-1",
		Tool:   &ToolCallInfo{Name: "delegate"},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	run := SubagentRunInfo{DelegateID: "dlg-new", OriginItemID: "item-1"}
	_, matched := r.stableDelegateMessageIndex(run)
	if !matched {
		t.Fatal("should match by originItemID")
	}
}

// --- stableDelegateMessageIndex: match by originToolCallID ---

func TestCovStableDelegateMessageIndexByOriginToolCallID(t *testing.T) {
	seed := []ChatMessage{{
		Kind:       MsgTool,
		ToolCallID: "call-1",
		Tool:       &ToolCallInfo{Name: "delegate"},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	run := SubagentRunInfo{DelegateID: "dlg-new", OriginToolCallID: "call-1"}
	_, matched := r.stableDelegateMessageIndex(run)
	if !matched {
		t.Fatal("should match by originToolCallID")
	}
}

// --- stableDelegateMessageIndex: no match ---

func TestCovStableDelegateMessageIndexNoMatch(t *testing.T) {
	seed := []ChatMessage{{
		Kind: MsgTool,
		Tool: &ToolCallInfo{Name: "delegate"},
	}}
	r := NewTranscriptReducer(seed, nil, nil)
	run := SubagentRunInfo{DelegateID: "dlg-new"}
	_, matched := r.stableDelegateMessageIndex(run)
	if matched {
		t.Fatal("should not match")
	}
}

// --- pendingUserEchoIndex: not found (only failed or pending) ---

func TestCovPendingUserEchoIndexFailed(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgUser, Text: "hello", Failed: true}}
	r := NewTranscriptReducer(seed, nil, nil)
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatal("failed echo should not match")
	}
}

func TestCovPendingUserEchoIndexPending(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgUser, Text: "hello", Pending: true}}
	r := NewTranscriptReducer(seed, nil, nil)
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatal("pending echo should not match")
	}
}

func TestCovPendingUserEchoIndexPendingID(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgUser, Text: "hello", PendingID: 5}}
	r := NewTranscriptReducer(seed, nil, nil)
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatal("echo with PendingID should not match")
	}
}

func TestCovPendingUserEchoIndexWithItemID(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgUser, Text: "hello", ItemID: "i1"}}
	r := NewTranscriptReducer(seed, nil, nil)
	if _, ok := r.pendingUserEchoIndex("hello"); ok {
		t.Fatal("echo with ItemID should not match")
	}
}

// --- activeToolIndex: by CallID ---

func TestCovActiveToolIndexByCallID(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{}}}
	activeTools := map[string]int{"call-1": 0}
	r := NewTranscriptReducer(seed, activeTools, nil)
	item := appwire.ThreadItem{CallID: "call-1"}
	idx, ok := r.activeToolIndex(item)
	if !ok || idx != 0 {
		t.Fatalf("activeToolIndex by CallID = %d %v", idx, ok)
	}
}

// --- activeToolIndex: by ID with turn scope mismatch ---

func TestCovActiveToolIndexIDTurnScopeMismatch(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgTool, TurnID: "turn_1", TurnIndex: 1, Tool: &ToolCallInfo{}}}
	activeTools := map[string]int{"tool-1": 0}
	r := NewTranscriptReducer(seed, activeTools, nil)
	item := appwire.ThreadItem{ID: "tool-1", TurnID: "turn_2"}
	_, ok := r.activeToolIndex(item)
	if ok {
		t.Fatal("turn scope mismatch should not match")
	}
}

// --- toolIndex: scan by CallID ---

func TestCovToolIndexScanByCallID(t *testing.T) {
	seed := []ChatMessage{{Kind: MsgTool, ToolCallID: "call-1", Tool: &ToolCallInfo{}}}
	r := NewTranscriptReducer(seed, nil, nil)
	item := appwire.ThreadItem{CallID: "call-1"}
	idx, ok := r.toolIndex(item, 1)
	if !ok || idx != 0 {
		t.Fatalf("toolIndex scan by CallID = %d %v", idx, ok)
	}
}

// --- toolIndex: no match ---

func TestCovToolIndexNoMatch(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	_, ok := r.toolIndex(appwire.ThreadItem{ID: "nope"}, 1)
	if ok {
		t.Fatal("should not match")
	}
}

// --- MessagesFromThread: with failed turn error ---

func TestCovMessagesFromThreadFailedTurnError(t *testing.T) {
	thread := appwire.Thread{
		Turns: []appwire.Turn{{
			ID:     "turn_1",
			Status: appwire.TurnStatusFailed,
			Error:  &appwire.TurnError{Message: "turn failed"},
		}},
	}
	msgs := MessagesFromThread(thread)
	// Should contain a system message with the error
	found := false
	for _, m := range msgs {
		if m.Kind == MsgSystem {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("failed turn should produce a system message")
	}
}

// --- MessagesFromThread: with delegates ---

func TestCovMessagesFromThreadWithDelegates(t *testing.T) {
	thread := appwire.Thread{
		Evener: appwire.EvenerThread{
			Diagnostics: &appwire.EvenerDiagnostics{
				Delegates: []appwire.EvenerDelegateInfo{
					{DelegateID: "dlg-1", Task: "work"},
				},
			},
		},
	}
	msgs := MessagesFromThread(thread)
	found := false
	for _, m := range msgs {
		if m.Kind == MsgTool && m.Tool != nil && m.Tool.Subagent != nil && m.Tool.Subagent.DelegateID == "dlg-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("should have a delegate message")
	}
}

// --- mergeThreadItemIntoToolInfo: nil info ---

func TestCovMergeThreadItemIntoToolInfoNil(t *testing.T) {
	mergeThreadItemIntoToolInfo(nil, appwire.ThreadItem{}, true, "")
	// Should not panic
}

// --- mergeThreadItemIntoToolInfo: with delegate raw ---

func TestCovMergeThreadItemIntoToolInfoDelegate(t *testing.T) {
	info := &ToolCallInfo{Name: "other"}
	item := appwire.ThreadItem{
		ToolName: "delegate",
		Raw:      jsonRaw(`{"delegate_id":"dlg-1","task":"delegated work"}`),
	}
	mergeThreadItemIntoToolInfo(info, item, true, "")
	if info.Subagent == nil || info.Subagent.DelegateID != "dlg-1" {
		t.Fatalf("should parse delegate info: %+v", info.Subagent)
	}
}

// --- mergeThreadItemIntoToolInfo: with delegate_send and output fallback ---

func TestCovMergeThreadItemIntoToolInfoDelegateSendWithOutput(t *testing.T) {
	info := &ToolCallInfo{Name: "other"}
	item := appwire.ThreadItem{
		ToolName: "delegate_send",
		Output:   `{"delegate_id":"dlg-2"}`,
	}
	mergeThreadItemIntoToolInfo(info, item, true, "")
	if info.Subagent == nil || info.Subagent.DelegateID != "dlg-2" {
		t.Fatalf("should parse delegate_send from output: %+v", info.Subagent)
	}
}

// --- mergeThreadItemIntoToolInfo: done with detail ---

func TestCovMergeThreadItemIntoToolInfoDoneWithDetail(t *testing.T) {
	info := &ToolCallInfo{Name: "read_file", Description: "read"}
	item := appwire.ThreadItem{
		ToolName:      "read_file",
		ArgumentsJSON: `{"path":"/foo"}`,
		Output:        "contents",
	}
	mergeThreadItemIntoToolInfo(info, item, true, "")
	if !info.Done {
		t.Fatal("should be done")
	}
	if !info.Expanded {
		t.Fatal("should be expanded when detail is present")
	}
}

// --- SubagentDisplayStatus ---

func TestCovSubagentDisplayStatusEmpty(t *testing.T) {
	if got := SubagentDisplayStatus(SubagentRunInfo{}); got != "" {
		t.Fatalf("empty status = %q, want empty", got)
	}
}

func TestCovSubagentDisplayStatusNonTerminal(t *testing.T) {
	if got := SubagentDisplayStatus(SubagentRunInfo{Status: "running"}); got != "running" {
		t.Fatalf("running = %q, want running", got)
	}
}

func TestCovSubagentDisplayStatusTerminalWithOutcome(t *testing.T) {
	if got := SubagentDisplayStatus(SubagentRunInfo{Status: "running", Terminal: true, Outcome: "completed"}); got != "completed" {
		t.Fatalf("terminal with outcome = %q, want completed", got)
	}
}

func TestCovSubagentDisplayStatusTerminalNoOutcome(t *testing.T) {
	if got := SubagentDisplayStatus(SubagentRunInfo{Status: "running", Terminal: true}); got != "running" {
		t.Fatalf("terminal no outcome = %q, want running", got)
	}
}

// --- mergeLatestDelegateActivity ---

func TestCovMergeLatestDelegateActivityNil(t *testing.T) {
	mergeLatestDelegateActivity(nil, "2025-01-01T00:00:00Z")
	// Should not panic
}

func TestCovMergeLatestDelegateActivityEmptyIncoming(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2025-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "")
	// Should not update
	if existing.LatestActivityAt != "2025-01-01T00:00:00Z" {
		t.Fatalf("empty incoming should not change: %q", existing.LatestActivityAt)
	}
}

func TestCovMergeLatestDelegateActivityInvalidIncoming(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2025-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "invalid")
	if existing.LatestActivityAt != "2025-01-01T00:00:00Z" {
		t.Fatalf("invalid incoming should not change: %q", existing.LatestActivityAt)
	}
}

func TestCovMergeLatestDelegateActivityNewer(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2025-01-01T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "2025-01-02T00:00:00Z")
	if existing.LatestActivityAt != "2025-01-02T00:00:00Z" {
		t.Fatalf("newer incoming should update: %q", existing.LatestActivityAt)
	}
}

func TestCovMergeLatestDelegateActivityOlder(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "2025-01-02T00:00:00Z"}
	mergeLatestDelegateActivity(existing, "2025-01-01T00:00:00Z")
	if existing.LatestActivityAt != "2025-01-02T00:00:00Z" {
		t.Fatalf("older incoming should not update: %q", existing.LatestActivityAt)
	}
}

func TestCovMergeLatestDelegateActivityInvalidExisting(t *testing.T) {
	existing := &SubagentRunInfo{LatestActivityAt: "invalid"}
	mergeLatestDelegateActivity(existing, "2025-01-01T00:00:00Z")
	if existing.LatestActivityAt != "2025-01-01T00:00:00Z" {
		t.Fatalf("invalid existing with valid incoming should update: %q", existing.LatestActivityAt)
	}
}

// --- isDelegateToolName ---

func TestCovIsDelegateToolName(t *testing.T) {
	if !isDelegateToolName("delegate") {
		t.Fatal("delegate should be true")
	}
	if !isDelegateToolName("delegate_send") {
		t.Fatal("delegate_send should be true")
	}
	if !isDelegateToolName(" delegate ") {
		t.Fatal("whitespace-trimmed delegate should be true")
	}
	if isDelegateToolName("other") {
		t.Fatal("other should be false")
	}
}

// --- isBackgroundShellRun ---

func TestCovIsBackgroundShellRun(t *testing.T) {
	if !isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "shell"}) {
		t.Fatal("background shell should be true")
	}
	if !isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "SHELL"}) {
		t.Fatal("background SHELL (case-insensitive) should be true")
	}
	if isBackgroundShellRun(SubagentRunInfo{Background: false, JobType: "shell"}) {
		t.Fatal("non-background shell should be false")
	}
	if isBackgroundShellRun(SubagentRunInfo{Background: true, JobType: "delegate"}) {
		t.Fatal("background delegate should be false")
	}
}

// --- subagentTerminalStatus ---

func TestCovSubagentTerminalStatus(t *testing.T) {
	for _, s := range []string{"completed", "done", "failed", "cancelled", "stopped", "succeeded", "exhausted", "COMPLETED"} {
		if !subagentTerminalStatus(s) {
			t.Errorf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"running", "idle", "", "pending"} {
		if subagentTerminalStatus(s) {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

// --- firstNonEmptyString ---

func TestCovFirstNonEmptyString(t *testing.T) {
	if got := firstNonEmptyString("", "", "x"); got != "x" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmptyString("", "  ", ""); got != "" {
		t.Fatalf("all empty = %q", got)
	}
}

// --- systemMessageItemText ---

func TestCovSystemMessageItemTextEmpty(t *testing.T) {
	if got := systemMessageItemText(appwire.ThreadItem{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestCovSystemMessageItemTextNoDescription(t *testing.T) {
	if got := systemMessageItemText(appwire.ThreadItem{Text: "hello"}); got != "hello" {
		t.Fatalf("no description = %q", got)
	}
}

// --- userMessageItemText ---

func TestCovUserMessageItemTextEmptyNoImages(t *testing.T) {
	if got := userMessageItemText(appwire.ThreadItem{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestCovUserMessageItemTextWithText(t *testing.T) {
	if got := userMessageItemText(appwire.ThreadItem{Text: "hi"}); got != "hi" {
		t.Fatalf("with text = %q", got)
	}
}

// --- ImageItemsPlaceholder ---

func TestCovImageItemsPlaceholderEmpty(t *testing.T) {
	if got := ImageItemsPlaceholder(nil); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestCovImageItemsPlaceholderSingle(t *testing.T) {
	if got := ImageItemsPlaceholder([]appwire.InputItem{{}}); got != "[image]" {
		t.Fatalf("single = %q", got)
	}
}

// --- ApplyUserMessageEcho / RemoveUserMessageEcho ---

func TestCovRemoveUserMessageEchoEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.RemoveUserMessageEcho("")
	// Should not panic
}

func TestCovRemoveUserMessageEchoNotFound(t *testing.T) {
	r := NewTranscriptReducer([]ChatMessage{{Kind: MsgUser, Text: "other"}}, nil, nil)
	r.RemoveUserMessageEcho("hello")
	if len(r.Messages()) != 1 {
		t.Fatal("should not remove non-matching")
	}
}

// --- ApplyToolOutputDelta empty ---

func TestCovApplyToolOutputDeltaEmpty(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyToolOutputDelta("tool-1", "")
	if len(r.Messages()) != 0 {
		t.Fatal("empty delta should not append")
	}
}

// --- ResetAgentMessage ---

func TestCovResetAgentMessageEmptyItemID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ResetAgentMessage("turn_1", "")
	// Should not panic
}

func TestCovResetAgentMessageNotFound(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ResetAgentMessage("turn_1", "nonexistent")
	// Should not panic
}

func TestCovResetAgentMessageRemoves(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyAgentMessageDelta("turn_1", "item-a", "partial")
	r.ResetAgentMessage("turn_1", "item-a")
	if len(r.Messages()) != 0 {
		t.Fatal("should remove the message")
	}
}

// --- turnScopeMatches ---

func TestCovTurnScopeMatches(t *testing.T) {
	// Both have turn IDs, they match
	if !turnScopeMatches("t1", "t1", 1, 1) {
		t.Fatal("matching turn IDs should match")
	}
	// Both have turn IDs, they differ
	if turnScopeMatches("t1", "t2", 1, 2) {
		t.Fatal("differing turn IDs should not match")
	}
	// No turn IDs, same indices
	if !turnScopeMatches("", "", 1, 1) {
		t.Fatal("same indices should match")
	}
	// No turn IDs, existing 0
	if !turnScopeMatches("", "", 0, 5) {
		t.Fatal("existing 0 should match")
	}
	// No turn IDs, incoming 0
	if !turnScopeMatches("", "", 5, 0) {
		t.Fatal("incoming 0 should match")
	}
	// No turn IDs, different indices
	if turnScopeMatches("", "", 1, 2) {
		t.Fatal("different indices should not match")
	}
}

// helper
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
