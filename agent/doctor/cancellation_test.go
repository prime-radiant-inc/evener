package doctor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// Each test below cancels from inside one reader stage (the context reports
// Canceled after Allow polls, so the cancel lands mid-scan with no sleeps) and
// asserts the stage stopped with the cancellation error instead of finishing.

func cancelMidRead(allow int32) *agenttest.CountdownContext {
	return &agenttest.CountdownContext{Context: context.Background(), Allow: allow}
}

func manyUserTurns(n int) []schema.Turn {
	turns := make([]schema.Turn, n)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("x")}})
	}
	return turns
}

func TestTranscriptHealthCancelDuringTranscriptRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	writeAuditSession(t, stateHomeBucket(base, hash1), sid, manyUserTurns(200), nil)

	if _, err := TranscriptHealth(cancelMidRead(20), base, sid); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestTranscriptHealthCancelDuringJobsRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	var jobs []jobstore.Event
	for i := range 100 {
		jobs = append(jobs, fiveRunTimeoutJobsFor(fmt.Sprintf("%s_%d", sid, i))...)
	}
	writeAuditSession(t, stateHomeBucket(base, hash1), sid, manyUserTurns(1), jobs)

	if _, err := TranscriptHealth(cancelMidRead(20), base, sid); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestAPILogCancelDuringDecode(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	var records []apilog.APILogRecord
	for i := range 200 {
		records = append(records, doctorAttempt(fmt.Sprintf("ag_%d", i), 1, apilog.AttemptSuccess, 10, 100, 20, 30, 5, 0))
	}
	writeRichSession(t, stateHomeBucket(base, hash1), sid, nil, records, schema.SessionMeta{})

	if _, err := APILog(cancelMidRead(20), base, sid, APILogOpts{SummaryOnly: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("APILog err = %v, want context.Canceled", err)
	}
	if _, err := APIHealth(cancelMidRead(20), base, sid); !errors.Is(err, context.Canceled) {
		t.Fatalf("APIHealth err = %v, want context.Canceled", err)
	}
}

func TestListSessionsCancelDuringJobsRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	var jobs []jobstore.Event
	for i := range 100 {
		jobs = append(jobs, fiveRunTimeoutJobsFor(fmt.Sprintf("%s_%d", sid, i))...)
	}
	writeSessionsFixtureSession(t, stateHomeBucket(base, hash1), sid,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m"}, jobs, time.Now())

	if _, err := ListSessions(cancelMidRead(20), base, SessionsOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestListSessionsCancelDuringDelegateJournalRead(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := newSessionsTestSID(t)
	writeSessionsFixtureSession(t, bucket, sid,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, time.Now())
	// One batch line per event: the countdown context fires once its allowed
	// observations run out, and a line-per-event journal gives the scan a
	// cancellation checkpoint per line — however few underlying reads the
	// scan pays — instead of one per buffer fill.
	writeDelegateEventsPerLine(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), manyDelegateEvents(sid, 200))

	if _, err := ListSessions(cancelMidRead(20), base, SessionsOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// RunAudit's per-session health read honors cancellation mid-transcript.
func TestRunAuditCancelDuringHealthRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	writeAuditSession(t, stateHomeBucket(base, hash1), sid, manyUserTurns(200), nil)

	res, err := RunAudit(cancelMidRead(20), base, mustParseFixtureRunbook(t), AuditOpts{Sessions: []string{sid}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.SessionsChecked != 0 || len(res.Unreadable) != 0 {
		t.Errorf("cancelled audit returned partial result: %+v", res)
	}
}

// The tree sweep reads each node's jobs journal and delegate cache with the
// caller's ctx, so a cancelled walk stops instead of running to its depth and
// fan-out limits.
func TestTreeCancelDuringNodeScan(t *testing.T) {
	base, rootSID := treeFixture(t)
	bucket := stateHomeBucket(base, hash1)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", rootSID, "jobs.jsonl"), manyJobsEvents(rootSID, 100))

	if _, err := Tree(cancelMidRead(20), base, rootSID, TreeOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A ctx already cancelled before the walk begins stops it without reading
// anything.
func TestTreeAlreadyCancelledContext(t *testing.T) {
	base, rootSID := treeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	node, err := Tree(ctx, base, rootSID, TreeOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if node.SessionID != "" || len(node.Children) != 0 {
		t.Errorf("cancelled tree returned partial node: %+v", node)
	}
}

// The tree's per-node delegate read honours the caller's ctx. The session's
// journal holds only a delegate owned by another session, so the read happens
// but yields no children for the root — nothing after it checks ctx, which
// isolates this seam from the walk's own per-node and per-child checks.
func TestTreeCancelDuringDelegateRead(t *testing.T) {
	base, sid := childlessTreeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	restore := delegateJournalRead
	delegateJournalRead = func(string) { cancel() }
	t.Cleanup(func() { delegateJournalRead = restore })

	node, err := Tree(ctx, base, sid, TreeOpts{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if node.SessionID != "" || len(node.Children) != 0 {
		t.Errorf("cancelled tree returned partial node: %+v", node)
	}
}

// A depth-limited walk still reads the child's own delegates journal to decide
// whether to record the depth-limit note, and that read honours the caller's
// ctx. The child lives in another bucket and holds its own journal, so its
// read is a cache miss that reaches the scan's checkpoints; the hook cancels
// on exactly that read, leaving nothing after it able to notice the cancel.
func TestTreeCancelDuringDepthLimitNote(t *testing.T) {
	base, rootSID := treeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	childDelegates := filepath.Join(stateHomeBucket(base, hash2), "sessions", childSID, "delegates.jsonl")
	writeDelegateEvents(t, childDelegates, manyDelegateEvents(childSID, 100))
	restore := delegateJournalRead
	delegateJournalRead = func(path string) {
		if path == childDelegates {
			cancel()
		}
	}
	t.Cleanup(func() { delegateJournalRead = restore })

	node, err := Tree(ctx, base, rootSID, TreeOpts{Depth: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if node.SessionID != "" || len(node.Children) != 0 {
		t.Errorf("cancelled tree returned partial node: %+v", node)
	}
}

// childlessTreeFixture writes a session whose delegates journal really exists
// (so a delegate read is paid) but records no delegate the session owns, so
// the tree walk has no children to expand after the read.
func childlessTreeFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "delOther", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID: sidB, TranscriptRef: "proj:" + hash1 + ":" + sidB,
			OwnerSessionID: sidB, VisibleSessionID: sidB, Task: "another session's delegate",
			AgentType: "explorer", ToolNameCeiling: []string{"communicate"}, Resumable: true,
		}}},
	})
	return base, sid
}

// The jobs report reads both journals — jobs.jsonl and the session's
// delegates.jsonl — with the caller's ctx.
func TestJobsCancelDuringJobsRead(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), manyJobsEvents(sid, 100))

	if _, err := Jobs(cancelMidRead(20), base, sid, JobOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestJobsCancelDuringDelegateJournalRead(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	// An empty jobs journal costs two polls, so the countdown below can only
	// run out inside the delegate read.
	writeDelegateEventsPerLine(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), manyDelegateEvents(sid, 200))

	if _, err := Jobs(cancelMidRead(20), base, sid, JobOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// The watches report reads jobs.jsonl — where watch_send_* events live — with
// the caller's ctx.
func TestWatchesCancelDuringJobsRead(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidA
	writeSession(t, bucket, sid)
	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), manyJobsEvents(sid, 100))

	if _, err := Watches(cancelMidRead(20), base, sid, WatchOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Count reads the transcript with the caller's ctx.
func TestCountCancelDuringTranscriptRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	writeAuditSession(t, stateHomeBucket(base, hash1), sid, manyUserTurns(200), nil)

	if _, err := Count(cancelMidRead(20), base, sid, "read_file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// Transcript reads the transcript with the caller's ctx.
func TestTranscriptCancelDuringTranscriptRead(t *testing.T) {
	base := t.TempDir()
	sid := newSessionsTestSID(t)
	writeAuditSession(t, stateHomeBucket(base, hash1), sid, manyUserTurns(200), nil)

	if _, err := Transcript(cancelMidRead(20), base, sid, TranscriptOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// manyDelegateEvents builds n one-delegate-per-event records for a session, so
// a journal scan has a cancellation checkpoint per batch line however few
// underlying reads it pays.
func manyDelegateEvents(sid string, n int) []delegatestore.Event {
	events := make([]delegatestore.Event, 0, n)
	for i := range n {
		child := fmt.Sprintf("%s%d", sid[:len(sid)-4], i)
		events = append(events, delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: fmt.Sprintf("del%d", i),
			Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
				ChildSessionID: child, TranscriptRef: "proj:" + hash1 + ":" + child,
				OwnerSessionID: sid, VisibleSessionID: sid,
				Task: "t", AgentType: "explorer", ToolNameCeiling: []string{"communicate"}, Resumable: true,
			}}})
	}
	return events
}

// writeDelegateEventsPerLine appends each event as its own batch line, so a
// scan that passes the 64 KiB read buffer still pays a cancellation
// checkpoint per event. writeDelegateEvents' single AppendBatch would put
// them all on one line, and the countdown could only fire once per buffer
// fill.
func writeDelegateEventsPerLine(t testing.TB, delegatesPath string, events []delegatestore.Event) {
	t.Helper()
	store, err := delegatestore.Open(delegatesPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	for _, event := range events {
		_, state, err = store.AppendBatch(state, []delegatestore.Event{event})
		if err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// manyJobsEvents builds n finished jobs for one session, one event per line in
// the journal, so a jobs scan pays a cancellation checkpoint per job.
func manyJobsEvents(sid string, n int) []jobstore.Event {
	events := make([]jobstore.Event, 0, n*2)
	exitOK := 0
	for i := range n {
		id := fmt.Sprintf("job_%s_%d", sid, i)
		events = append(events,
			jobstore.Event{Kind: jobstore.EventJobStarted, JobID: id, Type: jobstore.JobShell, Command: "x",
				OwnerSessionID: sid, VisibleToSession: sid, StartedAt: &jobStartedAt},
			jobstore.Event{Kind: jobstore.EventJobFinished, JobID: id, Status: jobstore.StatusCompleted,
				ExitCode: &exitOK, EndedAt: &jobEndedAt},
		)
	}
	return events
}
