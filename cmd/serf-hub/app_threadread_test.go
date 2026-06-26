package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/llm"
)

func TestThreadReadReconcilesDelegateRawWithTerminalJobstoreState(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusCompleted}
	rec := agent.HistoricalJobRecord{JobID: "job_A", DelegateID: "dlg_A", Type: "delegate", Status: "completed", Task: "inspect billing", TranscriptRef: "local:child", OriginToolCallID: "call_delegate", OutputBytes: 42}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != "completed" {
		t.Fatalf("item status=%q, want completed", got.Status)
	}
	if !strings.Contains(string(got.Raw), `"status":"completed"`) || !strings.Contains(string(got.Raw), `"output_bytes":42`) {
		t.Fatalf("raw after reconcile = %s", got.Raw)
	}
}

func TestThreadReadLeavesDelegateRawUnchangedWithoutJobstoreRecord(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_missing","delegate_id":"dlg_A","status":"running"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusInProgress}
	thread := appwire.Thread{Turns: []appwire.Turn{{ID: "turn_1", Items: []appwire.ThreadItem{item}}}}

	got := reconcileDelegateThreadItems(thread, map[string]agent.HistoricalJobRecord{})
	gotItem := got.Turns[0].Items[0]
	if gotItem.Status != appwire.TurnStatusInProgress || string(gotItem.Raw) != string(raw) {
		t.Fatalf("item changed without jobstore record: status=%q raw=%s", gotItem.Status, gotItem.Raw)
	}
}

func TestThreadReadReconciliationIgnoresMismatchedJobID(t *testing.T) {
	raw := json.RawMessage(`{"job_id":"job_A","status":"running"}`)
	item := appwire.ThreadItem{Type: "commandExecution", ID: "item_delegate", CallID: "call_delegate", ToolName: "delegate", Raw: raw, Status: appwire.TurnStatusInProgress}
	rec := agent.HistoricalJobRecord{JobID: "job_B", Type: "delegate", Status: "completed"}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != appwire.TurnStatusInProgress || string(got.Raw) != string(raw) {
		t.Fatalf("mismatched job id reconciled: status=%q raw=%s", got.Status, got.Raw)
	}
}

func TestPastThreadReadReconcilesDelegateRawWithTerminalJobstoreState(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "repo")
	parentID := "01PARENT"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions", parentID), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt:      now,
		UpdatedAt:      now,
		TurnCount:      1,
		OriginalPrompt: "inspect billing",
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{
		SessionID: parentID,
		CreatedAt: now,
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	runningRaw := json.RawMessage(`{"job_id":"job_A","delegate_id":"dlg_A","status":"running","task":"inspect billing","transcript_ref":"local:child"}`)
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_delegate", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_delegate",
				Name:       "delegate",
				Content:    string(runningRaw),
				ToolState:  runningRaw,
			},
		}}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	writeHistoricalJobLog(t, filepath.Join(stateDir, "sessions", parentID, "jobs.jsonl"), now, "job_A")

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := pastThreadForRead(hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + parentID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 || len(thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	item := thread.Turns[0].Items[0]
	if item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("status=%q, want completed", item.Status)
	}
	for _, want := range []string{`"status":"completed"`, `"output_bytes":42`, `"origin_tool_call_id":"call_delegate"`} {
		if !strings.Contains(string(item.Raw), want) {
			t.Fatalf("raw missing %s: %s", want, item.Raw)
		}
	}
}

func TestPastThreadReadProjectsThinkingFromTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "repo")
	sessionID := "01THINK"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions", sessionID), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "kimi",
		Model:     "kimi-for-coding",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "kimi",
		Model:     "kimi-for-coding",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "Let me reason about this."}},
			{Kind: llm.ContentText, Text: "Here is the answer."},
		}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := pastThreadForRead(hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	items := thread.Turns[0].Items
	if len(items) != 2 {
		t.Fatalf("expected reasoning + agentMessage, got %+v", items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me reason about this." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
	if items[1].Type != "agentMessage" || items[1].Text != "Here is the answer." {
		t.Fatalf("agent message item=%+v", items[1])
	}
}

func writeHistoricalJobLog(t *testing.T, path string, ts time.Time, jobID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	startedAt := ts.Format(time.RFC3339Nano)
	endedAt := ts.Add(time.Second).Format(time.RFC3339Nano)
	lines := []string{
		`{"kind":"job_started","seq":1,"ts":"` + startedAt + `","job_id":"` + jobID + `","type":"delegate","task":"inspect billing","owner_session_id":"01PARENT","visible_to_session_id":"01PARENT","delegate_id":"dlg_A","origin_tool_call_id":"call_delegate","started_at":"` + startedAt + `"}`,
		`{"kind":"job_session_assigned","seq":2,"ts":"` + startedAt + `","job_id":"` + jobID + `","transcript_ref":"local:child"}`,
		`{"kind":"job_finished","seq":3,"ts":"` + endedAt + `","job_id":"` + jobID + `","status":"completed","reason":"exit_zero","ended_at":"` + endedAt + `","output_bytes":42}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
