//go:build serffuzz

package transcript

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func init() {
	fuzzCoverageUnion = replayTranscriptCoverage
}

func replayTranscriptCoverage(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	items := []appwire.ThreadItem{
		{ID: "sys-empty", Type: "systemMessage"},
		{ID: "sys", TurnID: "turn_1", Type: "systemMessage", Text: " system ", Description: "Policy"},
		{ID: "hook", TurnID: "turn_1", Type: "systemMessage", Text: "many\n words", Description: "Hook"},
		{ID: "user-empty", Type: "userMessage"},
		{ID: "user-image", TurnID: "turn_1", Type: "userMessage", Images: []appwire.InputItem{{Type: "image"}}},
		{ID: "user-images", TurnID: "turn_1", Type: "userMessage", Images: []appwire.InputItem{{Type: "image"}, {Type: "image"}}},
		{ID: "user", TurnID: "turn_1", Type: "userMessage", Text: "hello"},
		{ID: "reason", TurnID: "turn_1", Type: "reasoning"},
		{ID: "agent", TurnID: "turn_1", Type: "agentMessage", Text: "answer"},
		{ID: "tool", CallID: "call", TurnID: "turn_1", Type: "commandExecution", ToolName: "delegate", ArgumentsJSON: `{"task":"cover"}`, Raw: []byte(`{"started_job_id":"job","type":"delegate","status":"running","task":"cover","transcript_ref":"ref","origin_turn_id":"turn_1","origin_tool_call_id":"call","origin_item_id":"tool","total_bytes":3}`), StartedAt: ms(10)},
		{ID: "tool", CallID: "call", TurnID: "turn_1", Type: "commandExecution", ToolName: "delegate_send", ArgumentsJSON: `{}`, Output: `{"delegate_id":"delegate","latest_job_id":"job","status":"completed","output_bytes":5}`, StartedAt: ms(10), CompletedAt: ms(20)},
		{ID: "communicate", Type: "commandExecution", ToolName: "communicate", Output: "ok"},
	}

	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyUserMessageEcho(" ")
	r.ApplyUserMessageEcho("hello")
	for _, item := range items {
		r.ApplyThreadItem(item, TurnIndexFromID(item.TurnID), false)
		r.ApplyThreadItem(item, TurnIndexFromID(item.TurnID), true)
	}
	// Cover reconciliation shapes that require pre-existing streamed rows.
	streamed := NewTranscriptReducer([]ChatMessage{{Kind: MsgAssistant, Text: "prefix", TurnID: "turn_3", TurnIndex: 3}}, nil, nil)
	streamed.ApplyAgentMessageDelta("turn_3", "stream", " suffix")
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "stream", TurnID: "turn_3", Type: "agentMessage", Text: "replacement"}, 3, true)
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "fresh", TurnID: "turn_4", Type: "agentMessage", Text: "fresh"}, 4, false)
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "fresh", TurnID: "turn_4", Type: "agentMessage", Text: "done"}, 4, true)
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "tail", TurnID: "turn_4", Type: "agentMessage", Text: "tail"}, 4, true)
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "reason", Type: "reasoning"}, 0, false)
	streamed.ApplyThreadItem(appwire.ThreadItem{ID: "reason", Type: "reasoning"}, 0, true)

	broken := NewTranscriptReducer([]ChatMessage{
		{Kind: MsgUser, Text: "pending", Pending: true},
		{Kind: MsgUser, Text: "failed", Failed: true},
		{Kind: MsgUser, Text: "echo"},
		{Kind: MsgTool, ItemID: "nil-tool"},
		{Kind: MsgTool, ItemID: "merge-tool", ToolCallID: "merge-call", Tool: &ToolCallInfo{}},
	}, map[string]int{"after": 4}, map[string]int{"after": 4})
	broken.RemoveUserMessageEcho("echo")
	broken.ApplyThreadItem(appwire.ThreadItem{ID: "nil-tool", Type: "commandExecution"}, 0, false)
	broken.ApplyThreadItem(appwire.ThreadItem{ID: "merge-tool", CallID: "merge-call", Type: "commandExecution", ToolName: "read_file", ArgumentsJSON: `{}`, Raw: []byte(`{"x":1}`), Error: "bad"}, 0, false)
	broken.ApplyThreadItem(appwire.ThreadItem{ID: "merge-tool", CallID: "merge-call", Type: "commandExecution", ToolName: "read_file", ArgumentsJSON: `{}`, Output: "one\ntwo\nthree\nfour\nfive\nsix"}, 0, true)
	r.RemoveUserMessageEcho(" ")
	r.RemoveUserMessageEcho("missing")
	r.ApplyAgentMessageDelta("turn_2", "a", "")
	r.ApplyAgentMessageDelta("turn_2", "a", "one")
	r.ApplyAgentMessageDelta("turn_2", "a", " two")
	r.ResetAgentMessage("turn_2", "")
	r.ResetAgentMessage("turn_2", "missing")
	r.ResetAgentMessage("turn_2", "a")
	r.ApplyReasoningSummaryDelta("turn_2", "r1", "")
	r.ApplyReasoningSummaryDelta("turn_2", "r1", "thinking")
	r.ApplyReasoningSummaryDelta("turn_2", "r1", " more")
	r.ApplyReasoningSummaryDelta("turn_2", "r2", "next")
	r.FinalizeReasoning()
	r.ApplyToolOutputDelta("out", "")
	r.ApplyToolOutputDelta("out", "one")
	r.ApplyToolOutputDelta("out", " two")

	jobs := []appwire.SerfJobInfo{
		{},
		{JobID: "foreground", JobType: "shell", Status: "running"},
		{JobID: "delegate-job", JobType: "delegate", Status: "running", Task: "task"},
		{JobID: "delegate-job", JobType: "delegate", DelegateID: "d", Status: "completed", Reason: "done", Task: "task", TranscriptRef: "child", OriginTurnID: "turn_1", OriginToolCallID: "call", OriginItemID: "tool", OutputBytes: 9},
		{JobID: "background", JobType: "shell", Background: true, Status: "failed", Command: "sleep 1"},
	}
	for _, job := range jobs {
		r.ApplySerfJob(job)
	}
	nilInfo := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool}}, nil, nil)
	nilInfo.ApplySerfJob(appwire.SerfJobInfo{JobID: "x", JobType: "delegate"})
	ordinary := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, ItemID: "origin"}}, nil, nil)
	ordinary.ApplySerfJob(appwire.SerfJobInfo{JobID: "x", JobType: "unknown", OriginItemID: "origin"})
	matched := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, ItemID: "origin", Tool: &ToolCallInfo{Name: "delegate"}}}, nil, nil)
	matched.ApplySerfJob(appwire.SerfJobInfo{JobID: "x", JobType: "delegate", Status: "done", Task: "described", OriginItemID: "origin"})
	r.ApplyTieHeadline("", "headline", false)
	r.ApplyTieHeadline("delegate-job", "", false)
	r.ApplyTieHeadline("missing", "headline", false)
	r.ApplyTieHeadline("delegate-job", "headline", true)
	r.ApplyChildActivity("", "step")
	r.ApplyChildActivity("child", "")
	r.ApplyChildActivity("missing", "step")
	r.ApplyChildActivity("child", "step")
	r.ApplyChildActivity("child", "step")
	r.ApplyChildActivity("child", "another")

	for _, text := range []string{
		"plain",
		`<job-notification job_id="j" status="failed"></job-notification>`,
		`<job-notification job_id="j" event="done" exit_code="2">excerpt: nope</job-notification>`,
		`<job-notification job_id="j" status="completed">excerpt: {"data":{"status":"ok","test_summary":"a deliberately long test summary that exceeds sixty characters for clipping coverage","commit_hashes":["123456789abcdef"],"concerns":["a","b"]}}</job-notification>`,
		`<job-notification job_id="j">excerpt: {"data":{"status":"ok","commit_hashes":["short"],"concerns":["a"]}}</job-notification>`,
	} {
		ParseJobNotificationHeadline(text)
	}

	thread := appwire.Thread{Turns: []appwire.Turn{
		{ID: "turn_1", Status: appwire.TurnStatusCompleted, Items: items},
		{ID: "opaque", Status: appwire.TurnStatusFailed, Error: &appwire.TurnError{Message: "boom"}},
	}}
	MessagesFromThread(thread)
	ItemDuration(appwire.ThreadItem{})
	ItemDuration(appwire.ThreadItem{StartedAt: ms(2), CompletedAt: ms(1)})
	clipStr(" short ", 60)
	ImageItemsPlaceholder(nil)
	systemMessageItemText(appwire.ThreadItem{Text: "plain"})
	mergeThreadItemIntoToolInfo(nil, appwire.ThreadItem{}, false)
	mergeThreadItemIntoToolInfo(&ToolCallInfo{Name: "delegate", Description: "existing"}, appwire.ThreadItem{ToolName: "delegate", Raw: []byte(`{"job_id":"x"}`)}, false)
	mergeThreadItemIntoToolInfo(&ToolCallInfo{Description: "existing", Detail: "detail"}, appwire.ThreadItem{Output: "1\n2\n3\n4\n5\n6"}, true)
	subagentRunFromToolItem(appwire.ThreadItem{Output: `{"current_job_id":"x","total_bytes":4}`})
	subagentRunFromToolItem(appwire.ThreadItem{})

	// Invalid cached indices and identity combinations are valid replay inputs.
	invalid := NewTranscriptReducer([]ChatMessage{{Kind: MsgSystem}}, map[string]int{"bad": 9, "call": 9}, map[string]int{"bad": 9})
	invalid.ApplyReasoningSummaryDelta("", "bad", "x")
	invalid.ApplyAgentMessageDelta("", "bad", "x")
	invalid.ApplyThreadItem(appwire.ThreadItem{ID: "bad", CallID: "call", Type: "commandExecution"}, 0, false)
	invalid.ApplyThreadItem(appwire.ThreadItem{CallID: "missing", Type: "commandExecution"}, 0, false)

	// Exercise every subagent matching identity without synthetic controllers.
	identityRows := []ChatMessage{
		{Kind: MsgSystem},
		{Kind: MsgTool},
		{Kind: MsgTool, Tool: &ToolCallInfo{Name: "read_file"}},
		{Kind: MsgTool, ItemID: "oi", ToolCallID: "oc", Tool: &ToolCallInfo{Name: "delegate", Subagent: &SubagentRunInfo{JobID: "old", DelegateID: "d"}}},
	}
	identities := NewTranscriptReducer(identityRows, nil, nil)
	identities.ApplySerfJob(appwire.SerfJobInfo{JobID: "new", JobType: "delegate", DelegateID: "d"})
	identities.ApplySerfJob(appwire.SerfJobInfo{JobID: "old", JobType: "delegate"})
	identities.ApplySerfJob(appwire.SerfJobInfo{JobType: "delegate", DelegateID: "d"})
	identities.ApplySerfJob(appwire.SerfJobInfo{JobID: "by-item", JobType: "delegate", OriginItemID: "oi"})
	identities.ApplySerfJob(appwire.SerfJobInfo{JobID: "by-call", JobType: "delegate", OriginToolCallID: "oc"})

	// Boundary states are possible when replay begins from a persisted partial.
	emptyReasoning := NewTranscriptReducer(nil, nil, nil)
	emptyReasoning.activeReasoningIndex("")
	wrongReasoning := NewTranscriptReducer([]ChatMessage{{Kind: MsgReasoning, Done: true}}, nil, map[string]int{"done": 0})
	wrongReasoning.activeReasoningIndex("done")
	shifted := NewTranscriptReducer([]ChatMessage{{}, {}, {}}, map[string]int{"tool": 2}, map[string]int{"message": 2})
	shifted.shiftActiveIndicesAfterRemoval(0)
	noIDs := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{}}}, nil, nil)
	noIDs.ApplyThreadItem(appwire.ThreadItem{Type: "commandExecution", ToolName: "shell"}, 0, false)
	noIDs.ApplyThreadItem(appwire.ThreadItem{Type: "commandExecution", ToolName: "shell", Output: "done"}, 0, true)
	unmatchedJob := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{Name: "delegate", Subagent: &SubagentRunInfo{JobID: "other"}}}}, nil, nil)
	unmatchedJob.ApplySerfJob(appwire.SerfJobInfo{JobID: "plain"})
	terminalActivity := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{Subagent: &SubagentRunInfo{TranscriptRef: "terminal", Status: "done"}}}}, nil, nil)
	terminalActivity.ApplyChildActivity("terminal", "ignored")

	// Directly probe lookup fallbacks using only wire item identities.
	lookup := NewTranscriptReducer([]ChatMessage{
		{Kind: MsgUser, Text: "skip", Pending: true},
		{Kind: MsgUser, Text: "skip", PendingID: 1},
		{Kind: MsgAssistant, ItemID: "assistant", TurnID: "turn_1", TurnIndex: 1},
		{Kind: MsgTool, ItemID: "tool", ToolCallID: "call", TurnID: "turn_1", TurnIndex: 1, Tool: &ToolCallInfo{}},
	}, map[string]int{"wrong-turn": 3, "call": 3}, map[string]int{"wrong-kind": 0, "wrong-turn": 2})
	lookup.activeMessageIndex(appwire.ThreadItem{})
	lookup.activeMessageIndex(appwire.ThreadItem{ID: "wrong-kind"})
	lookup.activeMessageIndex(appwire.ThreadItem{ID: "wrong-turn", TurnID: "turn_2"})
	lookup.messageIndexByItemID("", MsgAssistant, "", 0)
	lookup.pendingUserEchoIndex("skip")
	lookup.activeToolIndex(appwire.ThreadItem{ID: "wrong-turn", TurnID: "turn_2"})
	lookup.activeToolIndex(appwire.ThreadItem{CallID: "call"})
	lookup.toolIndex(appwire.ThreadItem{ID: "none", CallID: "call"}, 0)
	lookupNoCache := NewTranscriptReducer(lookup.Messages(), nil, nil)
	lookupNoCache.toolIndex(appwire.ThreadItem{CallID: "call"}, 0)

	activeNil := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool}}, map[string]int{"nil": 0}, nil)
	activeNil.ApplyThreadItem(appwire.ThreadItem{ID: "nil", Type: "commandExecution"}, 0, false)
	activeMerge := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{}}}, map[string]int{"open": 0}, nil)
	activeMerge.ApplyThreadItem(appwire.ThreadItem{ID: "open", Type: "commandExecution"}, 0, false)

	runningActivity := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{Subagent: &SubagentRunInfo{TranscriptRef: "running", Status: "running"}}}}, nil, nil)
	runningActivity.ApplyChildActivity("running", "first")
	runningActivity.ApplyChildActivity("running", "first")

	delegateMatch := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, Tool: &ToolCallInfo{Name: "delegate", Subagent: &SubagentRunInfo{DelegateID: "same"}}}}, nil, nil)
	delegateMatch.ApplySerfJob(appwire.SerfJobInfo{DelegateID: "same", JobType: "delegate"})
	itemMatch := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, ItemID: "origin", Tool: &ToolCallInfo{Name: "delegate"}}}, nil, nil)
	itemMatch.ApplySerfJob(appwire.SerfJobInfo{JobID: "item", JobType: "delegate", OriginItemID: "origin"})
	callMatch := NewTranscriptReducer([]ChatMessage{{Kind: MsgTool, ToolCallID: "origin-call", Tool: &ToolCallInfo{Name: "delegate"}}}, nil, nil)
	callMatch.ApplySerfJob(appwire.SerfJobInfo{JobID: "call", JobType: "delegate", OriginToolCallID: "origin-call"})

	p1 := r.AppendPendingSteering("steer")
	p2 := r.AppendPendingUser("user")
	r.AppendPendingDrain(2)
	r.MarkPendingFailed(-1, "missing")
	r.MarkPendingFailed(p1, "failed")
	r.RemovePending(-1)
	r.RemovePending(p2)

	if len(r.Messages()) == 0 || r.ActiveTools() == nil || r.ActiveMessages() == nil {
		t.Fatal("coverage replay produced invalid reducer state")
	}
}
