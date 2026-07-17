package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// buildParentSession creates a parent session with a 4-turn transcript
// (USER, ASSISTANT, USER, ASSISTANT) and a corresponding meta file.
// Returns the parentID and stateDir.
func buildParentSession(t *testing.T) (stateDir, parentID string) {
	t.Helper()
	stateDir = t.TempDir()
	parentID = "01PARENT00000000000000001"

	tpath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("second reply")),
	}
	for _, turn := range turns {
		if err := tw.Append(turn); err != nil {
			t.Fatalf("Append turn: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        parentID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 50},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/test"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		TurnCount: 2,
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	return stateDir, parentID
}

func appendParentTranscript(t *testing.T, stateDir, parentID, suffix string) {
	t.Helper()
	path := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open parent transcript: %v", err)
	}
	if _, err := f.WriteString(suffix); err != nil {
		_ = f.Close()
		t.Fatalf("append parent transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close parent transcript: %v", err)
	}
}

func sessionArtifactNames(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, sessionsSubdir))
	if err != nil {
		t.Fatalf("read session artifacts: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return strings.Join(names, "\n")
}

// TestForkSession_CopiesPrefixAndAppliesEdit verifies the core fork semantics:
// the child gets the first (divergenceTurn-1) USER_INPUT turns from the parent
// followed by the edited message, and meta is wired up correctly on both sides.
func TestForkSession_CopiesPrefixAndAppliesEdit(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if err := identifier.ValidateSessionID(childID); err != nil {
		t.Fatalf("child session ID %q: %v", childID, err)
	}

	// childID must be non-empty and distinct from parentID.
	if childID == "" {
		t.Fatal("childID is empty")
	}
	if childID == parentID {
		t.Fatalf("childID should differ from parentID, got %q", childID)
	}

	// Child meta assertions.
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Errorf("child ParentSessionID: got %q, want %q", childMeta.ParentSessionID, parentID)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Errorf("child DivergenceTurn: got %d, want 3", childMeta.DivergenceTurn)
	}
	if childMeta.TurnCount != 1 {
		t.Errorf("child TurnCount: got %d, want 1", childMeta.TurnCount)
	}
	if childMeta.ForkLabel != "" {
		t.Errorf("child ForkLabel should be empty, got %q", childMeta.ForkLabel)
	}

	// Parent meta should have been updated with the fork label.
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	if parentMeta.ForkLabel != "before TDD" {
		t.Errorf("parent ForkLabel: got %q, want %q", parentMeta.ForkLabel, "before TDD")
	}

	// Child transcript must contain the edited message in its last entry line.
	childTranscriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	data, err := os.ReadFile(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadFile(child transcript): %v", err)
	}
	if !strings.Contains(string(data), "second task, table-driven") {
		t.Error("child transcript does not contain the edited message text")
	}

	// Read child transcript via readTranscript and verify structure.
	_, entries, _, err := readTranscript(childTranscriptPath)
	if err != nil {
		t.Fatalf("readTranscript(child): %v", err)
	}

	// Expected entries:
	//   0: USER_INPUT "first task"         (turn 1 from parent)
	//   1: ASSISTANT  "first reply"        (non-USER turn, included in prefix)
	//   2: USER_INPUT "second task, table-driven"  (the edited divergence turn)
	if len(entries) != 3 {
		t.Fatalf("child transcript entry count: got %d, want 3", len(entries))
	}

	// First two entries are the prefix (turn 1 user + turn 1 assistant from parent).
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Errorf("entries[0].Kind: got %q, want %q", entries[0].Turn.Kind, schema.TurnUserInput)
	}
	if entries[0].Turn.Message.Text() != "first task" {
		t.Errorf("entries[0] text: got %q, want %q", entries[0].Turn.Message.Text(), "first task")
	}
	if entries[1].Turn.Kind != schema.TurnAssistant {
		t.Errorf("entries[1].Kind: got %q, want %q", entries[1].Turn.Kind, schema.TurnAssistant)
	}

	// Third entry is the edited turn.
	if entries[2].Turn.Kind != schema.TurnUserInput {
		t.Errorf("entries[2].Kind: got %q, want %q", entries[2].Turn.Kind, schema.TurnUserInput)
	}
	if entries[2].Turn.Message.Text() != "second task, table-driven" {
		t.Errorf("entries[2] text: got %q, want %q", entries[2].Turn.Message.Text(), "second task, table-driven")
	}
}

func TestForkSession_RejectsMixedAPICallWithoutCreatingChild(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"api_call\"}\n")
	before := sessionArtifactNames(t, stateDir)

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if !errors.Is(err, transcript.ErrUnsupportedFormat) {
		t.Fatalf("ForkSession error = %v, want transcript.ErrUnsupportedFormat", err)
	}
	if childID != "" {
		t.Fatalf("ForkSession child id = %q, want empty", childID)
	}
	if after := sessionArtifactNames(t, stateDir); after != before {
		t.Fatalf("session artifacts changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestForkSession_RejectsUnsupportedHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transform func(string) string
	}{
		{
			name: "version one",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2`, `"format_version":1`, 1)
			},
		},
		{
			name: "missing version",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2,`, "", 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stateDir, parentID := buildParentSession(t)
			parentPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
			raw, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatalf("read parent transcript: %v", err)
			}
			if err := os.WriteFile(parentPath, []byte(tt.transform(string(raw))), 0o644); err != nil {
				t.Fatalf("write parent transcript: %v", err)
			}

			if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("ForkSession error = %v, want transcript.ErrUnsupportedFormat", err)
			}
		})
	}
}

func TestForkSession_RejectsUnknownTranscriptFields(t *testing.T) {
	tests := []struct {
		name      string
		transform func(string) string
	}{
		{
			name: "header",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2`, `"format_version":2,"unknown":true`, 1)
			},
		},
		{
			name: "entry",
			transform: func(raw string) string {
				return strings.Replace(raw, `"seq":0`, `"seq":0,"unknown":true`, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir, parentID := buildParentSession(t)
			parentPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
			raw, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(parentPath, []byte(tt.transform(string(raw))), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); err == nil {
				t.Fatal("ForkSession accepted an unknown transcript field")
			}
		})
	}
}

func TestForkSession_UsesFirstNonEmptyRecordAsHeader(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	parentPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")
	raw, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("read parent transcript: %v", err)
	}
	if err := os.WriteFile(parentPath, append([]byte("\n"), raw...), 0o644); err != nil {
		t.Fatalf("write parent transcript: %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if childID == "" {
		t.Fatal("ForkSession child id is empty")
	}
}

func TestForkSession_RejectsMalformedCompleteInteriorRecordWithoutCreatingChild(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{not valid json\n")
	before := sessionArtifactNames(t, stateDir)
	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err == nil {
		t.Fatal("ForkSession error is nil, want malformed-record failure")
	}
	if childID != "" {
		t.Fatalf("ForkSession child id = %q, want empty", childID)
	}
	if after := sessionArtifactNames(t, stateDir); after != before {
		t.Fatalf("session artifacts changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestForkSession_RejectsMalformedCompleteEntry(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"entry\",\"turn\":\"not-an-object\"}\n")

	if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); err == nil || !strings.Contains(err.Error(), "parsing parent transcript entry") {
		t.Fatalf("ForkSession error = %v, want malformed-entry failure", err)
	}
}

func TestForkSession_IgnoresIncompleteFinalRecord(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"entry\",\"seq\":9,\"tur")

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if childID == "" {
		t.Fatal("ForkSession child id is empty")
	}
}

func TestForkSession_ChildLineagePreservedAcrossMetaRewrite(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), childMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ParentSessionID != parentID {
		t.Errorf("rewritten ParentSessionID: got %q, want %q", rewritten.ParentSessionID, parentID)
	}
	if rewritten.DivergenceTurn != 3 {
		t.Errorf("rewritten DivergenceTurn: got %d, want 3", rewritten.DivergenceTurn)
	}
	if rewritten.IsSubagent {
		t.Error("rewritten IsSubagent: got true, want false for fork child")
	}
}

func TestForkSession_ParentForkLabelPreservedAcrossMetaRewrite(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	if _, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD"); err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ForkLabel != "before TDD" {
		t.Errorf("rewritten ForkLabel: got %q, want %q", rewritten.ForkLabel, "before TDD")
	}
}

func TestForkSession_RestorePreservesAcceptedInputBudget(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	parentMeta.Config = (SessionConfig{MaxTurns: 7}).toSnapshot()
	if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
		t.Fatalf("SaveSessionMeta(parent): %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "second task, edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.AcceptedInputTurns != 2 {
		t.Fatalf("child accepted input turns = %d, want 2", childMeta.AcceptedInputTurns)
	}

	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return communicateResponse(true, "done")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	restored := restoreBudgetSession(t, client, childMeta, stateDir, nil)

	restored.mu.Lock()
	acceptedAtRestore := restored.turns
	restored.mu.Unlock()
	if acceptedAtRestore != 2 {
		t.Fatalf("restored accepted input turns = %d, want 2", acceptedAtRestore)
	}
	if got := countBudgetSteering(budgetHistory(restored), rootTurnBudgetWarning); got != 0 {
		t.Fatalf("warning count before first restored input = %d, want 0", got)
	}

	for i := 0; i < 5; i++ {
		if _, err := restored.ProcessInput(context.Background(), "continued input", nil); err != nil {
			t.Fatalf("ProcessInput(%d): %v", i+1, err)
		}
		if i == 0 {
			requests := adapter.Requests()
			if len(requests) != 1 || !requestHasExactUserMessage(requests[0], rootTurnBudgetWarning) {
				t.Fatalf("first restored request missing five-turn warning: %+v", requests)
			}
		}
	}
	if got := countBudgetSteering(budgetHistory(restored), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("warning count after budget use = %d, want 1", got)
	}
	restored.mu.Lock()
	acceptedAtLimit := restored.turns
	restored.mu.Unlock()
	if acceptedAtLimit != 7 {
		t.Fatalf("accepted input turns at limit = %d, want 7", acceptedAtLimit)
	}

	out, err := restored.ProcessInput(context.Background(), "over budget", nil)
	if out != "" {
		t.Fatalf("over-budget output = %q, want empty", out)
	}
	requireBudgetExhaustion(t, err, exhaustedBudgetTurns, 7, false)
	if got := len(adapter.Requests()); got != 5 {
		t.Fatalf("model requests after rejected input = %d, want 5", got)
	}
}

// TestForkSession_RejectsOutOfRangeDivergence verifies that divergenceTurn=0
// and divergenceTurn exceeding the parent's USER_INPUT count both return errors.
func TestForkSession_RejectsOutOfRangeDivergence(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	// divergenceTurn=0 must error.
	_, err := ForkSession(stateDir, parentID, 0, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=0) should return an error")
	}

	// divergenceTurn exceeding count (parent has 2 USER_INPUT turns).
	_, err = ForkSession(stateDir, parentID, 10, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=10) should return an error when parent has only 2 USER_INPUT turns")
	}
}

// TestForkSession_RejectsMissingParent verifies that a missing parent transcript
// causes ForkSession to return an error.
func TestForkSession_RejectsMissingParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	_, err := ForkSession(stateDir, "NONEXISTENT_SESSION", 1, "hello", "")
	if err == nil {
		t.Error("ForkSession with missing parent should return an error")
	}
}
