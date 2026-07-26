package agent

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// An unrecognized child status falls through to the failed default with the
// unknown-status reason, while a set stop status/reason on the running job wins
// over the mapped child status.
func TestW2Dlg_DelegateTerminalStatus_DefaultAndStopOverride(t *testing.T) {
	t.Parallel()

	status, reason := delegateTerminalStatus(nil, nil, SubagentStatus("bogus"), nil)
	if status != jobstore.StatusFailed || reason != "unknown_child_status" {
		t.Fatalf("default = (%q, %q), want (failed, unknown_child_status)", status, reason)
	}

	jm := newTestJM(t)
	run := &runningJob{
		rec:        &jobstore.JobRecord{JobID: "job_stop"},
		stopStatus: jobstore.StatusCancelled,
		stopReason: "stopped_by_parent",
	}
	status, reason = delegateTerminalStatus(jm, run, SubagentCompleted, nil)
	if status != jobstore.StatusCancelled || reason != "stopped_by_parent" {
		t.Fatalf("stop override = (%q, %q), want (cancelled, stopped_by_parent)", status, reason)
	}
}

// cloneDelegateResultSchema collapses a value that round-trips to an empty
// object (here a struct with no fields) back to nil.
func TestW2Dlg_CloneDelegateResultSchema_EmptyAfterRoundTrip(t *testing.T) {
	t.Parallel()
	if got := cloneDelegateResultSchema(struct{}{}); got != nil {
		t.Fatalf("clone(struct{}{}) = %#v, want nil", got)
	}
	if got := cloneDelegateResultSchema(map[string]any{}); got != nil {
		t.Fatalf("clone(empty map) = %#v, want nil", got)
	}
}

// activeDelegateWatchSummaries returns nil for a record missing the delegate
// coordinates and for a record whose transcript ref cannot be decoded.
func TestW2Dlg_ActiveDelegateWatchSummaries_GuardsAndBadRef(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)

	if got := activeDelegateWatchSummaries(nil, nil); got != nil {
		t.Fatalf("nil inputs = %#v, want nil", got)
	}
	if got := activeDelegateWatchSummaries(jm, &jobstore.JobRecord{}); got != nil {
		t.Fatalf("empty rec = %#v, want nil", got)
	}
	badRef := &jobstore.JobRecord{DelegateID: "dlg_x", TranscriptRef: "!not-a-ref!"}
	if got := activeDelegateWatchSummaries(jm, badRef); got != nil {
		t.Fatalf("bad ref = %#v, want nil", got)
	}
}

// appendDelegateOutput is a no-op when the running job or its output store is
// absent.
func TestW2Dlg_AppendDelegateOutput_NilRun(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	if n, err := appendDelegateOutput(jm, nil, []byte("x"), nil); n != 0 || err != nil {
		t.Fatalf("nil run = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := appendDelegateOutput(jm, &runningJob{}, []byte("x"), nil); n != 0 || err != nil {
		t.Fatalf("nil output = (%d, %v), want (0, nil)", n, err)
	}
}

// delegateTerminalResult returns a read_failed result when the job record for
// the running delegate can no longer be found in the store.
func TestW2Dlg_DelegateTerminalResult_ReadFailed(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	run := &runningJob{rec: &jobstore.JobRecord{
		JobID:         "job_gone",
		DelegateID:    "dlg_gone",
		TranscriptRef: encodeRef("", "child_gone"),
	}}

	res := delegateTerminalResult(nil, jm, run)
	if res.Status != jobstore.StatusFailed || res.Reason != "read_failed" || res.Err == nil {
		t.Fatalf("result = (%q, %q, err=%v), want (failed, read_failed, non-nil)", res.Status, res.Reason, res.Err)
	}
	if res.JobID != "job_gone" || res.DelegateID != "dlg_gone" {
		t.Fatalf("identity carried wrong: %+v", res)
	}
}

// delegateTerminalResult falls back to the live in-memory run.structured value
// when the durable record hasn't persisted a structured result yet (both
// rec.StructuredResult and rec.StructuredResultValid are nil), and defaults
// that live value's validity to true.
func TestW2Dlg_DelegateTerminalResult_LiveStructuredDefaultsValidTrue(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	const jobID = "job_live_structured"
	started := time.Unix(3000, 0).UTC()
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}
	if err := jm.appendJobEvents([]jobstore.Event{{
		Kind:          jobstore.EventJobStarted,
		TS:            started,
		JobID:         jobID,
		Type:          jobstore.JobDelegate,
		Description:   "live structured",
		DelegateID:    "dlg_live",
		TranscriptRef: encodeRef("", "child_live"),
		StartedAt:     &started,
		OutputPath:    outputPath,
	}}); err != nil {
		t.Fatalf("seed started delegate job: %v", err)
	}
	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		t.Fatalf("findJobRecord: %v", err)
	}
	if rec.StructuredResult != nil || rec.StructuredResultValid != nil {
		t.Fatalf("fixture rec already has a persisted structured result: %+v", rec)
	}
	run := &runningJob{rec: rec, structured: map[string]any{"ok": true}}

	res := delegateTerminalResult(nil, jm, run)

	got, ok := res.StructuredResult.(map[string]any)
	if !ok || got["ok"] != true {
		t.Fatalf("StructuredResult = %#v, want the live run.structured value", res.StructuredResult)
	}
	if !res.StructuredResultValidSet || !res.StructuredResultValid {
		t.Fatalf("StructuredResultValid(Set) = (%v, %v), want (true, true)", res.StructuredResultValid, res.StructuredResultValidSet)
	}
}
