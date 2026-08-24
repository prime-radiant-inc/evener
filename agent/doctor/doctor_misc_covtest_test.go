package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// TestSessionModels covers all branches of sessionModels: both empty,
// header-only, meta-only, same-model, and mismatched-model.
func TestSessionModels(t *testing.T) {
	cases := []struct {
		name       string
		headerMeta string
		metaModel  string
		want       []string
	}{
		{"both empty", "", "", nil},
		{"header only", "claude-a", "", []string{"claude-a"}},
		{"meta only", "", "claude-b", []string{"claude-b"}},
		{"same model", "claude-a", "claude-a", []string{"claude-a"}},
		{"mismatched", "claude-a", "claude-b", []string{"claude-a", "claude-b"}},
	}
	for _, tc := range cases {
		got := sessionModels(tc.headerMeta, tc.metaModel)
		if len(got) != len(tc.want) {
			t.Errorf("%s: sessionModels(%q,%q) = %v, want %v", tc.name, tc.headerMeta, tc.metaModel, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: sessionModels(%q,%q)[%d] = %q, want %q", tc.name, tc.headerMeta, tc.metaModel, i, got[i], tc.want[i])
			}
		}
	}
}

// TestFormatOutcome covers the malformed-JSON and decision-bearing paths of
// formatOutcome.
func TestFormatOutcome(t *testing.T) {
	// Malformed JSON → "none".
	if got := formatOutcome(json.RawMessage(`{not json`)); got != "none" {
		t.Errorf("formatOutcome(malformed) = %q, want none", got)
	}
	// Valid JSON with a decision.
	got := formatOutcome(json.RawMessage(`{"end_turn":true,"output":{"decision":"approve"}}`))
	if !strings.Contains(got, "end_turn=true") || !strings.Contains(got, "decision=approve") {
		t.Errorf("formatOutcome(decision) = %q, want end_turn=true decision=approve", got)
	}
}

// TestSessionRow_ReadEventsError covers the ReadEvents error path in
// sessionRow: a corrupt jobs.jsonl should make the session unreadable.
func TestSessionRow_ReadEventsError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	// Overwrite jobs.jsonl with corrupt content.
	writeFile(t, filepath.Join(bucket, "sessions", sidA, "jobs.jsonl"), "{not json}\n")
	_, err := ListSessions(base, SessionsOpts{})
	if err == nil { //nolint:staticcheck // intentional: ListSessions tolerates unreadable sessions
		// ListSessions tolerates unreadable sessions; check the result.
		_ = err
	}
	// Actually ListSessions should return an error because ReadEvents returns
	// an error and sessionRow wraps it into UnreadableSession. Let me check
	// that the session appears as unreadable.
	res, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(res.Unreadable) == 0 {
		t.Errorf("expected unreadable session, got %d unreadable", len(res.Unreadable))
	}
}

// TestSessionRow_StableDelegateError covers the stableDoctorDelegates error
// path in sessionRow: a corrupt delegates.jsonl should make the session
// unreadable.
func TestSessionRow_StableDelegateError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	// Write a corrupt delegates.jsonl.
	writeFile(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), "{not json}\n")
	res, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(res.Unreadable) == 0 {
		t.Errorf("expected unreadable session, got %d unreadable", len(res.Unreadable))
	}
}

// TestWithDelegateReason covers all branches of withDelegateReason.
func TestWithDelegateReason(t *testing.T) {
	if got := withDelegateReason("", ""); got != "" {
		t.Errorf("withDelegateReason(empty, empty) = %q, want empty", got)
	}
	if got := withDelegateReason("note", ""); got != "note" {
		t.Errorf("withDelegateReason(note, empty) = %q, want note", got)
	}
	if got := withDelegateReason("", "disposed"); got != "disposed" {
		t.Errorf("withDelegateReason(empty, disposed) = %q, want disposed", got)
	}
	if got := withDelegateReason("note", "disposed"); got != "disposed; note" {
		t.Errorf("withDelegateReason(note, disposed) = %q, want 'disposed; note'", got)
	}
}

// TestPlural covers the plural helper including the "entry" special case.
func TestPlural(t *testing.T) {
	cases := []struct {
		n    int
		noun string
		want string
	}{
		{1, "mutation", "mutation"},
		{2, "mutation", "mutations"},
		{0, "mutation", "mutations"},
		{1, "entry", "entry"},
		{2, "entry", "entries"},
		{0, "entry", "entries"},
	}
	for _, tc := range cases {
		if got := plural(tc.n, tc.noun); got != tc.want {
			t.Errorf("plural(%d, %q) = %q, want %q", tc.n, tc.noun, got, tc.want)
		}
	}
}

// TestCallArgsEndTurn covers the malformed-JSON path of callArgsEndTurn.
func TestCallArgsEndTurn(t *testing.T) {
	if callArgsEndTurn(json.RawMessage(`{not json`)) {
		t.Error("callArgsEndTurn(malformed) = true, want false")
	}
	if !callArgsEndTurn(json.RawMessage(`{"end_turn":true}`)) {
		t.Error("callArgsEndTurn(true) = false, want true")
	}
	if callArgsEndTurn(json.RawMessage(`{"end_turn":false}`)) {
		t.Error("callArgsEndTurn(false) = true, want false")
	}
}

// TestTranscriptHealth_SteeringKindUnknown covers the SteeringKind="" branch
// (falls back to "unknown") in TranscriptHealth.
func TestTranscriptHealth_SteeringKindUnknown(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	// Build a session with a steering turn that has an empty SteeringKind.
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Unix(1, 0).UTC()},
		[]schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("hi")}}),
			schema.NewTurn(schema.TurnSteering, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("steer")}}),
		},
		schema.SessionMeta{TurnCount: 2},
		nil,
		time.Unix(2, 0).UTC(),
	)
	r, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatalf("TranscriptHealth: %v", err)
	}
	if r.Steering["unknown"] == 0 {
		t.Errorf("expected Steering[unknown] >= 1, got %v", r.Steering)
	}
}

// TestTranscriptHealth_NilToolCallAndResult covers the nil-guard continue
// branches for ContentToolCall and ContentToolResult in TranscriptHealth.
func TestTranscriptHealth_NilToolCallAndResult(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	// Build a session with a nil ToolCall and nil ToolResult content part.
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Unix(1, 0).UTC()},
		[]schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: nil},
				{Kind: llm.ContentToolResult, ToolResult: nil},
			}}),
		},
		schema.SessionMeta{TurnCount: 1},
		nil,
		time.Unix(2, 0).UTC(),
	)
	r, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatalf("TranscriptHealth: %v", err)
	}
	// The nil tool call should not register as a tool call.
	if len(r.ToolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %v", r.ToolCalls)
	}
}

// TestTranscriptHealth_JobsReadError covers the ReadEvents error path in
// TranscriptHealth: a corrupt jobs.jsonl should produce an error.
func TestTranscriptHealth_JobsReadError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	writeFile(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), "{not json}\n")
	_, err := TranscriptHealth(base, sid)
	if err == nil {
		t.Fatal("expected error from corrupt jobs.jsonl, got nil")
	}
}

// TestTranscriptHealth_JobsTerminalReasons covers the jobs health summary:
// terminal jobs with reasons and zero output.
func TestTranscriptHealth_JobsTerminalReasons(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	exitOK := 0
	ended := time.Unix(10, 0).UTC()
	// Write the transcript first (writeSessionsFixtureSession also creates an
	// empty jobs.jsonl).
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Unix(1, 0).UTC()},
		[]schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("hi")}}),
		},
		schema.SessionMeta{TurnCount: 1},
		[]jobstore.Event{
			{Kind: jobstore.EventJobStarted, JobID: "job_h1", Type: jobstore.JobShell, Command: "true"},
			{Kind: jobstore.EventJobFinished, JobID: "job_h1", Status: jobstore.StatusCompleted,
				ExitCode: &exitOK, EndedAt: &ended, OutputBytes: 0, Reason: ""},
			{Kind: jobstore.EventJobStarted, JobID: "job_h2", Type: jobstore.JobShell, Command: "false"},
			{Kind: jobstore.EventJobFinished, JobID: "job_h2", Status: jobstore.StatusStopped,
				ExitCode: &exitOK, EndedAt: &ended, OutputBytes: 10, Reason: "run_timeout"},
		},
		time.Unix(2, 0).UTC(),
	)
	r, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatalf("TranscriptHealth: %v", err)
	}
	if r.Jobs.ByTerminalReason["run_timeout"] != 1 {
		t.Errorf("expected 1 run_timeout terminal reason, got %v", r.Jobs.ByTerminalReason)
	}
	if r.Jobs.ZeroOutputTerminal != 1 {
		t.Errorf("expected 1 zero-output terminal job, got %d", r.Jobs.ZeroOutputTerminal)
	}
}

// TestDecodeClientMutationStore_TrailingData covers the trailing-data error
// path in decodeClientMutationStore: a second JSON value after the store is
// rejected.
func TestDecodeClientMutationStore_TrailingData(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	// A valid store JSON followed by trailing garbage.
	storePath := filepath.Join(bucket, "mutations", sid+".json")
	valid := `{"version":1,"session_id":"` + sid + `","active_turn_id":"","accepted_turns":0,"queue_revision":0,"journal":{},"budget_reservations":{},"pending_executions":{}}`
	writeFile(t, storePath, valid+` 42`)
	_, err := Mutations(base, sid)
	if err == nil {
		t.Fatal("expected trailing-data error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("expected trailing-data error, got: %v", err)
	}
}

// TestDecodeClientMutationStore_ReadError covers the os.ReadFile non-NotExist
// error path in Mutations: reading a directory should fail.
func TestDecodeClientMutationStore_ReadError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	storePath := filepath.Join(bucket, "mutations", sid+".json")
	// Make it a directory so os.ReadFile fails with a non-NotExist error.
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Mutations(base, sid)
	if err == nil {
		t.Fatal("expected read error, got nil")
	}
}

// TestMutations_SortPendingExecutions covers the PendingExecutions sort path
// (line 141-143): multiple pending executions should be sorted by
// ClientMutationID.
func TestMutations_SortPendingExecutions(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	storePath := filepath.Join(bucket, "mutations", sid+".json")
	// A store with two pending executions in reverse id order.
	store := `{"version":1,"session_id":"` + sid + `","active_turn_id":"","accepted_turns":0,"queue_revision":0,"journal":{},"budget_reservations":{},"pending_executions":{
		"mut_b":{"client_mutation_id":"mut_b","method":"test","execution_state":"pending","turn_id":"t2"},
		"mut_a":{"client_mutation_id":"mut_a","method":"test","execution_state":"pending","turn_id":"t1"}
	}}`
	writeFile(t, storePath, store)
	r, err := Mutations(base, sid)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if !r.Present {
		t.Fatal("expected Present=true")
	}
	if len(r.PendingExecutions) != 2 {
		t.Fatalf("expected 2 pending executions, got %d", len(r.PendingExecutions))
	}
	if r.PendingExecutions[0].ClientMutationID != "mut_a" || r.PendingExecutions[1].ClientMutationID != "mut_b" {
		t.Errorf("pending executions not sorted: %v", r.PendingExecutions)
	}
}
