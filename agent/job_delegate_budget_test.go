package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/hooks"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func newDelegateBudgetTestSession(t *testing.T, adapter *fakeAdapter, cfg SessionConfig, workDir string) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	if workDir == "" {
		workDir = t.TempDir()
	}
	cfg.StateDir = packageFixtureTempDir(t, "delegate-budget-state-*")
	cfg.MaxSubagentDepth = 1
	cfg.NoProjectPrompts = true
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	return newSession(t, withClient(client), withDir(workDir), withConfig(cfg))
}

func toolRoundBudgetResponse(callID, filePath, assistantText string) llm.Response {
	args, _ := json.Marshal(map[string]any{"file_path": filePath})
	call := llm.ToolCallData{
		ID:        callID,
		Name:      "read_file",
		Arguments: args,
		Type:      "function",
	}
	return llm.Response{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: assistantText},
			{Kind: llm.ContentToolCall, ToolCall: &call},
		},
	}}
}

func createLifetimeExhaustedDelegate(t *testing.T) (*Session, *fakeAdapter, delegateResult) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return communicateWithDefaultOutput("must not be requested")
		},
	}}
	parent := newDelegateBudgetTestSession(t, adapter, SessionConfig{}, "")

	origTrack := delegateTrackPrepared
	delegateTrackPrepared = func(s *Session, prepared *preparedSubagentRun) error {
		child := prepared.sub.sess
		child.mu.Lock()
		child.cfg.MaxTurns = 1
		child.turns = 1
		child.mu.Unlock()
		return s.trackAndLaunchPreparedSubagent(prepared)
	}
	defer func() { delegateTrackPrepared = origTrack }()

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "attempt work beyond the lifetime turn budget",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	return parent, adapter, result
}

func TestDelegate_LifetimeBudgetExhaustionIsDurableAndNotResumable(t *testing.T) {
	parent, adapter, result := createLifetimeExhaustedDelegate(t)
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	rec := loadShellRecord(t, parent.jobManager, result.JobID)
	if rec.Status != jobstore.StatusExhausted || rec.Reason != "turn_budget_exhausted" {
		t.Fatalf("terminal state = %s/%q, want exhausted/turn_budget_exhausted", rec.Status, rec.Reason)
	}
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript_ref: %v", err)
	}
	child := parent.subagents.get(childID)
	if child == nil || child.sess == nil {
		t.Fatalf("retained child %q missing", childID)
	}
	if rec.ExhaustionBudget != string(exhaustedBudgetTurns) || rec.ExhaustionLimit != child.sess.cfg.MaxTurns {
		t.Fatalf("exhaustion metadata = %q/%d, want %q/%d", rec.ExhaustionBudget, rec.ExhaustionLimit, exhaustedBudgetTurns, child.sess.cfg.MaxTurns)
	}
	if rec.Resumable == nil || *rec.Resumable {
		t.Fatalf("lifetime resumable = %v, want false", rec.Resumable)
	}

	requestsBefore := len(adapter.Requests())
	sent := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         result.DelegateID,
		Message:        "try another turn",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if sent.Err == nil || !strings.Contains(sent.Err.Error(), "turn_budget_exhausted") {
		t.Fatalf("delegate_send result = %+v, want turn_budget_exhausted refusal", sent)
	}
	if requestsAfter := len(adapter.Requests()); requestsAfter != requestsBefore {
		t.Fatalf("child model requests = %d after lifetime resume refusal, want %d", requestsAfter, requestsBefore)
	}
	if jobs := parent.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != 1 {
		t.Fatalf("delegate jobs after rejected resume = %d, want 1", len(jobs))
	}
}

func TestDelegate_CommunicateNudgeBudgetExhaustionSkipsSubagentStopHook(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "subagent-stop-hook")
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response 1")} },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response 2")} },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response 3")} },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("bare response 4")} },
		func(llm.Request) llm.Response {
			t.Fatal("communicate nudge or SubagentStop continuation reached the model after the turn budget")
			return llm.Response{}
		},
	}}
	parent := newDelegateBudgetTestSession(t, adapter, SessionConfig{}, "")

	origTrack := delegateTrackPrepared
	delegateTrackPrepared = func(s *Session, prepared *preparedSubagentRun) error {
		child := prepared.sub.sess
		child.mu.Lock()
		child.cfg.MaxTurns = 1
		child.mu.Unlock()
		runner := hooks.NewRunner(nil, "")
		hookJSON := `{"decision":"block","reason":"continue after the stop hook"}`
		runner.Add(plugin.HookSubagentStop, plugin.RegisteredHook{
			Matcher: "*",
			Type:    "command",
			Command: "printf '%s' " + shellQuote(hookJSON) + " | tee " + shellQuote(marker),
			Timeout: 5,
		})
		child.hookRunner = runner
		return s.trackAndLaunchPreparedSubagent(prepared)
	}
	defer func() { delegateTrackPrepared = origTrack }()

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "stop without communicating at the lifetime turn limit",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	rec := loadShellRecord(t, parent.jobManager, result.JobID)
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript_ref: %v", err)
	}
	child := parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("retained child %q missing", childID)
	}
	child.mu.Lock()
	childStatus := child.status
	child.mu.Unlock()
	if childStatus != SubagentExhausted {
		t.Fatalf("child status = %q, want %q", childStatus, SubagentExhausted)
	}
	if rec.Status != jobstore.StatusExhausted || rec.Reason != "turn_budget_exhausted" {
		t.Fatalf("durable terminal = %s/%q, want exhausted/turn_budget_exhausted", rec.Status, rec.Reason)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SubagentStop hook ran after communicate nudge exhausted the budget: %v", err)
	}
	if got := len(adapter.Requests()); got != maxBareTextRetries+1 {
		t.Fatalf("model requests = %d, want %d before nudge rejection", got, maxBareTextRetries+1)
	}
}

func TestDelegate_ToolRoundBudgetExhaustionIsDurableAndResumable(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "evidence.txt"), []byte("tool evidence\n"), 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolRoundBudgetResponse("read-tool-budget", "evidence.txt", "partial before tool-round exhaustion")
		},
		func(llm.Request) llm.Response {
			return communicateWithDefaultOutput("resumed after tool-round exhaustion")
		},
	}}
	parent := newDelegateBudgetTestSession(t, adapter, SessionConfig{MaxToolRoundsPerInput: 1}, workDir)

	first := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "collect evidence until the tool-round limit",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate: %v", first.Err)
	}
	rec := loadShellRecord(t, parent.jobManager, first.JobID)
	if rec.Status != jobstore.StatusExhausted || rec.Reason != "tool_round_budget_exhausted" {
		t.Fatalf("terminal state = %s/%q, want exhausted/tool_round_budget_exhausted", rec.Status, rec.Reason)
	}
	if rec.ExhaustionBudget != string(exhaustedBudgetToolRounds) || rec.ExhaustionLimit != 1 {
		t.Fatalf("exhaustion metadata = %q/%d, want %q/1", rec.ExhaustionBudget, rec.ExhaustionLimit, exhaustedBudgetToolRounds)
	}
	if rec.Resumable == nil || !*rec.Resumable {
		t.Fatalf("tool-round resumable = %v, want true", rec.Resumable)
	}

	resumed := parent.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         first.DelegateID,
		Message:        "continue with a fresh input",
		OnIdle:         "start",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})
	if resumed.Err != nil {
		t.Fatalf("delegate_send after tool-round exhaustion: %v", resumed.Err)
	}
	if resumed.JobID == "" || resumed.JobID == first.JobID || resumed.ResumedFromJobID != first.JobID {
		t.Fatalf("resumed result = %+v, want a new job from %s", resumed, first.JobID)
	}
	resumedRec := loadShellRecord(t, parent.jobManager, resumed.JobID)
	if resumedRec.Status != jobstore.StatusCompleted || resumedRec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want completed on retained child transcript", resumedRec)
	}
}

func TestDelegate_BudgetExhaustionPreservesPartialOutputAndTranscript(t *testing.T) {
	const (
		assistantEvidence = "last assistant evidence before exhaustion"
		toolEvidence      = "durable tool evidence payload"
	)
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "evidence.txt"), []byte(toolEvidence+"\n"), 0o600); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return toolRoundBudgetResponse("read-durable-evidence", "evidence.txt", assistantEvidence)
		},
	}}
	parent := newDelegateBudgetTestSession(t, adapter, SessionConfig{MaxToolRoundsPerInput: 1}, workDir)

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task:           "preserve partial evidence",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	rec := loadShellRecord(t, parent.jobManager, result.JobID)
	if rec.Status != jobstore.StatusExhausted {
		t.Fatalf("record status = %q, want exhausted", rec.Status)
	}
	output, _, _, err := parent.jobManager.readOutput(rec.JobID, shellInlineOutputBytes)
	if err != nil {
		t.Fatalf("read exhausted delegate output: %v", err)
	}
	if !strings.Contains(output, assistantEvidence) {
		t.Fatalf("delegate output = %q, want assistant evidence %q", output, assistantEvidence)
	}
	if rec.TranscriptRef == "" || rec.TranscriptRef != result.TranscriptRef {
		t.Fatalf("transcript_ref = %q result=%q, want stable readable reference", rec.TranscriptRef, result.TranscriptRef)
	}
	_, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil {
		t.Fatalf("decode transcript_ref: %v", err)
	}
	transcriptPath := filepath.Join(parent.stateDir, sessionsSubdir, childID+".transcript.jsonl")
	_, entries, _, err := readTranscript(transcriptPath)
	if err != nil {
		t.Fatalf("read exhausted child transcript: %v", err)
	}
	var sawAssistant, sawToolEvidence bool
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind == schema.TurnAssistant && strings.Contains(turn.Message.Text(), assistantEvidence) {
			sawAssistant = true
		}
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && strings.Contains(fmt.Sprint(part.ToolResult.Content), toolEvidence) {
				sawToolEvidence = true
			}
		}
	}
	if !sawAssistant || !sawToolEvidence {
		t.Fatalf("transcript evidence assistant=%t tool=%t, want both readable", sawAssistant, sawToolEvidence)
	}
}

func attachExhaustedDelegateForTest(t *testing.T, parent, child *Session, result string, exhausted *budgetExhaustionError) (*subagent, *runningJob) {
	t.Helper()
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		status:  SubagentExhausted,
		result:  result,
		err:     exhausted,
		done:    make(chan struct{}),
		running: false,
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "exhausted delegate", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}
	return sub, run
}

func TestDelegate_ExhaustedFinishPersistFailureStaysFailedAcrossRetries(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	exhausted := &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 2, Resumable: false}
	sub, run := attachExhaustedDelegateForTest(t, parent, child, "partial result survives failed exhausted persist", exhausted)

	origAppend := parent.jobManager.appendEvent
	failuresRemaining := 3
	var attempted []jobstore.Status
	parent.jobManager.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventJobFinished {
			attempted = append(attempted, event.Status)
			if failuresRemaining > 0 {
				failuresRemaining--
				return errors.New("injected job_finished append failure")
			}
		}
		return origAppend(event)
	}
	var notifications []jobNotification
	parent.jobManager.enqueue = func(notification jobNotification) {
		notifications = append(notifications, notification)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	wantAttempted := []jobstore.Status{
		jobstore.StatusExhausted,
		jobstore.StatusFailed,
		jobstore.StatusFailed,
		jobstore.StatusFailed,
	}
	if len(attempted) != len(wantAttempted) {
		t.Fatalf("attempted terminal statuses = %v, want %v", attempted, wantAttempted)
	}
	for i := range wantAttempted {
		if attempted[i] != wantAttempted[i] {
			t.Fatalf("attempted terminal statuses = %v, want %v", attempted, wantAttempted)
		}
	}
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusFailed || rec.Reason != "exhausted_persist_failed" {
		t.Fatalf("durable terminal = %s/%q, want failed/exhausted_persist_failed", rec.Status, rec.Reason)
	}
	if len(notifications) != 1 || notifications[0].Status != string(jobstore.StatusFailed) || notifications[0].Reason != "exhausted_persist_failed" {
		t.Fatalf("terminal notifications = %+v, want one failed/exhausted_persist_failed", notifications)
	}
}

func TestDelegate_ExplicitParentStopPersistFailureRetriesParentStopOutcome(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	exhausted := &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 2, Resumable: false}
	sub, run := attachExhaustedDelegateForTest(t, parent, child, "partial result before parent stop", exhausted)
	parent.jobManager.mu.Lock()
	run.stopStatus = jobstore.StatusStopped
	run.stopReason = "stopped_by_parent"
	parent.jobManager.mu.Unlock()

	origAppend := parent.jobManager.appendEvent
	failed := false
	var attempted []jobstore.Status
	parent.jobManager.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventJobFinished {
			attempted = append(attempted, event.Status)
			if !failed {
				failed = true
				return errors.New("injected parent-stop job_finished append failure")
			}
		}
		return origAppend(event)
	}
	var notifications []jobNotification
	parent.jobManager.enqueue = func(notification jobNotification) {
		notifications = append(notifications, notification)
	}

	if err := parent.finalizeDelegate(run.rec.JobID, child.ID(), sub); err != nil {
		t.Fatalf("finalizeDelegate: %v", err)
	}
	wantAttempted := []jobstore.Status{jobstore.StatusStopped, jobstore.StatusStopped}
	if len(attempted) != len(wantAttempted) || attempted[0] != wantAttempted[0] || attempted[1] != wantAttempted[1] {
		t.Fatalf("attempted terminal statuses = %v, want %v", attempted, wantAttempted)
	}
	rec := loadShellRecord(t, parent.jobManager, run.rec.JobID)
	if rec.Status != jobstore.StatusStopped || rec.Reason != "stopped_by_parent" {
		t.Fatalf("durable terminal = %s/%q, want stopped/stopped_by_parent", rec.Status, rec.Reason)
	}
	if len(notifications) != 1 || notifications[0].Status != string(jobstore.StatusStopped) || notifications[0].Reason != "stopped_by_parent" {
		t.Fatalf("terminal notifications = %+v, want one stopped/stopped_by_parent", notifications)
	}
}

func TestDelegate_BudgetTruthfulnessLeavesExistingTerminalStatusesUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		stopStatus jobstore.Status
		stopReason string
		child      SubagentStatus
		wantStatus jobstore.Status
		wantReason string
	}{
		{name: "completed", child: SubagentCompleted, wantStatus: jobstore.StatusCompleted},
		{name: "failed", child: SubagentFailed, wantStatus: jobstore.StatusFailed},
		{name: "cancelled", child: SubagentCancelled, wantStatus: jobstore.StatusCancelled, wantReason: "stopped_by_parent"},
		{name: "explicit parent stopped", stopStatus: jobstore.StatusStopped, stopReason: "stopped_by_parent", child: SubagentCompleted, wantStatus: jobstore.StatusStopped, wantReason: "stopped_by_parent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotStatus, gotReason := resolveDelegateTerminalStatus(test.stopStatus, test.stopReason, test.child, nil)
			if gotStatus != test.wantStatus || gotReason != test.wantReason {
				t.Fatalf("terminal = %s/%q, want %s/%q", gotStatus, gotReason, test.wantStatus, test.wantReason)
			}
		})
	}
}
