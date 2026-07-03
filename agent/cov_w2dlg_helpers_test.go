package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// An unrecognized child status falls through to the failed default with the
// unknown-status reason, while a set stop status/reason on the running job wins
// over the mapped child status.
func TestW2Dlg_DelegateTerminalStatus_DefaultAndStopOverride(t *testing.T) {
	t.Parallel()

	status, reason := delegateTerminalStatus(nil, nil, SubagentStatus("bogus"))
	if status != jobstore.StatusFailed || reason != "unknown_child_status" {
		t.Fatalf("default = (%q, %q), want (failed, unknown_child_status)", status, reason)
	}

	jm := newTestJM(t)
	run := &runningJob{
		rec:        &jobstore.JobRecord{JobID: "job_stop"},
		stopStatus: jobstore.StatusCancelled,
		stopReason: "stopped_by_parent",
	}
	status, reason = delegateTerminalStatus(jm, run, SubagentCompleted)
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
