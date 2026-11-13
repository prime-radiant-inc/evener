package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// newSessionsTestSID mints a real, validator-passing session id — the fixed
// consts (sidA, sidB, hash1, hash2) shared across doctor's tests cover only
// two session ids, so a three-session fixture needs a generator.
func newSessionsTestSID(t testing.TB) string {
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
func writeSessionsFixtureSession(t testing.TB, bucketDir, sid string, header transcript.Header, turns []schema.Turn, meta schema.SessionMeta, jobsEvents []jobstore.Event, mtime time.Time) {
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

// communicateCall builds a schema-valid communicate tool call: end_turn plus
// the full output envelope DefCommunicateNamed requires (message, data,
// artifacts) — additionalProperties:false at both the top level and inside
// output means no other shape is one the runtime could ever have produced.
// decision, when non-empty, is nested at output.decision — the shape
// provider.WithAllowedDecisions actually injects (addDecisionToSchema mutates
// output.properties, not the top-level schema).
func communicateCall(endTurn bool, decision string) llm.ContentPart {
	output := map[string]any{"message": "done", "data": map[string]any{}, "artifacts": []string{}}
	if decision != "" {
		output["decision"] = decision
	}
	args, err := json.Marshal(map[string]any{
		"message":  "done",
		"end_turn": endTurn,
		"output":   output,
	})
	if err != nil {
		panic(err)
	}
	return toolCall("communicate", string(args))
}

// sessionsFixture builds three sessions in one bucket:
//   - root: recent last activity, delegates to child, has one observer, ends
//     with a communicate(end_turn=true, output.decision="approve") call — the
//     outcome hint.
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
	// assistant turn with a communicate(end_turn=true, output.decision) call.
	writeSessionsFixtureSession(t, bucket, root,
		transcript.Header{CreatedAt: now.Add(-2 * time.Hour), Model: "anthropic/claude-a"},
		[]schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("go")}}),
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				communicateCall(true, "approve"),
			}}),
		},
		schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 2, ObservedBy: []string{observer}},
		nil,
		recent,
	)
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", root, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "del1", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   child,
			TranscriptRef:    "proj:" + hash1 + ":" + child,
			OwnerSessionID:   root,
			VisibleSessionID: root,
			Task:             "explore fixture",
			AgentType:        "explorer",
			ToolNameCeiling:  []string{"communicate"},
			Resumable:        true,
		}}},
	})

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
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3 (no --since filter): %+v", len(res.Sessions), res.Sessions)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("unreadable = %+v, want none", res.Unreadable)
	}
}

func TestListSessions_SinceFiltersOldSessions(t *testing.T) {
	base, _, _, oldSID := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (oldSID is outside the 24h window): %+v", len(res.Sessions), res.Sessions)
	}
	for _, r := range res.Sessions {
		if r.SessionID == oldSID {
			t.Errorf("oldSID should have been filtered by --since 24h, got it in rows: %+v", r)
		}
	}
}

func TestListSessions_SortedByLastActivityDescending(t *testing.T) {
	base, _, child, _ := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Sessions
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
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var childRow *SessionRow
	for i := range res.Sessions {
		if res.Sessions[i].SessionID == child {
			childRow = &res.Sessions[i]
		}
	}
	if childRow == nil {
		t.Fatalf("child session %s missing from rows: %+v", child, res.Sessions)
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
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SessionRow{}
	for _, r := range res.Sessions {
		byID[r.SessionID] = r
	}
	if got := byID[root].Outcome; got != "end_turn=true decision=approve" {
		t.Errorf("root outcome = %q, want end_turn=true decision=approve", got)
	}
	if got := byID[child].Outcome; got != "none" {
		t.Errorf("child outcome = %q, want none (last assistant turn has no result-tool call)", got)
	}
}

// TestListSessions_OutcomeHint_TopLevelStatusIsNotDecision guards against
// regressing to reading a schema-invalid top-level "status" field: no
// schema-valid communicate call can carry one (DefCommunicateNamed sets
// additionalProperties:false at the top level), so a call that only has one
// must report the same as a call with no decision at all.
func TestListSessions_OutcomeHint_TopLevelStatusIsNotDecision(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Now(), Model: "m"},
		[]schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				toolCall("communicate", `{"message":"done","end_turn":true,"status":"success"}`),
			}}),
		},
		schema.SessionMeta{Model: "m", TurnCount: 1},
		nil,
		time.Now(),
	)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(res.Sessions))
	}
	if got := res.Sessions[0].Outcome; got != "end_turn=true" {
		t.Errorf("outcome = %q, want end_turn=true (a top-level status field is not schema-valid and must not surface)", got)
	}
}

func TestListSessions_OutcomeHint_SkipsCompactionFoldCopies(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	earlierAssistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		toolCall("read_file", `{"path":"a"}`),
	}})
	finalAssistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		communicateCall(true, ""),
	}})
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Now(), Model: "m"},
		[]schema.Turn{
			earlierAssistant,
			finalAssistant,
			schema.NewTurn(schema.TurnCheckpoint, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("checkpoint")}}),
			foldCopy(earlierAssistant, 0),
		},
		schema.SessionMeta{TurnCount: 2},
		nil,
		time.Now(),
	)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(res.Sessions))
	}
	if got := res.Sessions[0].Outcome; got != "end_turn=true" {
		t.Errorf("outcome = %q, want end_turn=true", got)
	}
}

func TestListSessions_DelegateAndObserverCounts(t *testing.T) {
	base, root, _, _ := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var rootRow SessionRow
	for _, r := range res.Sessions {
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
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(res.Sessions))
	}
	want := []string{"anthropic/claude-old", "anthropic/claude-new"}
	if len(res.Sessions[0].Models) != 2 || res.Sessions[0].Models[0] != want[0] || res.Sessions[0].Models[1] != want[1] {
		t.Errorf("Models = %v, want %v (header start model, then meta's current model — the mid-session switch trace)", res.Sessions[0].Models, want)
	}
}

func TestListSessions_BucketFilter(t *testing.T) {
	base := t.TempDir()
	bucketA := stateHomeBucket(base, hash1)
	bucketB := stateHomeBucket(base, hash2)
	now := time.Now()
	writeSessionsFixtureSession(t, bucketA, sidA, transcript.Header{CreatedAt: now, Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, now)
	writeSessionsFixtureSession(t, bucketB, sidB, transcript.Header{CreatedAt: now, Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, now)

	res, err := ListSessions(context.Background(), base, SessionsOpts{Bucket: hash1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].SessionID != sidA {
		t.Fatalf("--bucket %s sessions = %+v, want just sidA", hash1, res.Sessions)
	}
}

// TestListSessions_UnreadableSessionIsListedNotFatal is Finding 3's core
// assertion: one corrupt session must not abort the whole sweep. It's listed
// in Unreadable, by name, with its error — and every other session in the
// same bucket still comes back in Sessions.
func TestListSessions_UnreadableSessionIsListedNotFatal(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)

	// A healthy sibling session that must survive the corrupt one.
	writeSessionsFixtureSession(t, bucket, sidB,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m", TurnCount: 0}, nil, time.Now())

	// The corrupt session: an unparseable transcript.
	path := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl")
	writeFile(t, path, "not valid json\n")
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sidA}); err != nil {
		t.Fatal(err)
	}

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions should not fail the whole sweep on one corrupt session, got: %v", err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].SessionID != sidB {
		t.Fatalf("sidB should still be enumerated despite sidA's corruption, got sessions: %+v", res.Sessions)
	}
	if len(res.Unreadable) != 1 {
		t.Fatalf("unreadable = %d, want 1: %+v", len(res.Unreadable), res.Unreadable)
	}
	u := res.Unreadable[0]
	if u.SessionID != sidA {
		t.Errorf("unreadable session id = %q, want %q", u.SessionID, sidA)
	}
	if u.Error == "" {
		t.Error("unreadable entry should carry a non-empty error")
	}
	if !strings.Contains(u.Error, sidA) && !strings.Contains(u.Error, path) {
		t.Errorf("unreadable error should name the file/session, got: %v", u.Error)
	}
}

func TestListSessions_TranscriptBytes(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Sessions {
		if r.TranscriptBytes <= 0 {
			t.Errorf("session %s TranscriptBytes = %d, want > 0", r.SessionID, r.TranscriptBytes)
		}
	}
}

func TestRenderSessions_HumanTable(t *testing.T) {
	base, root, child, oldSID := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderSessions(res)
	for _, want := range []string{root, child, oldSID, "sessions=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered table missing %q:\n%s", want, out)
		}
	}
}

func TestRenderSessions_ListsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	path := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl")
	writeFile(t, path, "not valid json\n")
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sidA}); err != nil {
		t.Fatal(err)
	}
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderSessions(res)
	if !strings.Contains(out, sidA) || !strings.Contains(out, "could not be read") {
		t.Errorf("rendered table should list the unreadable session:\n%s", out)
	}
}

// TestRenderSessions_TimesRenderedInUTC is the final-review minor fix:
// StartedAt (doc.Header.CreatedAt) and LastActivity (a file mtime, which
// time.Stat returns local-zoned on most platforms) come from different
// clocks -- RenderSessions must normalize both to UTC rather than mixing
// zones across the two time columns.
func TestRenderSessions_TimesRenderedInUTC(t *testing.T) {
	loc := time.FixedZone("UTC-7", -7*60*60)
	started := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	lastActivity := time.Date(2026, 8, 5, 5, 0, 0, 0, loc) // == 12:00:00Z
	res := SessionsResult{Sessions: []SessionRow{{
		SessionID: sidA, StartedAt: started, LastActivity: lastActivity,
	}}}
	out := RenderSessions(res)
	if !strings.Contains(out, "2026-08-05T10:00:00Z") {
		t.Errorf("started column not rendered in UTC:\n%s", out)
	}
	if !strings.Contains(out, "2026-08-05T12:00:00Z") {
		t.Errorf("last_activity column not rendered in UTC:\n%s", out)
	}
	if strings.Contains(out, "-07:00") {
		t.Errorf("last_activity column still carries a non-UTC offset:\n%s", out)
	}
}

// TestListSessions_JSONFields is a smoke test that SessionsResult's JSON shape
// is stable and carries the fields a batch study script would parse.
func TestListSessions_JSONFields(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sessions", "unreadable"} {
		if _, ok := m[key]; !ok {
			t.Errorf("SessionsResult JSON missing field %q: %s", key, string(b))
		}
	}
	sessions, _ := m["sessions"].([]any)
	if len(sessions) == 0 {
		t.Fatalf("no sessions in JSON: %s", string(b))
	}
	row, _ := sessions[0].(map[string]any)
	for _, key := range []string{"session_id", "bucket", "started_at", "last_activity", "models", "turn_count", "transcript_bytes", "is_subagent", "delegate_count", "observer_count", "outcome"} {
		if _, ok := row[key]; !ok {
			t.Errorf("SessionRow JSON missing field %q: %s", key, string(b))
		}
	}
}

// hashTree fingerprints every path and byte under root, so a read-only
// enumeration can prove it left the state tree byte-identical.
func hashTree(t testing.TB, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestListSessions_CorruptMetaListedAsUnreadable is DATA-10's regression: a
// session whose .meta.json is present but unparseable must appear in
// Unreadable, not vanish from the enumeration. Before the fix, ListSessions
// delegated discovery to schema.ListSessionMetas, which silently skips corrupt
// metadata, so a corrupt session produced zero Sessions, zero Unreadable and a
// nil error — a clean-looking undercount of the sessions most in need of
// recovery. The forensic contract (see UnreadableSession) is that every
// primary identity is reported readable or unreadable exactly once, and the
// read-only sweep must not mutate the tree.
func TestListSessions_CorruptMetaListedAsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)

	// readable: a healthy session with transcript + meta.
	writeSessionsFixtureSession(t, bucket, sidB,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m", TurnCount: 0}, nil, time.Now())

	// unreadable (missing transcript): a valid meta with no transcript file.
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sidA}); err != nil {
		t.Fatal(err)
	}

	// unreadable (corrupt meta): a valid filename whose bytes are not JSON.
	corruptSID := newSessionsTestSID(t)
	corruptPath := filepath.Join(bucket, "sessions", corruptSID+".meta.json")
	writeFile(t, corruptPath, "{ this is not valid json")

	before := hashTree(t, base)

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	// Each of the three primary identities appears exactly once, as a row or
	// as an unreadable entry — never twice, never zero times.
	readable := map[string]int{}
	for _, r := range res.Sessions {
		readable[r.SessionID]++
	}
	unreadable := map[string]int{}
	for _, u := range res.Unreadable {
		unreadable[u.SessionID]++
	}
	for _, sid := range []string{sidA, sidB, corruptSID} {
		if got := readable[sid] + unreadable[sid]; got != 1 {
			t.Errorf("session %s appears %d times across readable+unreadable, want exactly 1 (readable=%d unreadable=%d)",
				sid, got, readable[sid], unreadable[sid])
		}
	}
	if readable[sidB] != 1 {
		t.Errorf("healthy session %s should be readable, got readable=%d unreadable=%d", sidB, readable[sidB], unreadable[sidB])
	}
	if unreadable[corruptSID] != 1 {
		t.Fatalf("corrupt-meta session %s should be reported unreadable, got unreadable=%+v", corruptSID, res.Unreadable)
	}
	for _, u := range res.Unreadable {
		if u.SessionID != corruptSID {
			continue
		}
		if u.Error == "" {
			t.Error("corrupt-meta unreadable entry should carry a non-empty error")
		}
		if u.TranscriptRef == "" {
			t.Errorf("corrupt-meta unreadable entry should carry a transcript ref: %+v", u)
		}
	}

	if after := hashTree(t, base); after != before {
		t.Errorf("ListSessions mutated the state tree: before=%s after=%s", before, after)
	}
}

// TestListSessions_SymlinkedMetaListedAsUnreadable covers the other
// present-but-unloadable metadata shape the DATA-10 contract names: a
// valid-id .meta.json that is a symlink. The sweep must not follow the link
// (the security refusal is unchanged) but must still name the session in
// Unreadable rather than silently dropping it.
func TestListSessions_SymlinkedMetaListedAsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sessDir := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := newSessionsTestSID(t)
	target := filepath.Join(t.TempDir(), sid+".meta.json")
	writeFile(t, target, `{"id":"`+sid+`"}`)
	if err := os.Symlink(target, filepath.Join(sessDir, sid+".meta.json")); err != nil {
		t.Fatal(err)
	}

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(res.Sessions) != 0 {
		t.Fatalf("symlinked meta must not produce a readable row, got %+v", res.Sessions)
	}
	if len(res.Unreadable) != 1 || res.Unreadable[0].SessionID != sid || res.Unreadable[0].Error == "" {
		t.Fatalf("unreadable = %+v, want one entry naming %s", res.Unreadable, sid)
	}
}

// An old session's transcript is never opened when --since excludes it: a
// deliberately malformed old transcript is neither a row nor Unreadable,
// while the same malformation on a session inside the window is reported.
func TestDoctorSessionsSinceSkipsOldTranscriptsUnread(t *testing.T) {
	base, root, child, _ := sessionsFixture(t)
	bucket := stateHomeBucket(base, hash1)
	corrupt := func(sid string, mtime time.Time) {
		t.Helper()
		writeSessionsFixtureSession(t, bucket, sid,
			transcript.Header{CreatedAt: mtime, Model: "anthropic/claude-a"}, nil,
			schema.SessionMeta{Model: "anthropic/claude-a"}, nil, mtime)
		path := filepath.Join(bucket, "sessions", sid+".transcript.jsonl")
		if err := os.WriteFile(path, []byte("not a transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	oldBad := newSessionsTestSID(t)
	recentBad := newSessionsTestSID(t)
	corrupt(oldBad, time.Now().Add(-1000*time.Hour))
	corrupt(recentBad, time.Now().Add(-time.Hour))

	res, err := ListSessions(context.Background(), base, SessionsOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, r := range res.Sessions {
		listed[r.SessionID] = true
	}
	if !listed[root] || !listed[child] || len(listed) != 2 {
		t.Errorf("rows = %v, want exactly root and child", listed)
	}
	unreadable := map[string]bool{}
	for _, u := range res.Unreadable {
		unreadable[u.SessionID] = true
	}
	if unreadable[oldBad] {
		t.Errorf("old malformed session was parsed despite being outside --since: %+v", res.Unreadable)
	}
	if !unreadable[recentBad] {
		t.Errorf("recent malformed session must still be reported unreadable: %+v", res.Unreadable)
	}
}

func TestDoctorSessionsStopsWhenContextCancelled(t *testing.T) {
	base, _, _, _ := sessionsFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := ListSessions(ctx, base, SessionsOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(res.Sessions) != 0 || len(res.Unreadable) != 0 {
		t.Errorf("cancelled census returned partial data: %+v", res)
	}
}

// Cancellation arriving while a session's transcript is being decoded stops
// the census with the cancellation error, even when that session is the last
// one (no later between-session check exists to catch it).
func TestDoctorSessionsCancelDuringSessionRead(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	turns := make([]schema.Turn, 200)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("x")}})
	}
	writeSessionsFixtureSession(t, bucket, newSessionsTestSID(t),
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, turns, schema.SessionMeta{Model: "m"}, nil, time.Now())

	ctx := &agenttest.CountdownContext{Context: context.Background(), Allow: 20}
	res, err := ListSessions(ctx, base, SessionsOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(res.Sessions) != 0 || len(res.Unreadable) != 0 {
		t.Errorf("cancelled census returned partial data: %+v", res)
	}
}

// sessionModels reports the creation model and, when it differs, the current
// model.
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

// A result call whose arguments do not parse has no outcome, the same as a
// session with no result call; a decision the call carries is surfaced.
func TestFormatOutcome(t *testing.T) {
	if got := formatOutcome(json.RawMessage(`{not json`)); got != "none" {
		t.Errorf("formatOutcome(malformed) = %q, want none", got)
	}
	plain := formatOutcome(json.RawMessage(`{"end_turn":true}`))
	decided := formatOutcome(json.RawMessage(`{"end_turn":true,"output":{"decision":"approve-sentinel"}}`))
	if !strings.Contains(decided, "approve-sentinel") || !strings.HasPrefix(decided, plain) {
		t.Errorf("formatOutcome(decision) = %q, want %q plus the decision", decided, plain)
	}
}

// A session whose jobs or delegates journal cannot be read is listed as
// unreadable, never dropped and never shown as a healthy row.
func TestListSessionsReportsASessionWithACorruptJournalUnreadable(t *testing.T) {
	for _, journal := range []string{"jobs.jsonl", "delegates.jsonl"} {
		t.Run(journal, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, hash1)
			writeSessionsFixtureSession(t, bucket, sidA, transcript.Header{CreatedAt: time.Unix(1, 0).UTC()}, nil, schema.SessionMeta{}, nil, time.Unix(2, 0).UTC())
			writeFile(t, filepath.Join(bucket, "sessions", sidA, journal), "{not json}\n")
			res, err := ListSessions(context.Background(), base, SessionsOpts{})
			if err != nil {
				t.Fatalf("ListSessions: %v", err)
			}
			if len(res.Sessions) != 0 || len(res.Unreadable) != 1 || res.Unreadable[0].SessionID != sidA {
				t.Fatalf("sessions = %d, unreadable = %+v; want the session listed unreadable", len(res.Sessions), res.Unreadable)
			}
		})
	}
}

// TestListSessions_BoundedTranscriptRead proves a sessions row reads only the
// transcript header plus a bounded entry tail: with the tail bound shrunk far
// below a large transcript, the row still carries the header's facts and the
// final result-tool outcome, while the bytes read from the transcript stay
// far below the file's size — a full scan walks the whole file.
// Not parallel: overrides sessionRowTranscriptTailBytes and the transcript
// open seam.
func TestListSessions_BoundedTranscriptRead(t *testing.T) {
	origTail, origOpen := sessionRowTranscriptTailBytes, openDoctorTranscriptFile
	sessionRowTranscriptTailBytes = 8 << 10
	var readBytes int64
	openDoctorTranscriptFile = func(path string) (io.ReadSeekCloser, error) {
		file, err := origOpen(path)
		if err != nil {
			return nil, err
		}
		return &countingTranscriptRead{ReadSeekCloser: file, read: &readBytes}, nil
	}
	t.Cleanup(func() {
		sessionRowTranscriptTailBytes = origTail
		openDoctorTranscriptFile = origOpen
	})

	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	createdAt := time.Now().Add(-2 * time.Hour).UTC()
	padding := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText(strings.Repeat("p", 4096))}})
	turns := make([]schema.Turn, 64)
	for i := range turns {
		turns[i] = padding
	}
	turns = append(turns, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{communicateCall(true, "approve")}}))
	writeSessionsFixtureSession(t, bucket, sid, transcript.Header{CreatedAt: createdAt, Model: "anthropic/claude-a"}, turns, schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: len(turns)}, nil, time.Now())
	transcriptPath := filepath.Join(bucket, "sessions", sid+".transcript.jsonl")
	info, err := os.Stat(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || len(res.Unreadable) != 0 {
		t.Fatalf("sessions = %d, unreadable = %+v", len(res.Sessions), res.Unreadable)
	}
	row := res.Sessions[0]
	if !row.StartedAt.Equal(createdAt) {
		t.Errorf("StartedAt = %s, want the header's %s", row.StartedAt, createdAt)
	}
	if len(row.Models) != 1 || row.Models[0] != "anthropic/claude-a" {
		t.Errorf("Models = %v, want [anthropic/claude-a] from the header", row.Models)
	}
	if row.Outcome != "end_turn=true decision=approve" {
		t.Errorf("Outcome = %q, want end_turn=true decision=approve from the tail's final turn", row.Outcome)
	}
	if readBytes >= info.Size()/2 {
		t.Fatalf("transcript read %d of %d bytes (tail bound %d); a bounded header+tail read must stay under half the file", readBytes, info.Size(), sessionRowTranscriptTailBytes)
	}
}

// countingTranscriptRead counts the bytes a transcript read pulls from the
// file, so a test can prove a summary stopped reading short of the end.
type countingTranscriptRead struct {
	io.ReadSeekCloser
	read *int64
}

func (c *countingTranscriptRead) Read(p []byte) (int, error) {
	n, err := c.ReadSeekCloser.Read(p)
	*c.read += int64(n)
	return n, err
}

// TestListSessions_BoundedJobsScan proves the jobs scan is bounded to the
// journal's head: with the bound shrunk, a corrupt line beyond the window no
// longer fails the session's row, while the retired delegate activation record
// at the head — what the failures fold exists to find — still reports.
// Not parallel: overrides sessionRowJobsMaxBytes.
func TestListSessions_BoundedJobsScan(t *testing.T) {
	origMax := sessionRowJobsMaxBytes
	sessionRowJobsMaxBytes = 512
	t.Cleanup(func() { sessionRowJobsMaxBytes = origMax })

	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	startedAt := time.Unix(100, 0).UTC()
	events := []jobstore.Event{{
		// The retired activation format: a delegate-type job, the oldest
		// record in the journal and the one the failures fold exists to find.
		Kind: jobstore.EventJobStarted, TS: startedAt, JobID: "delegate_legacy", Type: jobstore.JobType("delegate"), OwnerSessionID: sid, VisibleToSession: sid, StartedAt: &startedAt,
	}}
	// Padding shell jobs push the corrupt line far beyond the head window.
	for i := range 12 {
		ts := startedAt.Add(time.Duration(i) * time.Second)
		events = append(events, jobstore.Event{Kind: jobstore.EventJobStarted, TS: ts, JobID: fmt.Sprintf("shell_%02d", i), Type: jobstore.JobShell, OwnerSessionID: sid, VisibleToSession: sid, Command: "echo padding", StartedAt: &ts})
	}
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Unix(1, 0).UTC()},
		[]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("go")}})},
		schema.SessionMeta{}, events, time.Unix(2, 0).UTC())

	jobsPath := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	// The corrupt line is the one byte sequence the Store cannot write, so it
	// lands raw after the Store-shaped fixture lines, beyond the head window:
	// a full scan fails the session on it, the bounded scan never reaches it.
	jobsFile, err := os.OpenFile(jobsPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsFile.WriteString("{not json}\n"); err != nil {
		t.Fatal(err)
	}
	if err := jobsFile.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || len(res.Unreadable) != 0 {
		t.Fatalf("sessions = %d, unreadable = %+v; the corrupt line beyond the window must not fail the row", len(res.Sessions), res.Unreadable)
	}
	var foundLegacy bool
	for _, failure := range res.Sessions[0].Failures {
		if failure.Code == "legacy_delegate_state" {
			foundLegacy = true
		}
	}
	if !foundLegacy {
		t.Fatalf("Failures = %+v, want the head-window legacy delegate activation reported", res.Sessions[0].Failures)
	}
}

// TestListSessions_OverBoundTranscriptCorruptInWindowStaysUnreadable pins the
// window's unreadable-session contract: a transcript past the tail bound
// with a corrupt complete line inside the window fails the row exactly as the
// full scan would.
// Not parallel: overrides sessionRowTranscriptTailBytes.
func TestListSessions_OverBoundTranscriptCorruptInWindowStaysUnreadable(t *testing.T) {
	origTail := sessionRowTranscriptTailBytes
	sessionRowTranscriptTailBytes = 8 << 10
	t.Cleanup(func() { sessionRowTranscriptTailBytes = origTail })

	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	padding := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText(strings.Repeat("p", 4096))}})
	turns := make([]schema.Turn, 8)
	for i := range turns {
		turns[i] = padding
	}
	writeSessionsFixtureSession(t, bucket, sid, transcript.Header{CreatedAt: time.Unix(1, 0).UTC()}, turns, schema.SessionMeta{}, nil, time.Unix(2, 0).UTC())
	transcriptFile, err := os.OpenFile(filepath.Join(bucket, "sessions", sid+".transcript.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transcriptFile.WriteString("{not json}\n"); err != nil {
		t.Fatal(err)
	}
	if err := transcriptFile.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 0 || len(res.Unreadable) != 1 || res.Unreadable[0].SessionID != sid {
		t.Fatalf("sessions = %d, unreadable = %+v; the corrupt line inside the window must fail the row", len(res.Sessions), res.Unreadable)
	}
}

// TestListSessions_OutcomeBeyondFirstTailWindow proves the tail window grows
// until it holds the last assistant record: a session whose final assistant
// turn sits outside the first window still reports its true outcome instead
// of degrading to "none".
// Not parallel: overrides sessionRowTranscriptTailBytes.
func TestListSessions_OutcomeBeyondFirstTailWindow(t *testing.T) {
	origTail := sessionRowTranscriptTailBytes
	sessionRowTranscriptTailBytes = 8 << 10
	t.Cleanup(func() { sessionRowTranscriptTailBytes = origTail })

	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	// The last assistant turn sits ~12 KiB before the end — outside the
	// first 8 KiB window, inside the second — with only user padding after
	// it, which the outcome hint's backward walk skips.
	turns := make([]schema.Turn, 8)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText(strings.Repeat("p", 4096))}})
	}
	turns = append(turns, schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{communicateCall(true, "approve")}}))
	for range 3 {
		turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText(strings.Repeat("p", 4096))}}))
	}
	writeSessionsFixtureSession(t, bucket, sid, transcript.Header{CreatedAt: time.Unix(1, 0).UTC(), Model: "m"}, turns, schema.SessionMeta{Model: "m", TurnCount: len(turns)}, nil, time.Unix(2, 0).UTC())

	res, err := ListSessions(context.Background(), base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || len(res.Unreadable) != 0 {
		t.Fatalf("sessions = %d, unreadable = %+v", len(res.Sessions), res.Unreadable)
	}
	if got := res.Sessions[0].Outcome; got != "end_turn=true decision=approve" {
		t.Errorf("Outcome = %q, want the assistant turn beyond the first window's end_turn=true decision=approve", got)
	}
}
