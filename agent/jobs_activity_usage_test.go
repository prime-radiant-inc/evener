package agent

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// TestLoadHistoricalActivityBaseReadsUsage pins that a retained session's own
// token totals come from its transcript's per-turn usage blocks, not from the
// session meta: the activity tree renders a delegate's spend even when the
// session closed before it persisted a cumulative total.
func TestLoadHistoricalActivityBaseReadsUsage(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := "histusagechild"
	savePastActivityMeta(t, stateDir, sessionID, "Usage child")
	writeOneHistoricalJob(t, stateDir, sessionID)

	tw, err := transcript.NewWriter(
		filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl"),
		transcript.Header{
			SessionID: sessionID,
			Model:     "gpt-5.2",
			CreatedAt: time.Unix(400, 0).UTC(),
		},
	)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	appendTurnWithUsage(t, tw, llm.Usage{InputTokens: 1200, OutputTokens: 300, TotalTokens: 1500})
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	loaded, err := loadHistoricalActivityBase(stateDir, sessionID, true)
	if err != nil {
		t.Fatalf("loadHistoricalActivityBase: %v", err)
	}
	if loaded.snapshot.Usage == nil {
		t.Fatal("snapshot.Usage is nil, want totals from the transcript")
	}
	if loaded.snapshot.Usage.InputTokens != 1200 || loaded.snapshot.Usage.OutputTokens != 300 || loaded.snapshot.Usage.TotalTokens != 1500 {
		t.Errorf("Usage = %+v, want 1200/300/1500", loaded.snapshot.Usage)
	}
}

// TestLoadHistoricalActivityBaseReadsUsageWithoutJobsFile covers the early
// no-jobs.jsonl return: usage must be filled there too, since a retained
// session that never ran a job still spent tokens.
func TestLoadHistoricalActivityBaseReadsUsageWithoutJobsFile(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := "histusagenojobs"
	savePastActivityMeta(t, stateDir, sessionID, "No jobs")

	tw, err := transcript.NewWriter(
		filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl"),
		transcript.Header{
			SessionID: sessionID,
			Model:     "gpt-5.2",
			CreatedAt: time.Unix(410, 0).UTC(),
		},
	)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	appendTurnWithUsage(t, tw, llm.Usage{InputTokens: 42, OutputTokens: 7, TotalTokens: 49})
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	loaded, err := loadHistoricalActivityBase(stateDir, sessionID, false)
	if err != nil {
		t.Fatalf("loadHistoricalActivityBase: %v", err)
	}
	if loaded.snapshot.Usage == nil {
		t.Fatal("snapshot.Usage is nil on the no-jobs-file path, want totals from the transcript")
	}
	if loaded.snapshot.Usage.InputTokens != 42 || loaded.snapshot.Usage.OutputTokens != 7 {
		t.Errorf("Usage = %+v, want 42/7", loaded.snapshot.Usage)
	}
}

// TestLoadHistoricalActivityBaseUsageNilWithoutTokenData pins the nil-not-zero
// contract: a transcript with no usage blocks leaves snapshot.Usage nil so the
// wire omits the field and the UI hides the token cluster instead of rendering
// ↑0 ↓0.
func TestLoadHistoricalActivityBaseUsageNilWithoutTokenData(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessionID := "histusagenone"
	savePastActivityMeta(t, stateDir, sessionID, "No usage")
	writeOneHistoricalJob(t, stateDir, sessionID)

	tw, err := transcript.NewWriter(
		filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl"),
		transcript.Header{
			SessionID: sessionID,
			Model:     "gpt-5.2",
			CreatedAt: time.Unix(420, 0).UTC(),
		},
	)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	appendTurnWithUsage(t, tw, llm.Usage{})
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	loaded, err := loadHistoricalActivityBase(stateDir, sessionID, true)
	if err != nil {
		t.Fatalf("loadHistoricalActivityBase: %v", err)
	}
	if loaded.snapshot.Usage != nil {
		t.Errorf("snapshot.Usage = %+v, want nil for a transcript with no token data", loaded.snapshot.Usage)
	}
}

// TestLoadHistoricalActivityBaseRejectsUnsafeSessionID is a kata 1gc4 sibling
// site: sessionID here can arrive as a descendant ID pulled out of a
// persisted job record (buildActivityContinuationAt), not only as an
// already-indexed root, so it must be refused before either the meta load or
// the jobs.jsonl path join below it — both of which happen unconditionally,
// the second one even when the meta load itself fails.
func TestLoadHistoricalActivityBaseRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	_, err := loadHistoricalActivityBase(stateDir, "../escaped", true)
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("loadHistoricalActivityBase(%q) error = %v, want schema.ErrInvalidSessionID", "../escaped", err)
	}
}

// writeOneHistoricalJob persists one completed shell job so the loader takes
// its main jobs.jsonl path rather than the no-jobs-file early return.
func writeOneHistoricalJob(t *testing.T, stateDir, sessionID string) {
	t.Helper()
	started := time.Unix(401, 0).UTC()
	ended := started.Add(time.Second)
	s1cov_writeJobLog(t, stateDir, sessionID,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_hist_shell", Type: jobstore.JobShell, OwnerSessionID: sessionID, VisibleToSession: sessionID, StartedAt: &started, Description: "historical shell"},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_hist_shell", Status: jobstore.StatusCompleted, EndedAt: &ended},
	)
}

// appendTurnWithUsage appends one assistant turn carrying the given usage,
// mirroring the writer idiom from atif_test.go.
func appendTurnWithUsage(t *testing.T, tw *transcript.Writer, usage llm.Usage) {
	t.Helper()
	if err := tw.Append(schema.Turn{
		Kind:      schema.TurnAssistant,
		Message:   llm.Assistant("done"),
		Timestamp: time.Unix(402, 0).UTC(),
		Usage:     usage,
	}); err != nil {
		t.Fatalf("Append turn with usage: %v", err)
	}
}
