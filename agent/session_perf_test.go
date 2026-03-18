package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// ---------------------------------------------------------------------------
// Issue 1: Project docs should be loaded once at init, not every round
// ---------------------------------------------------------------------------

func TestSession_ProjectDocsLoadedOnceAtInit(t *testing.T) {
	dir := t.TempDir()

	// Create a project doc file so LoadProjectDocs has something to find.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# My Agent\nHello"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Session should have cached project docs.
	if sess.projectDocs == nil {
		t.Fatal("expected projectDocs to be cached after NewSession")
	}
}

func TestSession_CachedProjectDocsUsedInSystemPrompt(t *testing.T) {
	dir := t.TempDir()

	// Create a project doc.
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("cached-doc-content"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// The system prompt sent to the LLM should contain the cached doc content.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
	// System prompt is the first message (role=system).
	if len(reqs[0].Messages) == 0 {
		t.Fatal("expected at least 1 message in request")
	}
	sys := reqs[0].Messages[0].Text()
	if !strings.Contains(sys, "cached-doc-content") {
		t.Fatalf("system prompt should contain cached project doc content, got: %s", sys[:min(200, len(sys))])
	}
}

// ---------------------------------------------------------------------------
// Issue 2: SessionMeta — lightweight save instead of full snapshot
// ---------------------------------------------------------------------------

func testSessionMeta() SessionMeta {
	return SessionMeta{
		ID:        "01JTEST_META_00000000001",
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
		CreatedAt: time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC),
		TurnCount: 2,
	}
}

func TestSessionMeta_JSONRoundTrip(t *testing.T) {
	orig := testSessionMeta()
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SessionMeta
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
	if got.TurnCount != orig.TurnCount {
		t.Fatalf("turn_count: got %d want %d", got.TurnCount, orig.TurnCount)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("created_at: got %v want %v", got.CreatedAt, orig.CreatedAt)
	}
}

func TestSaveSessionMeta_CreatesMetaFile(t *testing.T) {
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	// File should exist at sessions/<id>.meta.json
	path := filepath.Join(dir, "sessions", meta.ID+".meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Should be compact JSON (no indentation).
	if strings.Contains(string(data), "  ") {
		t.Fatal("meta.json should use compact JSON (no indentation)")
	}

	var got SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("saved id: got %q want %q", got.ID, meta.ID)
	}
}

func TestLoadSessionMeta(t *testing.T) {
	dir := t.TempDir()
	meta := testSessionMeta()

	if err := SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	got, err := LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got.ID != meta.ID {
		t.Fatalf("id: got %q want %q", got.ID, meta.ID)
	}
	if got.Model != meta.Model {
		t.Fatalf("model: got %q want %q", got.Model, meta.Model)
	}
}

func TestLoadSessionMeta_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSessionMeta(dir, "NONEXISTENT")
	if err == nil {
		t.Fatal("expected error for nonexistent meta")
	}
}

func TestListSessionMetas_SortedByUpdatedAt(t *testing.T) {
	dir := t.TempDir()

	meta1 := testSessionMeta()
	meta1.ID = "01JTEST_META_00000000001"
	meta1.UpdatedAt = time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	meta2 := testSessionMeta()
	meta2.ID = "01JTEST_META_00000000002"
	meta2.UpdatedAt = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	meta3 := testSessionMeta()
	meta3.ID = "01JTEST_META_00000000003"
	meta3.UpdatedAt = time.Date(2025, 1, 15, 11, 0, 0, 0, time.UTC)

	for _, m := range []SessionMeta{meta1, meta2, meta3} {
		if err := SaveSessionMeta(dir, m); err != nil {
			t.Fatalf("SaveSessionMeta %s: %v", m.ID, err)
		}
	}

	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("list length: got %d want 3", len(list))
	}
	// Most recently updated first.
	if list[0].ID != meta2.ID {
		t.Fatalf("list[0].id: got %q want %q", list[0].ID, meta2.ID)
	}
	if list[1].ID != meta3.ID {
		t.Fatalf("list[1].id: got %q want %q", list[1].ID, meta3.ID)
	}
	if list[2].ID != meta1.ID {
		t.Fatalf("list[2].id: got %q want %q", list[2].ID, meta1.ID)
	}
}

func TestListSessionMetas_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	list, err := ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestSession_MaybeAutoSave_WritesMetaNotSnapshot(t *testing.T) {
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
	go func() { for range sess.Events() {} }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := sess.ProcessInput(ctx, "hello"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// Should find a .meta.json file, not a full .json snapshot.
	sessDir := filepath.Join(dir, "sessions")
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	foundMeta := false
	foundFullSnapshot := false
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".meta.json") {
			foundMeta = true
		}
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".meta.json") && !strings.HasSuffix(name, ".transcript.jsonl") {
			foundFullSnapshot = true
		}
	}

	if !foundMeta {
		t.Fatal("expected .meta.json file after ProcessInput")
	}
	if foundFullSnapshot {
		t.Fatal("should not write full .json snapshot from maybeAutoSave (only .meta.json)")
	}
}

func TestSession_Meta_ReturnsLightweightMeta(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 200,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	meta := sess.Meta()

	if meta.ID != sess.ID() {
		t.Fatalf("id: got %q want %q", meta.ID, sess.ID())
	}
	if meta.ProfileID != "openai" {
		t.Fatalf("profile_id: got %q want %q", meta.ProfileID, "openai")
	}
	if meta.Model != "gpt-5.2" {
		t.Fatalf("model: got %q want %q", meta.Model, "gpt-5.2")
	}
}

func TestRestoreSession_FromMetaAndTranscript(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_RESTORE_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 2,
	}

	// Write a transcript file with history.
	tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	if err := tw.Append(NewTurn(TurnUserInput, llm.User("transcript-msg"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Append(NewTurn(TurnAssistant, llm.Assistant("transcript-reply"))); err != nil {
		t.Fatalf("Append: %v", err)
	}
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	if sess.ID() != meta.ID {
		t.Fatalf("id: got %q want %q", sess.ID(), meta.ID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}

	// The restored history should come from the transcript.
	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	foundTranscript := false
	for _, text := range userTexts {
		if text == "transcript-msg" {
			foundTranscript = true
		}
	}
	if !foundTranscript {
		t.Fatalf("expected transcript history in restored session, got user texts: %v", userTexts)
	}
}

func TestRestoreSessionFromMeta_NoTranscript_StartsClean(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_CLEAN_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 0,
	}

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "test"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Should process fine with no history.
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}
}

func TestRestoreSessionFromMeta_TranscriptWithCompaction(t *testing.T) {
	dir := t.TempDir()
	stateDir := t.TempDir()

	meta := SessionMeta{
		ID:        "01JTEST_META_COMPACT_001",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    SessionConfig{MaxToolRoundsPerInput: 200},
		EnvInfo:   EnvironmentInfo{WorkingDir: dir},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		TurnCount: 5,
	}

	// Write a transcript with compaction.
	tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, TranscriptHeader{
		SessionID: meta.ID,
		CreatedAt: meta.CreatedAt,
		ProfileID: meta.ProfileID,
		Model:     meta.Model,
	})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	tw.Append(NewTurn(TurnUserInput, llm.User("old-msg")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("old-reply")))
	tw.Append(NewTurn(TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT] Summary")))
	tw.Append(NewTurn(TurnUserInput, llm.User("post-compact-msg")))
	tw.Append(NewTurn(TurnAssistant, llm.Assistant("post-compact-reply")))
	tw.Close()

	c := llm.NewClient()
	adapter := &snapshotFakeAdapter{name: "openai"}
	c.Register(adapter)

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM request")
	}

	var userTexts []string
	for _, m := range reqs[0].Messages {
		if m.Role == llm.RoleUser {
			userTexts = append(userTexts, m.Text())
		}
	}

	// Should have checkpoint + post-compact, NOT old-msg.
	foundCheckpoint := false
	foundPostCompact := false
	foundOld := false
	for _, text := range userTexts {
		if strings.Contains(text, "[CONTEXT CHECKPOINT]") {
			foundCheckpoint = true
		}
		if text == "post-compact-msg" {
			foundPostCompact = true
		}
		if text == "old-msg" {
			foundOld = true
		}
	}

	if !foundCheckpoint {
		t.Fatalf("expected checkpoint, got user texts: %v", userTexts)
	}
	if !foundPostCompact {
		t.Fatalf("expected post-compact message, got user texts: %v", userTexts)
	}
	if foundOld {
		t.Fatalf("pre-compaction messages should not appear, got user texts: %v", userTexts)
	}
}
