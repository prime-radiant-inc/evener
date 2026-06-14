package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// snapshotFakeAdapter is a minimal adapter for session restore and auto-save tests.
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
	return nil, llm.ErrStreamUnsupported
}
func (a *snapshotFakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}

func TestSession_AutoSave_WritesMetaAfterProcessInput(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	_, err = sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.Close()

	// A meta file should exist (not a full snapshot).
	list, err := schema.ListSessionMetas(dir)
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
	_, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 transcript entries, got %d", len(entries))
	}
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Fatalf("entry[0].kind: got %q want %q", entries[0].Turn.Kind, schema.TurnUserInput)
	}
	if entries[1].Turn.Kind != schema.TurnAssistant {
		t.Fatalf("entry[1].kind: got %q want %q", entries[1].Turn.Kind, schema.TurnAssistant)
	}
}

func TestSession_AutoSave_PersistsToolResults(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	comm := communicateCall("c1", "done")
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(comm)
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	out, err := sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "done")
	}
	sess.Close()

	// Meta file should exist.
	list, err := schema.ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 saved session meta, got %d", len(list))
	}

	// Tool results should be in the transcript.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
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
		t.Fatal("expected transcript to include communicate tool_result")
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

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
		_, err := sess.ProcessInput(ctx, "hello", nil)
		done <- err
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("tool did not start: %v", ctx.Err())
	}

	// NewSession writes the initial meta so the hub can discover the session
	// before any turn completes. No additional saves should happen mid-tool-round.
	list, err := schema.ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 meta (from session creation) before tool result, got %d", len(list))
	}

	close(release)

	if err := <-done; err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	list2, err := schema.ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas after completion: %v", err)
	}
	if len(list2) != 1 {
		t.Fatalf("expected 1 meta after completion, got %d", len(list2))
	}

	// Verify tool results are complete in the transcript.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript: %v", err)
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	if _, err := sess.ProcessInput(ctx, "first task", nil); err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	sess.Close()

	// Verify initial meta was saved.
	list, err := schema.ListSessionMetas(dir)
	if err != nil {
		t.Fatalf("ListSessionMetas after phase 1: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 session after phase 1, got %d", len(list))
	}
	initialTurnCount := list[0].TurnCount

	// Verify transcript has the initial history.
	tpath := filepath.Join(dir, sessionsSubdir, sess.ID()+".transcript.jsonl")
	_, entries, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript after phase 1: %v", err)
	}
	initialEntryCount := len(entries)
	if initialEntryCount < 2 {
		t.Fatalf("expected at least 2 transcript entries after phase 1, got %d", initialEntryCount)
	}

	// Phase 2: Restore from meta + transcript and process more input.
	meta := list[0]
	sess2, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	go func() {
		for range sess2.Events() {
		}
	}()

	if _, err := sess2.ProcessInput(ctx, "second task", nil); err != nil {
		t.Fatalf("ProcessInput #2: %v", err)
	}
	sess2.Close()

	// Verify the meta was updated with new turns.
	list2, err := schema.ListSessionMetas(dir)
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
	_, entries2, _, err := readTranscript(tpath)
	if err != nil {
		t.Fatalf("readTranscript after phase 2: %v", err)
	}
	if len(entries2) <= initialEntryCount {
		t.Fatalf("transcript did not grow after resume: got %d entries, was %d", len(entries2), initialEntryCount)
	}

	// Verify the transcript has both original and new user inputs.
	var userTexts []string
	for _, entry := range entries2 {
		if entry.Turn.Kind == schema.TurnUserInput {
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

// TestMetaTurnCount_CountsModelResponses verifies that meta.json turn_count
// reflects the number of model responses (LLM round-trips), not the number
// of user input submissions.
func TestRestoreSession_RestoresCheapModelRouting(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&snapshotFakeAdapter{name: "openai"})

	// A session configured with a cross-provider cheap model persists it...
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001")
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	meta := sess.Meta()
	sess.Close()
	if meta.CheapModel != "anthropic/claude-haiku-4-5-20251001" {
		t.Fatalf("meta.CheapModel = %q, want anthropic/claude-haiku-4-5-20251001", meta.CheapModel)
	}

	// ...and a resume from that meta (with a cheap-less base profile, as the hub
	// does not re-pass the launch arg) restores the cheap routing.
	sess2, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	go func() {
		for range sess2.Events() {
		}
	}()
	defer sess2.Close()

	prov, model := sess2.profile.CheapModelRef()
	if prov != "anthropic" || model != "claude-haiku-4-5-20251001" {
		t.Fatalf("restored CheapModelRef = (%q, %q), want (anthropic, claude-haiku-4-5-20251001)", prov, model)
	}
}

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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	// One user input, but three model responses (2 tool rounds + 1 final text).
	if _, err := sess.ProcessInput(context.Background(), "do stuff", nil); err != nil {
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
	metas, err := schema.ListSessionMetas(dir)
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

func TestSessionMeta_OriginalPrompt_RoundTrip(t *testing.T) {
	original := schema.SessionMeta{
		ID:             "01TEST0001",
		ProfileID:      "openai-gpt-5",
		Model:          "gpt-5.2",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: "/tmp/x"},
		CreatedAt:      time.Date(2026, 5, 7, 14, 32, 11, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 5, 7, 14, 32, 11, 0, time.UTC),
		TurnCount:      3,
		OriginalPrompt: "fix the bug in handler",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); !strings.Contains(got, "original_prompt") || strings.Contains(got, "original_task") {
		t.Fatalf("expected current original_prompt JSON only, got: %s", got)
	}
	var got schema.SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OriginalPrompt != "fix the bug in handler" {
		t.Fatalf("OriginalPrompt: got %q, want %q", got.OriginalPrompt, "fix the bug in handler")
	}
}

func TestSessionMeta_NameFields_RoundTrip(t *testing.T) {
	updatedAt := time.Date(2026, 5, 20, 14, 32, 11, 0, time.UTC)
	original := schema.SessionMeta{
		ID:            "01TEST0001",
		Name:          "Fix Handler Bug",
		NameSource:    "prompt",
		NameUpdatedAt: updatedAt,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	gotJSON := string(data)
	for _, want := range []string{"name", "name_source", "name_updated_at"} {
		if !strings.Contains(gotJSON, want) {
			t.Fatalf("expected %q in JSON, got: %s", want, gotJSON)
		}
	}
	var got schema.SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != original.Name {
		t.Fatalf("Name: got %q, want %q", got.Name, original.Name)
	}
	if got.NameSource != original.NameSource {
		t.Fatalf("NameSource: got %q, want %q", got.NameSource, original.NameSource)
	}
	if !got.NameUpdatedAt.Equal(updatedAt) {
		t.Fatalf("NameUpdatedAt: got %v, want %v", got.NameUpdatedAt, updatedAt)
	}
}

func TestSessionMeta_NameFields_OmitEmpty(t *testing.T) {
	meta := schema.SessionMeta{ID: "01TEST0001"}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	for _, field := range []string{"name", "name_source", "name_updated_at"} {
		if strings.Contains(got, field) {
			t.Fatalf("expected empty name fields to be omitted, got: %s", got)
		}
	}
}

func TestSessionDisplayName(t *testing.T) {
	tests := []struct {
		name string
		meta schema.SessionMeta
		want string
	}{
		{
			name: "name wins over original prompt",
			meta: schema.SessionMeta{ID: "01TEST0001", Name: "Generated Name", OriginalPrompt: "original prompt"},
			want: "Generated Name",
		},
		{
			name: "original prompt wins over ID",
			meta: schema.SessionMeta{ID: "01TEST0001", OriginalPrompt: "original prompt"},
			want: "original prompt",
		},
		{
			name: "ID used when both are blank",
			meta: schema.SessionMeta{ID: "01TEST0001"},
			want: "01TEST0001",
		},
		{
			name: "whitespace is trimmed",
			meta: schema.SessionMeta{ID: "  01TEST0001  ", Name: "  Generated Name  ", OriginalPrompt: "  original prompt  "},
			want: "Generated Name",
		},
		{
			name: "blank name falls back after trimming",
			meta: schema.SessionMeta{ID: "01TEST0001", Name: "  ", OriginalPrompt: "  original prompt  "},
			want: "original prompt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schema.SessionDisplayName(tt.meta); got != tt.want {
				t.Fatalf("SessionDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionMeta_OriginalPrompt_ReadsLegacyOriginalTask(t *testing.T) {
	data := []byte(`{"id":"01TEST0001","original_task":"fix the bug in handler"}`)
	var got schema.SessionMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OriginalPrompt != "fix the bug in handler" {
		t.Fatalf("OriginalPrompt: got %q, want %q", got.OriginalPrompt, "fix the bug in handler")
	}
}

func TestSessionMeta_OriginalPrompt_OmitEmpty(t *testing.T) {
	meta := schema.SessionMeta{ID: "01TEST0001"}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); strings.Contains(got, "original_prompt") || strings.Contains(got, "original_task") {
		t.Fatalf("expected original prompt fields to be omitted when empty, got: %s", got)
	}
}

func TestSessionMeta_ForkFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	meta := schema.SessionMeta{
		ID:              "01CHILD",
		ParentSessionID: "01PARENT",
		DivergenceTurn:  7,
		ForkLabel:       "before TDD",
		UpdatedAt:       time.Now(),
	}
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	got, err := schema.LoadSessionMeta(dir, "01CHILD")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentSessionID != "01PARENT" {
		t.Errorf("ParentSessionID: %q", got.ParentSessionID)
	}
	if got.DivergenceTurn != 7 {
		t.Errorf("DivergenceTurn: %d", got.DivergenceTurn)
	}
	if got.ForkLabel != "before TDD" {
		t.Errorf("ForkLabel: %q", got.ForkLabel)
	}
}
