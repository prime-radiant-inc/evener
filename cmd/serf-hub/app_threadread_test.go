package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

func requirePastThreadForRead(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.Thread, bool) {
	t.Helper()
	thread, found, err := pastThreadForRead(cfg, params)
	if err != nil {
		t.Fatalf("pastThreadForRead: %v", err)
	}
	return thread, found
}

func requirePastThreadReadResponse(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, bool) {
	t.Helper()
	resp, found, err := pastThreadReadResponse(cfg, params)
	if err != nil {
		t.Fatalf("pastThreadReadResponse: %v", err)
	}
	return resp, found
}

func requirePastThreadTurnsList(t testing.TB, cfg hubcore.WebConfig, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, bool) {
	t.Helper()
	resp, found, err := pastThreadTurnsList(cfg, params)
	if err != nil {
		t.Fatalf("pastThreadTurnsList: %v", err)
	}
	return resp, found
}

func requirePastEntryTurns(t testing.TB, entry hubcore.PastEntry) []appwire.Turn {
	t.Helper()
	turns, err := pastEntryTurns(entry)
	if err != nil {
		t.Fatalf("pastEntryTurns: %v", err)
	}
	return turns
}

func requirePastEntryThread(t testing.TB, cfg hubcore.WebConfig, entry hubcore.PastEntry, includeTurns bool) appwire.Thread {
	t.Helper()
	thread, err := pastEntryThread(cfg, entry, includeTurns)
	if err != nil {
		t.Fatalf("pastEntryThread: %v", err)
	}
	return thread
}

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

func TestHistoricalJob_ExhaustedIsTerminal(t *testing.T) {
	if !isTerminalHistoricalJobStatus("exhausted") {
		t.Fatal("exhausted historical job reported as non-terminal")
	}
	raw := json.RawMessage(`{"job_id":"job_exhausted","delegate_id":"dlg_exhausted","status":"running","task":"bounded work","transcript_ref":"local:child-exhausted"}`)
	item := appwire.ThreadItem{
		Type:     "commandExecution",
		ID:       "item_delegate",
		CallID:   "call_delegate",
		ToolName: "delegate",
		Raw:      raw,
		Status:   appwire.TurnStatusInProgress,
	}
	rec := agent.HistoricalJobRecord{
		JobID:         "job_exhausted",
		DelegateID:    "dlg_exhausted",
		Type:          "delegate",
		Status:        "exhausted",
		Reason:        "tool_round_budget_exhausted",
		Task:          "bounded work",
		TranscriptRef: "local:child-exhausted",
	}

	got := reconcileDelegateThreadItemForTest(item, rec)
	if got.Status != "exhausted" {
		t.Fatalf("item status = %q, want exhausted", got.Status)
	}
	for _, want := range []string{`"status":"exhausted"`, `"reason":"tool_round_budget_exhausted"`} {
		if !strings.Contains(string(got.Raw), want) {
			t.Fatalf("raw missing %s: %s", want, got.Raw)
		}
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
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	parentID := "02wMz5Txv1C3Hut0M8GCeB"
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
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + parentID, IncludeTurns: true})
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
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
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
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
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

func TestPastThreadReadProjectsToolResultOutputImages(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv733WHFsVy66SR"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "openai",
		Model:     "gpt-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := w.Append(schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_img", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID:     "call_img",
				Name:           "screenshot",
				Content:        "captured",
				ImageData:      png,
				ImageMediaType: "image/png",
			},
		}}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	thread, ok := requirePastThreadForRead(t, hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !ok {
		t.Fatal("past thread not found")
	}
	if len(thread.Turns) != 1 || len(thread.Turns[0].Items) != 1 {
		t.Fatalf("turns=%+v", thread.Turns)
	}
	item := thread.Turns[0].Items[0]
	wantSHA := imageSha(png)
	if len(item.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want one", item.OutputImages)
	}
	img := item.OutputImages[0]
	if img.Source != "tool-result" || img.Name != "screenshot" || img.MediaType != "image/png" || img.Size != int64(len(png)) || img.SHA != wantSHA || img.URL != "/s/"+sessionID+"/images/"+wantSHA {
		t.Fatalf("OutputImages[0]=%+v", img)
	}
}

// TestPastEntryTurns_StampsCostFromSessionModel verifies pastEntryTurns
// estimates each turn's Cost from the session's own recorded Model — usage
// alone (from the transcript) isn't enough to price a turn, since
// appwire.EstimateCost needs a model to look up catalog rates.
func TestPastEntryTurns_StampsCostFromSessionModel(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "01COST"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID,
		CreatedAt: now,
		ProfileID: "anthropic",
		Model:     "claude-opus-4-5",
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Here is the answer."}}},
		Usage:   llm.Usage{InputTokens: 100, OutputTokens: 50},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entry := hubcore.PastEntry{
		ID:       sessionID,
		Meta:     schema.SessionMeta{ID: sessionID, Model: "claude-opus-4-5"},
		StateDir: stateDir,
	}
	turns := requirePastEntryTurns(t, entry)
	var found bool
	for _, turn := range turns {
		if turn.Usage == nil {
			continue
		}
		found = true
		if !strings.HasPrefix(turn.Cost, "~$") {
			t.Fatalf("turn.Cost=%q, want ~$ prefix", turn.Cost)
		}
	}
	if !found {
		t.Fatalf("no turn with usage found: %+v", turns)
	}
}

func TestPastEntryThread_CarriesWorkMetrics(t *testing.T) {
	entry := hubcore.PastEntry{
		Meta: schema.SessionMeta{
			WorkMillis: 5000,
			CumulativeUsage: schema.CumulativeUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			},
		},
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Serf.WorkMillis != 5000 {
		t.Fatalf("thread.Serf.WorkMillis = %d, want 5000", thread.Serf.WorkMillis)
	}
	if thread.Serf.Usage == nil {
		t.Fatalf("thread.Serf.Usage = nil, want non-nil")
	}
	if thread.Serf.Usage.InputTokens != 100 || thread.Serf.Usage.OutputTokens != 50 || thread.Serf.Usage.TotalTokens != 150 {
		t.Fatalf("thread.Serf.Usage = %+v, want InputTokens=100 OutputTokens=50 TotalTokens=150", thread.Serf.Usage)
	}
	if thread.Serf.ActiveTurnStartedAt != 0 {
		t.Fatalf("thread.Serf.ActiveTurnStartedAt = %d, want 0 (ended session)", thread.Serf.ActiveTurnStartedAt)
	}
}

// TestPastEntryThread_CarriesCostTotal proves the past-entry hydrate stamps
// the session-level dollar total on SerfThread from the cumulative usage at
// the session model's price — the honest full-session figure, never a page
// of loaded turns — and honestly omits it when there is no usage or the model
// is uncataloged (the absent-vs-zero distinction).
func TestPastEntryThread_CarriesCostTotal(t *testing.T) {
	priced := hubcore.PastEntry{
		Meta: schema.SessionMeta{
			Model:           "claude-opus-4-5",
			CumulativeUsage: schema.CumulativeUsage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000},
		},
	}
	thread := requirePastEntryThread(t, hubcore.WebConfig{}, priced, false)
	if want := appwire.EstimateCost("claude-opus-4-5", thread.Serf.Usage); thread.Serf.Cost != want || want == "" {
		t.Fatalf("thread.Serf.Cost = %q, want non-empty %q", thread.Serf.Cost, want)
	}
	if !strings.HasPrefix(thread.Serf.Cost, "~$") {
		t.Fatalf("thread.Serf.Cost = %q, want ~$ prefix", thread.Serf.Cost)
	}

	noUsage := hubcore.PastEntry{Meta: schema.SessionMeta{Model: "claude-opus-4-5"}}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, noUsage, false); got.Serf.Cost != "" {
		t.Fatalf("no-usage thread.Serf.Cost = %q, want \"\" (absent)", got.Serf.Cost)
	}

	uncataloged := hubcore.PastEntry{
		Meta: schema.SessionMeta{
			Model:           "totally-unknown-model-xyz",
			CumulativeUsage: schema.CumulativeUsage{InputTokens: 100_000, OutputTokens: 20_000, TotalTokens: 120_000},
		},
	}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, uncataloged, false); got.Serf.Cost != "" {
		t.Fatalf("uncataloged-model thread.Serf.Cost = %q, want \"\" (absent, not ~$0.00)", got.Serf.Cost)
	}
}

// TestPastEntryThread_UnnamedSessionKeepsTheShortForm proves the wire Name
// field agrees with the rail row (kata kspb / hubcore.nodeTitle) rather than
// the bare 22-char ID SessionDisplayName falls back to when a session has
// neither a generated name nor a prompt. Name feeds the pane header
// (model.name || ref) and the browser tab title (threadName(ref) ?? ref) on
// the frontend, and the TUI's tree title (thread.Name) directly — all three
// showed the raw ID until this fix (kata b309); the rail alone was fixed by
// kspb, in a different function (nodeTitle) that never touches this wire
// object.
//
// The short-circuit is scoped to Name only, not Preview: Preview is this
// wire object's own full-text field (unaffected, still the raw-ID fallback,
// matching the live-thread path in appwire_runtime.go and the TUI's own
// Name-then-Preview-then-SessionID chain), and SessionDisplayName itself
// stays untouched because eight other callers want its bare-ID last resort
// (see nodeTitle's doc comment).
func TestPastEntryThread_UnnamedSessionKeepsTheShortForm(t *testing.T) {
	const id = "033vq9Kif27AzZgnbjr55t" // a real 22-char UUIDv7 base62 payload

	unnamed := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id}}
	thread := requirePastEntryThread(t, hubcore.WebConfig{}, unnamed, false)
	if want := hubcore.ShortID(id); thread.Name != want {
		t.Fatalf("thread.Name = %q, want %q", thread.Name, want)
	}
	if thread.Name == id {
		t.Fatalf("thread.Name = %q — the raw payload, which is what ShortID exists to avoid", thread.Name)
	}
	if thread.Preview != id {
		t.Fatalf("thread.Preview = %q, want the raw ID %q (Preview keeps the full-text fallback)", thread.Preview, id)
	}

	// A prompted-but-unnamed session has something better than the ID to
	// show, so it keeps the prompt verbatim rather than being shortened.
	prompted := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id, OriginalPrompt: "fix the login bug"}}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, prompted, false).Name; got != "fix the login bug" {
		t.Fatalf("thread.Name (prompted) = %q, want the prompt verbatim", got)
	}

	// A named session is untouched.
	named := hubcore.PastEntry{Meta: schema.SessionMeta{ID: id, Name: "Login bug fix"}}
	if got := requirePastEntryThread(t, hubcore.WebConfig{}, named, false).Name; got != "Login bug fix" {
		t.Fatalf("thread.Name (named) = %q, want %q", got, "Login bug fix")
	}
}

func runningSubagentProjectionConfig(t *testing.T) (hubcore.WebConfig, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-running-subagent-0000000000")
	now := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	parentID := "02wMz5Txv1C3Hut0M8GCeB"
	childID := "02wMz5Txv2enqVTitaig6F"
	for _, meta := range []schema.SessionMeta{
		{ID: parentID, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
		{ID: childID, CreatedAt: now, UpdatedAt: now, ParentSessionID: parentID, IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/serf"}},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:              rendezvous.Entry{PID: 1, SessionID: parentID},
		SessionID:          parentID,
		Status:             appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{childID},
	})
	return hubcore.WebConfig{Past: past, Roster: roster}, childID
}

func TestPastThreadReadProjectsRunningSubagentActive(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfig(t)
	thread, ok := requirePastThreadForRead(t, cfg, appwire.ThreadReadParams{Ref: "local:" + childID})
	if !ok {
		t.Fatal("running subagent not found in past index")
	}
	if thread.Status.Type != appwire.ThreadStatusActive {
		t.Fatalf("running subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
	}
}

func seedBoundedPastThread(t *testing.T) (hubcore.WebConfig, appwire.ThreadReadParams) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-bounded-0000000000")
	sessionID := "02wMz5Txv47YP64RR3B9YJ"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-5", TurnCount: 200,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/tmp/project"}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Input fixture, not a durability test: batch the writes so seeding the
	// 200-turn transcript does not pay one fsync per Append. Close still
	// flushes, so the transcript read back is byte-identical.
	w.SyncInterval = time.Hour
	for range 199 {
		if err := w.Append(schema.NewTurn(schema.TurnAssistant, llm.Assistant("saved turn"))); err != nil {
			t.Fatal(err)
		}
	}
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := w.Append(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_img", Name: "screenshot", Content: "captured", ImageData: png, ImageMediaType: "image/png"},
	}}}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{Past: idx}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, TurnLimit: 40}
}

// TestPastEntryThreadAdvertisesResumableCapabilities asserts a past/exited
// local thread advertises exactly the capabilities that actually succeed once
// qp94's auto-resume is in place (kata xr4x). The resume-and-retry mutations
// (compact, clear, change model, shutdown) plus the always-available ones
// (send, fork, goal, rename) are true; the turn-in-flight controls (steer,
// interrupt, queue) are false because a cold exited session has no active turn
// for them to act on.
func TestPastEntryThreadAdvertisesResumableCapabilities(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-repo-0000000000")
	sessionID := "02wMz5Txv5aIxgf9yVdd0N"
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5",
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/project"},
		CreatedAt: now,
		UpdatedAt: now,
		TurnCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	idx := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := idx.Rebuild(); err != nil {
		t.Fatal(err)
	}
	entry, ok := idx.Find(sessionID)
	if !ok {
		t.Fatal("past entry not found")
	}
	thread := requirePastEntryThread(t, hubcore.WebConfig{Past: idx}, entry, false)
	caps := thread.Serf.Capabilities

	want := appwire.ThreadCapabilities{
		Send:         true,
		ForkFromTurn: true,
		Compact:      true,
		ChangeModel:  true,
		Shutdown:     true,
		Goal:         true,
		Rename:       true,
		// Steer, Interrupt, Queue stay false: turn-in-flight controls with no
		// active turn on a cold exited session.
	}
	if caps != want {
		t.Fatalf("past thread capabilities:\n got  %+v\n want %+v", caps, want)
	}
}

func TestPastThreadReadUsesBoundedSavedTranscript(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	full, ok := requirePastThreadForRead(t, cfg, params)
	if !ok || len(full.Turns) != 200 {
		t.Fatalf("full saved thread found=%v turns=%d, want true/200", ok, len(full.Turns))
	}
	wantTurns, wantCursor := appwire.WindowTurns(full.Turns, params.TurnLimit)

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
	t.Cleanup(restore)
	got, ok := requirePastThreadReadResponse(t, cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	if !reflect.DeepEqual(got.Thread.Turns, wantTurns) || got.OlderCursor != wantCursor {
		t.Fatal("bounded saved read differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{40}) {
		t.Fatalf("saved read used legacy full projection of 200 turns; bounded projection reports = %v, want [40]", projected)
	}
	last := got.Thread.Turns[len(got.Thread.Turns)-1].Items[0]
	if len(last.OutputImages) != 1 || last.OutputImages[0].Name != "screenshot" {
		t.Fatalf("bounded saved projection lost embedded output image: %+v", last)
	}
}

func TestPastThreadTranscriptReadersPropagateUnsupportedFormat(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	entry, ok := pastEntryForRead(cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	path := filepath.Join(entry.StateDir, "sessions", entry.Meta.ID+".transcript.jsonl")
	if err := os.WriteFile(path, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, found, err := pastThreadReadResponse(cfg, params)
	if !found || !errors.Is(err, transcript.ErrUnsupportedFormat) || resp.Thread.Turns != nil {
		t.Fatalf("past thread/read = (%+v, %v, %v), want found empty ErrUnsupportedFormat", resp, found, err)
	}
	page, found, err := pastThreadTurnsList(cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, Limit: 1})
	if !found || !errors.Is(err, transcript.ErrUnsupportedFormat) || page.Data != nil {
		t.Fatalf("past thread/turns/list = (%+v, %v, %v), want found empty ErrUnsupportedFormat", page, found, err)
	}
}

func TestPastThreadTurnsListUsesBoundedSavedTranscript(t *testing.T) {
	cfg, params := seedBoundedPastThread(t)
	full, ok := requirePastThreadForRead(t, cfg, params)
	if !ok {
		t.Fatal("past thread not found")
	}
	_, cursor := appwire.WindowTurns(full.Turns, params.TurnLimit)
	want := appwire.PageTurns(full.Turns, cursor, 30)

	var projected []int
	restore := apptranscript.InstallReadObserverForTesting(func(stats apptranscript.ReadStats) { projected = append(projected, stats.ProjectedTurns) })
	t.Cleanup(restore)
	got, ok := requirePastThreadTurnsList(t, cfg, appwire.ThreadTurnsListParams{Ref: params.Ref, Cursor: cursor, Limit: 30})
	if !ok {
		t.Fatal("past thread not found")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("bounded saved page differs from full reference")
	}
	if !reflect.DeepEqual(projected, []int{30}) {
		t.Fatalf("saved page used legacy full projection of 200 turns; bounded projection reports = %v, want [30]", projected)
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
		`{"kind":"job_started","seq":1,"ts":"` + startedAt + `","job_id":"` + jobID + `","type":"delegate","task":"inspect billing","owner_session_id":"02wMz5Txv1C3Hut0M8GCeB","visible_to_session_id":"02wMz5Txv1C3Hut0M8GCeB","delegate_id":"dlg_A","origin_tool_call_id":"call_delegate","started_at":"` + startedAt + `"}`,
		`{"kind":"job_session_assigned","seq":2,"ts":"` + startedAt + `","job_id":"` + jobID + `","transcript_ref":"local:child"}`,
		`{"kind":"job_finished","seq":3,"ts":"` + endedAt + `","job_id":"` + jobID + `","status":"completed","reason":"exit_zero","ended_at":"` + endedAt + `","output_bytes":42}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStampSessionImageURLsIsTheOnlyAuthorityForTheSHARoute pins the three
// rules the sha-addressed route depends on. Producers (the agent live, the
// transcript projector on reload) mint sha-only descriptors and never a URL;
// this is where the route is decided, once, for both.
func TestStampSessionImageURLsIsTheOnlyAuthorityForTheSHARoute(t *testing.T) {
	sha := strings.Repeat("a", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{
		{Source: "tool-result", SHA: sha},
		{Source: "read-file", SHA: sha, URL: "/doc/image?session=s&path=shot.png"},
		{Source: "shell-path", Path: "shot.png"},
	}}}}}
	stampSessionImageURLs("proj/one", turns)
	got := turns[0].Items[0].OutputImages
	if got[0].URL != "/s/proj%2Fone/images/"+sha {
		t.Errorf("sha-only descriptor URL=%q, want the escaped sha route", got[0].URL)
	}
	if got[1].URL != "/doc/image?session=s&path=shot.png" {
		t.Errorf("already-routed descriptor URL=%q, want it left alone", got[1].URL)
	}
	if got[2].URL != "" {
		t.Errorf("sha-less descriptor URL=%q, want no route invented for it", got[2].URL)
	}
}

func TestStampSessionImageURLsLeavesDescriptorsAloneWithoutASession(t *testing.T) {
	sha := strings.Repeat("b", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{{SHA: sha}}}}}}
	stampSessionImageURLs("", turns)
	if url := turns[0].Items[0].OutputImages[0].URL; url != "" {
		t.Fatalf("URL=%q, want no route stamped when the session is unknown", url)
	}
}

// TestStampSessionImageURLsCoversReplayedInputImages pins kata ck8z: a
// replayed user-attached image reaches the wire with only metadata sha/size
// (projectReplayInputImage strips the bytes), and handleSessionImage serves
// exactly that sha back — so the stamping pass must put the fetchable route
// on the item, not leave the client to reconstruct it from metadata.
func TestStampSessionImageURLsCoversReplayedInputImages(t *testing.T) {
	sha := strings.Repeat("d", 64)
	turns := []appwire.Turn{{Items: []appwire.ThreadItem{{Images: []appwire.InputItem{
		{Type: "image", Metadata: map[string]string{"sha": sha, "size": "78"}},
		{Type: "image", URL: "/doc/image?session=s&path=shot.png", Metadata: map[string]string{"sha": sha}},
		{Type: "image", Name: "inline.png"},
	}}}}}
	stampSessionImageURLs("proj/one", turns)
	got := turns[0].Items[0].Images
	if got[0].URL != "/s/proj%2Fone/images/"+sha {
		t.Errorf("sha-metadata image URL=%q, want the escaped sha route", got[0].URL)
	}
	if got[1].URL != "/doc/image?session=s&path=shot.png" {
		t.Errorf("already-routed image URL=%q, want it left alone", got[1].URL)
	}
	if got[2].URL != "" {
		t.Errorf("sha-less image URL=%q, want no route invented for it", got[2].URL)
	}
}

// TestStampThreadImageURLsFallsBackToThreadID mirrors how the file-backed
// enrichment resolves a thread's session: SessionID first, thread ID second.
func TestStampThreadImageURLsFallsBackToThreadID(t *testing.T) {
	sha := strings.Repeat("c", 64)
	thread := stampThreadImageURLs(appwire.Thread{
		ID:    "02wMz5Txv733WHFsVy66SR",
		Turns: []appwire.Turn{{Items: []appwire.ThreadItem{{OutputImages: []appwire.OutputImage{{SHA: sha}}}}}},
	})
	if url := thread.Turns[0].Items[0].OutputImages[0].URL; url != "/s/02wMz5Txv733WHFsVy66SR/images/"+sha {
		t.Fatalf("URL=%q, want the sha route built from the thread id", url)
	}
}
