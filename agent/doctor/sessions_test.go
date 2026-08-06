package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

// newSessionsTestSID mints a real, validator-passing session id — the fixed
// consts (sidA, sidB, hash1, hash2) shared across doctor's tests cover only
// two session ids, so a three-session fixture needs a generator.
func newSessionsTestSID(t *testing.T) string {
	t.Helper()
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// writeSessionsFixtureSession writes one session's transcript (with a real
// header and entries), meta, and jobs.jsonl, then stamps the transcript
// file's mtime — the signal ListSessions reads as "last activity" — to a
// caller-chosen time so --since filtering and ordering are deterministic
// without depending on wall-clock timing during the test run.
func writeSessionsFixtureSession(t *testing.T, bucketDir, sid string, header transcript.Header, turns []schema.Turn, meta schema.SessionMeta, jobsEvents []jobstore.Event, mtime time.Time) {
	t.Helper()
	path := filepath.Join(bucketDir, "sessions", sid+".transcript.jsonl")
	header.SessionID = sid
	w, err := transcript.NewWriterNoSync(path, header)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	meta.ID = sid
	if err := schema.SaveSessionMeta(bucketDir, meta); err != nil {
		t.Fatal(err)
	}

	// writeJobsEvents' underlying jobstore.OpenNoSync does not create the
	// per-session subdir, so always lay down the (possibly empty) jobs.jsonl
	// first — mirroring writeSession's convention — before appending events.
	jobsPath := filepath.Join(bucketDir, "sessions", sid, "jobs.jsonl")
	writeFile(t, jobsPath, "")
	if len(jobsEvents) > 0 {
		writeJobsEvents(t, jobsPath, jobsEvents)
	}
}

// sessionsFixture builds three sessions in one bucket:
//   - root: recent last activity, delegates to child, has one observer, ends
//     with communicate(end_turn=true) — the outcome hint.
//   - child (subagent): recent last activity (slightly after root's, for
//     ordering), spawned by root (transcript header ParentSessionID),
//     meta.IsSubagent=true, last ASSISTANT turn has no result-tool call —
//     outcome hint "none".
//   - old: old last activity, outside any reasonable --since window.
//
// Returns base and the session ids, so tests can assert on specific rows.
func sessionsFixture(t *testing.T) (base, root, child, oldSID string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	root = newSessionsTestSID(t)
	child = newSessionsTestSID(t)
	oldSID = newSessionsTestSID(t)
	observer := newSessionsTestSID(t)

	now := time.Now()
	recent := now.Add(-1 * time.Hour)
	old := now.Add(-1000 * time.Hour)

	// root: delegates to the child, observed by one observer, ends its last
	// assistant turn with a communicate(end_turn=true) call.
	writeSessionsFixtureSession(t, bucket, root,
		transcript.Header{CreatedAt: now.Add(-2 * time.Hour), Model: "anthropic/claude-a"},
		[]schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("go")}}),
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				toolCall("communicate", `{"message":"done","end_turn":true,"status":"success"}`),
			}}),
		},
		schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 2, ObservedBy: []string{observer}},
		[]jobstore.Event{
			{Kind: jobstore.EventDelegateCreated, DelegateID: "del1", Delegate: &jobstore.DelegateEvent{
				ChildSessionID: child,
				TranscriptRef:  "proj:" + hash1 + ":" + child,
				AgentType:      "explorer",
			}},
		},
		recent,
	)

	// child: subagent spawned by root; last assistant turn has a plain text
	// reply, no communicate call, so the outcome hint is "none".
	writeSessionsFixtureSession(t, bucket, child,
		transcript.Header{CreatedAt: now.Add(-90 * time.Minute), Model: "anthropic/claude-a", ParentSessionID: root},
		[]schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{assistantText("still working")}}),
		},
		schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 1, IsSubagent: true},
		nil,
		recent.Add(1*time.Minute), // slightly more recent than root, for ordering
	)

	// oldSID: outside the --since window.
	writeSessionsFixtureSession(t, bucket, oldSID,
		transcript.Header{CreatedAt: old, Model: "anthropic/claude-a"},
		[]schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("ancient")}}),
		},
		schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 1},
		nil,
		old,
	)

	return base, root, child, oldSID
}

func TestListSessions_EnumeratesAllByDefault(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (no --since filter): %+v", len(rows), rows)
	}
}

func TestListSessions_SinceFiltersOldSessions(t *testing.T) {
	base, _, _, oldSID := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (oldSID is outside the 24h window): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.SessionID == oldSID {
			t.Errorf("oldSID should have been filtered by --since 24h, got it in rows: %+v", r)
		}
	}
}

func TestListSessions_SortedByLastActivityDescending(t *testing.T) {
	base, _, child, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("need at least 2 rows to check order, got %d", len(rows))
	}
	if rows[0].SessionID != child {
		t.Errorf("rows[0] = %s, want %s (most recent last-activity first)", rows[0].SessionID, child)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].LastActivity.Before(rows[i].LastActivity) {
			t.Errorf("rows not sorted by last activity descending: row %d (%s, %s) before row %d (%s, %s)",
				i-1, rows[i-1].SessionID, rows[i-1].LastActivity, i, rows[i].SessionID, rows[i].LastActivity)
		}
	}
}

func TestListSessions_ParentLinkageAndSubagentFlag(t *testing.T) {
	base, root, child, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var childRow *SessionRow
	for i := range rows {
		if rows[i].SessionID == child {
			childRow = &rows[i]
		}
	}
	if childRow == nil {
		t.Fatalf("child session %s missing from rows: %+v", child, rows)
	}
	if !childRow.IsSubagent {
		t.Error("child.IsSubagent = false, want true")
	}
	if childRow.ParentSessionID != root {
		t.Errorf("child.ParentSessionID = %q, want %q", childRow.ParentSessionID, root)
	}
}

func TestListSessions_OutcomeHint(t *testing.T) {
	base, root, child, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SessionRow{}
	for _, r := range rows {
		byID[r.SessionID] = r
	}
	if got := byID[root].Outcome; got != "end_turn=true status=success" {
		t.Errorf("root outcome = %q, want end_turn=true status=success", got)
	}
	if got := byID[child].Outcome; got != "none" {
		t.Errorf("child outcome = %q, want none (last assistant turn has no result-tool call)", got)
	}
}

func TestListSessions_DelegateAndObserverCounts(t *testing.T) {
	base, root, _, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var rootRow SessionRow
	for _, r := range rows {
		if r.SessionID == root {
			rootRow = r
		}
	}
	if rootRow.DelegateCount != 1 {
		t.Errorf("root DelegateCount = %d, want 1", rootRow.DelegateCount)
	}
	if rootRow.ObserverCount != 1 {
		t.Errorf("root ObserverCount = %d, want 1", rootRow.ObserverCount)
	}
}

func TestListSessions_ModelsFromHeaderAndMeta(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSessionsFixtureSession(t, bucket, sidA,
		transcript.Header{CreatedAt: time.Now(), Model: "anthropic/claude-old"},
		nil,
		schema.SessionMeta{Model: "anthropic/claude-new", TurnCount: 0},
		nil,
		time.Now(),
	)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	want := []string{"anthropic/claude-old", "anthropic/claude-new"}
	if len(rows[0].Models) != 2 || rows[0].Models[0] != want[0] || rows[0].Models[1] != want[1] {
		t.Errorf("Models = %v, want %v (header start model, then meta's current model — the mid-session switch trace)", rows[0].Models, want)
	}
}

func TestListSessions_BucketFilter(t *testing.T) {
	base := t.TempDir()
	bucketA := stateHomeBucket(base, hash1)
	bucketB := stateHomeBucket(base, hash2)
	now := time.Now()
	writeSessionsFixtureSession(t, bucketA, sidA, transcript.Header{CreatedAt: now, Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, now)
	writeSessionsFixtureSession(t, bucketB, sidB, transcript.Header{CreatedAt: now, Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, now)

	rows, err := ListSessions(base, SessionsOpts{Bucket: hash1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SessionID != sidA {
		t.Fatalf("--bucket %s rows = %+v, want just sidA", hash1, rows)
	}
}

func TestListSessions_UnreadableTranscriptIsLoudError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	path := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl")
	writeFile(t, path, "not valid json\n")
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sidA}); err != nil {
		t.Fatal(err)
	}

	_, err := ListSessions(base, SessionsOpts{})
	if err == nil {
		t.Fatal("want a loud error for the unparseable transcript, got nil")
	}
	if !strings.Contains(err.Error(), sidA) && !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the unreadable file/session, got: %v", err)
	}
}

func TestListSessions_TranscriptBytes(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.TranscriptBytes <= 0 {
			t.Errorf("session %s TranscriptBytes = %d, want > 0", r.SessionID, r.TranscriptBytes)
		}
	}
}

func TestRenderSessions_HumanTable(t *testing.T) {
	base, root, child, oldSID := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderSessions(rows)
	for _, want := range []string{root, child, oldSID, "sessions=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered table missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSessions_Empty(t *testing.T) {
	out := RenderSessions(nil)
	if !strings.Contains(out, "no sessions") {
		t.Errorf("empty render should say so, got: %q", out)
	}
}

// TestListSessions_JSONFields is a smoke test that SessionRow's JSON shape is
// stable and carries the fields a batch study script would parse.
func TestListSessions_JSONFields(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	rows, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "bucket", "started_at", "last_activity", "models", "turn_count", "transcript_bytes", "is_subagent", "delegate_count", "observer_count", "outcome"} {
		if _, ok := m[key]; !ok {
			t.Errorf("SessionRow JSON missing field %q: %s", key, string(b))
		}
	}
}
