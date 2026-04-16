package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func testSnapshot() SessionSnapshot {
	return SessionSnapshot{
		ID:        "01JTEST000000000000000001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: SessionConfig{
			MaxToolRoundsPerInput: 200,
			ReasoningEffort:       "high",
		},
		EnvInfo: EnvironmentInfo{
			WorkingDir: "/tmp/test",
			Platform:   "linux",
			IsGitRepo:  true,
			GitBranch:  "main",
		},
		History: []Turn{
			{Kind: TurnUserInput, Message: llm.User("hello")},
			{Kind: TurnAssistant, Message: llm.Assistant("hi there")},
		},
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
}

func TestSessionSnapshot_JSONRoundTrip(t *testing.T) {
	orig := testSnapshot()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SessionSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID {
		t.Fatalf("id: got %q want %q", got.ID, orig.ID)
	}
	if got.ProfileID != orig.ProfileID {
		t.Fatalf("profile_id: got %q want %q", got.ProfileID, orig.ProfileID)
	}
	if got.Model != orig.Model {
		t.Fatalf("model: got %q want %q", got.Model, orig.Model)
	}
	if len(got.History) != len(orig.History) {
		t.Fatalf("history length: got %d want %d", len(got.History), len(orig.History))
	}
	if got.History[0].Kind != TurnUserInput {
		t.Fatalf("history[0].kind: got %q want %q", got.History[0].Kind, TurnUserInput)
	}
	if got.History[1].Message.Text() != "hi there" {
		t.Fatalf("history[1].text: got %q want %q", got.History[1].Message.Text(), "hi there")
	}
	if got.TurnCount != 2 {
		t.Fatalf("turn_count: got %d want %d", got.TurnCount, 2)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("created_at: got %v want %v", got.CreatedAt, orig.CreatedAt)
	}
}

func TestSaveSession_CreatesFileAtomically(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()

	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// File should exist at sessions/<id>.json
	path := filepath.Join(dir, "sessions", snap.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got SessionSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != snap.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, snap.ID)
	}

	// No .tmp files should remain.
	entries, _ := os.ReadDir(filepath.Join(dir, "sessions"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file not cleaned up: %s", e.Name())
		}
	}
}

func TestSaveSession_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()

	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession first: %v", err)
	}

	// Update and save again.
	snap.TurnCount = 10
	snap.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)
	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession second: %v", err)
	}

	loaded, err := LoadSession(dir, snap.ID)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if loaded.TurnCount != 10 {
		t.Fatalf("turn_count after overwrite: got %d want 10", loaded.TurnCount)
	}
}

func TestLoadSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSession(dir, "NONEXISTENT")
	if err == nil {
		t.Fatalf("expected error for nonexistent session")
	}
}

func TestLoadSession_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "CORRUPT.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := LoadSession(dir, "CORRUPT")
	if err == nil {
		t.Fatalf("expected error for corrupt JSON")
	}
}

func TestListSessions_SortedByUpdatedAt(t *testing.T) {
	dir := t.TempDir()

	snap1 := testSnapshot()
	snap1.ID = "01JTEST000000000000000001"
	snap1.UpdatedAt = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	snap2 := testSnapshot()
	snap2.ID = "01JTEST000000000000000002"
	snap2.UpdatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	snap3 := testSnapshot()
	snap3.ID = "01JTEST000000000000000003"
	snap3.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	for _, s := range []SessionSnapshot{snap1, snap2, snap3} {
		if err := SaveSession(dir, s); err != nil {
			t.Fatalf("SaveSession %s: %v", s.ID, err)
		}
	}

	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list length: got %d want 3", len(list))
	}
	// Most recently updated first.
	if list[0].ID != snap2.ID {
		t.Fatalf("list[0].id: got %q want %q", list[0].ID, snap2.ID)
	}
	if list[1].ID != snap3.ID {
		t.Fatalf("list[1].id: got %q want %q", list[1].ID, snap3.ID)
	}
	if list[2].ID != snap1.ID {
		t.Fatalf("list[2].id: got %q want %q", list[2].ID, snap1.ID)
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestListSessions_SkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()

	// Save a valid snapshot.
	snap := testSnapshot()
	if err := SaveSession(dir, snap); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	// Add a corrupt file.
	sessDir := filepath.Join(dir, "sessions")
	if err := os.WriteFile(filepath.Join(sessDir, "CORRUPT.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	list, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 valid session, got %d", len(list))
	}
	if list[0].ID != snap.ID {
		t.Fatalf("id: got %q want %q", list[0].ID, snap.ID)
	}
}

// snapshotFakeAdapter is a minimal adapter for RestoreSession tests.
type snapshotFakeAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
}

func (a *snapshotFakeAdapter) Name() string { return a.name }
func (a *snapshotFakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	return wrapCommunicateResponse(llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("restored response")}), nil
}
func (a *snapshotFakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, errors.New("not implemented")
}
func (a *snapshotFakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

func TestRestoreSession_RestoresHistoryAndID(t *testing.T) {
	dir := t.TempDir()
	snap := testSnapshot()
	snap.EnvInfo.WorkingDir = dir

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), snap, "")
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	defer sess.Close()

	// Session should have the snapshot's ID.
	if sess.ID() != snap.ID {
		t.Fatalf("id: got %q want %q", sess.ID(), snap.ID)
	}

	// Send a new input and verify it includes the restored history.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(ctx, "continue please")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(result) != "restored response" {
		t.Fatalf("result: %q", result)
	}

	// The LLM request should contain the restored history turns.
	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	// Messages should be: system + restored history (user "hello", assistant "hi there") + new user "continue please"
	msgs := reqs[0].Messages
	// Find the user messages.
	var userTexts []string
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}
	if len(userTexts) < 2 {
		t.Fatalf("expected at least 2 user messages (restored + new), got %d: %v", len(userTexts), userTexts)
	}
	if userTexts[0] != "hello" {
		t.Fatalf("first user message: got %q want %q", userTexts[0], "hello")
	}
	if userTexts[len(userTexts)-1] != "continue please" {
		t.Fatalf("last user message: got %q want %q", userTexts[len(userTexts)-1], "continue please")
	}
}

func TestSession_AutoSave_WritesMetaAfterProcessInput(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events to prevent channel blocking.
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = sess.ProcessInput(ctx, "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// A meta file should exist (not a full snapshot).
	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 saved session meta, got %d", len(list))
	}
	meta := list[0]
	if meta.ID != sess.ID() {
		t.Fatalf("saved id: got %q want %q", meta.ID, sess.ID())
	}
	if meta.ProfileID != "openai" {
		t.Fatalf("profile_id: got %q want %q", meta.ProfileID, "openai")
	}

	// History should be in the transcript, not the meta.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 transcript entries, got %d", len(entries))
	}
	if entries[0].Turn.Kind != TurnUserInput {
		t.Fatalf("entry[0].kind: got %q want %q", entries[0].Turn.Kind, TurnUserInput)
	}
	if entries[1].Turn.Kind != TurnAssistant {
		t.Fatalf("entry[1].kind: got %q want %q", entries[1].Turn.Kind, TurnAssistant)
	}
}

func TestSession_AutoSave_PersistsToolResults(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	comm := submitResultCall("c1", "done")
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := sess.ProcessInput(ctx, "hello")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "done")
	}
	sess.Close()

	// Meta file should exist.
	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 saved session meta, got %d", len(list))
	}

	// Tool results should be in the transcript.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}

	pendingToolCalls := map[string]struct{}{}
	seenToolResult := false
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				if part.ToolCall != nil && part.ToolCall.ID != "" {
					pendingToolCalls[part.ToolCall.ID] = struct{}{}
				}
			case llm.ContentToolResult:
				if part.ToolResult != nil && part.ToolResult.ToolCallID != "" {
					delete(pendingToolCalls, part.ToolResult.ToolCallID)
					if part.ToolResult.ToolCallID == "c1" {
						seenToolResult = true
					}
				}
			}
		}
	}

	if !seenToolResult {
		t.Fatal("expected transcript to include submit_result tool_result")
	}
	if len(pendingToolCalls) != 0 {
		t.Fatalf("expected no dangling tool calls in transcript, got %d", len(pendingToolCalls))
	}
}

func TestSession_AutoSave_DoesNotPersistMidToolRound(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	blockCall := llm.ToolCallData{
		ID:        "block-1",
		Name:      "block_tool",
		Arguments: json.RawMessage(`{}`),
	}
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(blockCall)
			},
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	sess.RegisterTool("block_tool", "blocks until released", map[string]any{}, func(ctx context.Context, args any) (any, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return "ok", nil
	})

	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "hello")
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("tool did not start: %v", ctx.Err())
	}

	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no meta before tool result, got %d", len(list))
	}

	close(release)

	if err := <-done; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	list2, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas after completion: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected 1 meta after completion, got %d", len(list2))
	}

	// Verify tool results are complete in the transcript.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}

	pending := map[string]struct{}{}
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				if part.ToolCall != nil && part.ToolCall.ID != "" {
					pending[part.ToolCall.ID] = struct{}{}
				}
			case llm.ContentToolResult:
				if part.ToolResult != nil && part.ToolResult.ToolCallID != "" {
					delete(pending, part.ToolResult.ToolCallID)
				}
			}
		}
	}
	if len(pending) != 0 {
		t.Fatalf("expected no dangling tool calls in transcript, got %d", len(pending))
	}
}

func TestRestoreSession_AutoSaveContinues(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	// Phase 1: Create a new session with auto-save and process input.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
		StateDir:              dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "first task"); err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	sess.Close()

	// Verify initial meta was saved.
	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas after phase 1: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session after phase 1, got %d", len(list))
	}
	initialTurnCount := list[0].TurnCount

	// Verify transcript has the initial history.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript after phase 1: %v", err)
	}
	initialEntryCount := len(entries)
	if initialEntryCount < 2 {
		t.Fatalf("expected at least 2 transcript entries after phase 1, got %d", initialEntryCount)
	}

	// Phase 2: Restore from meta + transcript and process more input.
	meta := list[0]
	sess2, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	go func() {
		for range sess2.Events() {
		}
	}()

	if _, err := sess2.ProcessInput(ctx, "second task"); err != nil {
		t.Fatalf("ProcessInput #2: %v", err)
	}
	sess2.Close()

	// Verify the meta was updated with new turns.
	list2, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas after phase 2: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected 1 session after phase 2, got %d", len(list2))
	}
	if list2[0].TurnCount <= initialTurnCount {
		t.Fatalf("turn count did not increase after resume: got %d, was %d", list2[0].TurnCount, initialTurnCount)
	}

	// Verify the transcript grew with new entries.
	_, entries2, _, err := ReadTranscript(tpath)
	if err != nil {
		t.Fatalf("ReadTranscript after phase 2: %v", err)
	}
	if len(entries2) <= initialEntryCount {
		t.Fatalf("transcript did not grow after resume: got %d entries, was %d", len(entries2), initialEntryCount)
	}

	// Verify the transcript has both original and new user inputs.
	var userTexts []string
	for _, entry := range entries2 {
		if entry.Turn.Kind == TurnUserInput {
			userTexts = append(userTexts, entry.Turn.Message.Text())
		}
	}
	if len(userTexts) < 2 {
		t.Fatalf("expected at least 2 user turns, got %d", len(userTexts))
	}
	if userTexts[0] != "first task" {
		t.Fatalf("first user text: got %q want %q", userTexts[0], "first task")
	}
	if userTexts[1] != "second task" {
		t.Fatalf("second user text: got %q want %q", userTexts[1], "second task")
	}
}

func TestRestoreSession_CanProcessInput(t *testing.T) {
	dir := t.TempDir()
	// Minimal snapshot with no history.
	snap := SessionSnapshot{
		ID:        "01JTEST000000000000000099",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		History:   []Turn{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := RestoreSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), snap, "")
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := sess.ProcessInput(ctx, "test")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(result) == "" {
		t.Fatalf("expected non-empty result")
	}
}

func TestRestoreSession_UsesTranscriptOverSnapshot(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	// Snapshot has old history that should be ignored when a transcript exists.
	snap := SessionSnapshot{
		ID:        "01JTEST_TRANSCRIPT_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		History: []Turn{
			NewTurn(TurnUserInput, llm.User("snapshot-old-message")),
			NewTurn(TurnAssistant, llm.Assistant("snapshot-old-reply")),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 2,
	}

	// Write a transcript file with different history.
	tpath := filepath.Join(stateDir, sessionsSubdir, snap.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: snap.ID,
		CreatedAt: snap.CreatedAt,
		ProfileID: snap.ProfileID,
		Model:     snap.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	if err := tw.Append(NewTurn(TurnUserInput, llm.User("transcript-message"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Append(NewTurn(TurnAssistant, llm.Assistant("transcript-reply"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), snap, stateDir)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	defer sess.Close()

	// Process a new input so we can inspect the history sent to the LLM.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}

	// The restored history should come from the transcript, not the snapshot.
	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	// Should contain "transcript-message" (from transcript), not "snapshot-old-message".
	foundTranscript := false
	foundSnapshot := false
	for _, text := range userTexts {
		if text == "transcript-message" {
			foundTranscript = true
		}
		if text == "snapshot-old-message" {
			foundSnapshot = true
		}
	}
	if !foundTranscript {
		t.Fatalf("expected transcript history in restored session, got user texts: %v", userTexts)
	}
	if foundSnapshot {
		t.Fatalf("snapshot history should not appear when transcript exists, got user texts: %v", userTexts)
	}
}

func TestRestoreSession_FallsBackToSnapshotWithoutTranscript(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	// No transcript file exists -- should fall back to snapshot history.
	snap := SessionSnapshot{
		ID:        "01JTEST_FALLBACK_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		History: []Turn{
			NewTurn(TurnUserInput, llm.User("snapshot-fallback")),
			NewTurn(TurnAssistant, llm.Assistant("snapshot-reply")),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 2,
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), snap, stateDir)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}

	// Should contain snapshot history since no transcript exists.
	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	foundSnapshot := false
	for _, text := range userTexts {
		if text == "snapshot-fallback" {
			foundSnapshot = true
		}
	}
	if !foundSnapshot {
		t.Fatalf("expected snapshot history in restored session, got user texts: %v", userTexts)
	}
}

func TestRestoreSession_TranscriptWithCompaction(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	snap := SessionSnapshot{
		ID:        "01JTEST_COMPACT_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		History: []Turn{
			NewTurn(TurnUserInput, llm.User("snapshot-should-not-appear")),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 5,
	}

	// Write a transcript with a checkpoint in the middle.
	tpath := filepath.Join(stateDir, sessionsSubdir, snap.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: snap.ID,
		CreatedAt: snap.CreatedAt,
		ProfileID: snap.ProfileID,
		Model:     snap.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	// Pre-compaction turns (should be discarded by ResumeHistory).
	tw.Append(NewTurn(TurnUserInput, llm.User("old-message-1")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("old-reply-1")))
	tw.Append(NewTurn(TurnUserInput, llm.User("old-message-2")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("old-reply-2")))
	// Checkpoint turn.
	tw.Append(NewTurn(TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT] Summary of prior work")))
	// Post-compaction turns (should be kept).
	tw.Append(NewTurn(TurnUserInput, llm.User("post-compact-message")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("post-compact-reply")))
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), snap, stateDir)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue after compaction"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}

	// Collect all user-role message texts.
	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	// Should have: checkpoint summary, post-compact-message, continue after compaction
	// Should NOT have: old-message-1, old-message-2, snapshot-should-not-appear
	foundCheckpoint := false
	foundPostCompact := false
	foundOld := false
	foundSnapshot := false
	for _, text := range userTexts {
		if strings.Contains(text, "[CONTEXT CHECKPOINT]") {
			foundCheckpoint = true
		}
		if text == "post-compact-message" {
			foundPostCompact = true
		}
		if text == "old-message-1" || text == "old-message-2" {
			foundOld = true
		}
		if text == "snapshot-should-not-appear" {
			foundSnapshot = true
		}
	}

	if !foundCheckpoint {
		t.Fatalf("expected checkpoint in resumed history, got user texts: %v", userTexts)
	}
	if !foundPostCompact {
		t.Fatalf("expected post-compaction message in resumed history, got user texts: %v", userTexts)
	}
	if foundOld {
		t.Fatalf("pre-compaction messages should not appear in resumed history, got user texts: %v", userTexts)
	}
	if foundSnapshot {
		t.Fatalf("snapshot history should not appear when transcript exists, got user texts: %v", userTexts)
	}
}

// TestMetaTurnCount_CountsModelResponses verifies that meta.json turn_count
// reflects the number of model responses (LLM round-trips), not the number
// of user input submissions.
func TestMetaTurnCount_CountsModelResponses(t *testing.T) {
	c := llm.NewClient()
	callNum := 0
	var mu sync.Mutex
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 1: model returns a tool call
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
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
					Finish: llm.FinishReason{Reason: "tool_calls"},
				}
			},
			// Round 2: model returns another tool call
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				call := llm.ToolCallData{
					ID:        "c2",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":"b.txt"}`),
					Type:      "function",
				}
				return llm.Response{
					Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}},
					},
					Finish: llm.FinishReason{Reason: "tool_calls"},
				}
			},
			// Round 3: model returns final text
			func(req llm.Request) llm.Response {
				mu.Lock()
				callNum++
				mu.Unlock()
				return wrapCommunicateResponse(llm.Response{
					Message: llm.Assistant("all done"),
					Finish:  llm.FinishReason{Reason: "stop"},
				})
			},
		},
	}
	c.Register(f)

	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir),
		SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	// One user input, but three model responses (2 tool rounds + 1 final text).
	if _, err := sess.ProcessInput(context.Background(), "do stuff"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	mu.Lock()
	totalCalls := callNum
	mu.Unlock()
	if totalCalls != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", totalCalls)
	}

	// The meta.json turn_count should reflect all 3 model responses,
	// not just 1 (the number of user inputs).
	metas, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].TurnCount != 3 {
		t.Fatalf("meta turn_count: got %d, want 3 (should count model responses, not user inputs)", metas[0].TurnCount)
	}
}
