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
	var events []delegatestore.Event
	for i := range 200 {
		child := newSessionsTestSID(t)
		events = append(events, delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: fmt.Sprintf("del%d", i),
			Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
				ChildSessionID: child, TranscriptRef: "proj:" + hash1 + ":" + child,
				OwnerSessionID: sid, VisibleSessionID: sid,
				Task: "t", AgentType: "explorer", ToolNameCeiling: []string{"communicate"}, Resumable: true,
			}}})
	}
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", sid, "delegates.jsonl"), events)

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
