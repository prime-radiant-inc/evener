package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	targetJobWatchID  = "watch_02wMz5TxvEMoJEDTDGOTa1"
	orphanWatchID     = "watch_02wMz5TxvEMoJEDTDGOTa2"
	sessionWatchID    = "watch_02wMz5TxvEMoJEDTDGOTa3"
	watchedJobID      = "job_02wMz5TxvEMoJEDTDGOTb1"
	unrecordedJobID   = "job_02wMz5TxvEMoJEDTDGOTb2"
	watchGenerationID = "wg_02wMz5TxvEMoJEDTDGOTc1"
)

// targetJobFixture reproduces the 2026-07-31 diagnosis shape: a watch on a job
// that was ALREADY stopped by the run timeout with zero output bytes, so its
// output_match condition could never match. It ends unfired
// (auto_removed_terminal) with no deliveries at all. Alongside it sit the two
// other target shapes: a watch on a job this log never recorded, and a watch on
// the session itself (target "caller"), which has no job to join.
func targetJobFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	exitTimeout := -1

	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: watchedJobID, Type: jobstore.JobShell,
			Command: "npm run dev", OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobFinished, JobID: watchedJobID, Status: jobstore.StatusStopped,
			Reason: "run_timeout", ExitCode: &exitTimeout, EndedAt: &jobEndedAt, OutputBytes: 0, TerminalGen: "tg1"},

		{Kind: jobstore.EventWatchRegistered, WatchID: targetJobWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: watchedJobID, SendTo: "caller", Condition: "output_match:ready", ConfigHash: "cfg1"}},
		{Kind: jobstore.EventWatchCleared, WatchID: targetJobWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, EndReason: "auto_removed_terminal"}},

		{Kind: jobstore.EventWatchRegistered, WatchID: orphanWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: unrecordedJobID, SendTo: "caller", Condition: "output_match:ready", ConfigHash: "cfg2"}},

		{Kind: jobstore.EventWatchRegistered, WatchID: sessionWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: "caller", SendTo: "caller", Condition: "assistant.message", ConfigHash: "cfg3"}},
	})
	return base, sid
}

// "Why didn't my watch fire": the answer is the target job's state, and it must
// be readable from the watch row itself.
func TestWatches_TargetJobStateJoined(t *testing.T) {
	base, sid := targetJobFixture(t)
	r, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	w := findWatch(r, targetJobWatchID)
	if w == nil {
		t.Fatalf("%s missing from report", targetJobWatchID)
	}
	if w.TargetJob == nil {
		t.Fatal("watch row carries no target job state — the answer to 'why didn't it fire' is missing")
	}
	if w.TargetJobMissing {
		t.Error("TargetJobMissing set for a target job this log DOES record")
	}
	if w.TargetJob.JobID != watchedJobID {
		t.Errorf("TargetJob.JobID = %q, want %q", w.TargetJob.JobID, watchedJobID)
	}
	if w.TargetJob.Status != "stopped" || w.TargetJob.Reason != "run_timeout" {
		t.Errorf("target job status/reason = %q/%q, want stopped/run_timeout", w.TargetJob.Status, w.TargetJob.Reason)
	}
	if w.TargetJob.ExitCode == nil || *w.TargetJob.ExitCode != -1 {
		t.Errorf("target job ExitCode = %v, want -1", w.TargetJob.ExitCode)
	}
	if w.TargetJob.OutputBytes != 0 {
		t.Errorf("target job OutputBytes = %d, want 0", w.TargetJob.OutputBytes)
	}
}

// The whole story in one block: ended unfired, zero deliveries, because the
// target was already dead and produced nothing to match.
func TestWatches_RenderTellsTheUnfiredStory(t *testing.T) {
	base, sid := targetJobFixture(t)
	r, _ := Watches(base, sid, WatchOpts{WatchID: targetJobWatchID})
	out := RenderWatches(r)

	for _, want := range []string{
		"(ended: auto_removed_terminal)",
		"condition=output_match:ready",
		"target job: status=stopped  reason=run_timeout  exit=-1  output_bytes=0",
		"deliveries: 0 distinct",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}
}

// A target job this session's log never recorded is reported as exactly that —
// never guessed at, and never crashed on.
func TestWatches_TargetJobAbsentIsHonest(t *testing.T) {
	base, sid := targetJobFixture(t)
	r, err := Watches(base, sid, WatchOpts{WatchID: orphanWatchID})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	w := findWatch(r, orphanWatchID)
	if w == nil {
		t.Fatalf("%s missing from report", orphanWatchID)
	}
	if w.TargetJob != nil {
		t.Errorf("TargetJob = %+v, want nil for a job absent from jobs.jsonl", w.TargetJob)
	}
	if !w.TargetJobMissing {
		t.Error("TargetJobMissing should mark a job target this log does not record")
	}
	out := RenderWatches(r)
	if !strings.Contains(out, "target job: not recorded in this session's jobs.jsonl") {
		t.Errorf("render should say the target job is unrecorded; got:\n%s", out)
	}
}

// A watch on the session (target "caller") has no target job at all: neither a
// joined state nor a missing-job claim.
func TestWatches_SessionTargetHasNoTargetJob(t *testing.T) {
	base, sid := targetJobFixture(t)
	r, err := Watches(base, sid, WatchOpts{WatchID: sessionWatchID})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	w := findWatch(r, sessionWatchID)
	if w == nil {
		t.Fatalf("%s missing from report", sessionWatchID)
	}
	if w.TargetJob != nil || w.TargetJobMissing {
		t.Errorf("session-target watch claims a target job: TargetJob=%+v missing=%t", w.TargetJob, w.TargetJobMissing)
	}
	if out := RenderWatches(r); strings.Contains(out, "target job:") {
		t.Errorf("session-target watch should render no target-job line; got:\n%s", out)
	}
}
