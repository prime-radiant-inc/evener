//go:build serffuzz

package agent

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJobDelegateExactFinalizeReportSeed100 covers the defensive finalize and
// report branches with deterministic stores, virtual time, and scripted Git.
func FuzzJobDelegateExactFinalizeReportSeed100(f *testing.F) {
	for i := byte(0); i < 18; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		switch selector % 18 {
		case 0:
			s := newTestSession(t)
			got := s.resumedDelegateRestoreDescriptor("job", "child", "local:child", nil, &jobstore.DelegateRestoreDescriptor{})
			if got.Version != 1 {
				t.Fatalf("version = %d, want 1", got.Version)
			}
		case 1:
			if err := (&Session{}).finalizeDelegate("job", "child", nil); err == nil {
				t.Fatal("finalize without manager succeeded")
			}
		case 2:
			jdExactFinalizeRetry(t)
		case 3:
			parent, run, _ := jdExactAttached(t, nil)
			if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, nil, false); err != nil {
				t.Fatalf("missing-child finalize: %v", err)
			}
		case 4:
			parent, run, sub := jdExactAttached(t, &subagent{status: SubagentCompleted, result: "done"})
			run.delegateOutputAppended = true
			if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err != nil {
				t.Fatalf("already-appended finalize: %v", err)
			}
		case 5:
			s := newTestSession(t)
			if err := s.persistDelegateResumability(nil, nil); err != nil {
				t.Fatalf("nil resumability guard: %v", err)
			}
			run := &runningJob{rec: &jobstore.JobRecord{Type: jobstore.JobShell}}
			if err := s.persistDelegateResumability(s.jobManager, run); err != nil {
				t.Fatalf("non-delegate resumability guard: %v", err)
			}
		case 6:
			s := newTestSession(t)
			run := &runningJob{rec: &jobstore.JobRecord{JobID: "assessed", Type: jobstore.JobDelegate}, delegateResumeAssessed: true}
			if err := s.persistDelegateResumability(s.jobManager, run); err != nil {
				t.Fatalf("assessed resumability: %v", err)
			}
		case 7:
			if !delegateJobManagerClosing(nil) || !delegateFinalizeStopsRetry(nil, errors.New("retry")) {
				t.Fatal("nil manager was considered retryable")
			}
			if n, err := appendDelegateOutput(nil, nil, []byte("x"), nil); n != 0 || err != nil {
				t.Fatalf("nil output append = %d, %v", n, err)
			}
		case 8:
			status, reason := delegateTerminalStatus(nil, nil, SubagentCancelled)
			if status != jobstore.StatusCancelled || reason != "stopped_by_parent" {
				t.Fatalf("cancel terminal = %q/%q", status, reason)
			}
		case 9:
			jm := newTestJM(t)
			run := &runningJob{rec: &jobstore.JobRecord{JobID: "missing", DelegateID: "delegate", Type: jobstore.JobDelegate}}
			got := delegateTerminalResult(nil, jm, run)
			if got.Reason != "read_failed" || got.Err == nil {
				t.Fatalf("missing terminal result = %+v", got)
			}
		case 10:
			jm := newTestJM(t)
			guards := []*jobstore.JobRecord{nil, {}, {DelegateID: "d"}, {DelegateID: "d", TranscriptRef: "bad"}}
			for _, rec := range guards {
				if got := activeDelegateWatchSummaries(jm, rec); got != nil {
					t.Fatalf("watch guard %#v = %#v", rec, got)
				}
			}
		case 11:
			if delegateResultSchema(nil) != nil || delegateResultSchema(&jobstore.JobRecord{}) != nil {
				t.Fatal("nil result schema guard failed")
			}
			if cloneDelegateResultSchema(nil) != nil || cloneDelegateResultSchema(map[string]any{}) != nil {
				t.Fatal("empty result schema was retained")
			}
		case 12:
			value := map[string]any{"type": "object"}
			old := delegateResultJSONUnmarshal
			delegateResultJSONUnmarshal = func([]byte, any) error { return errors.New("decode fault") }
			t.Cleanup(func() { delegateResultJSONUnmarshal = old })
			if got := cloneDelegateResultSchema(value); !reflect.DeepEqual(got, value) {
				t.Fatalf("decode-fault clone = %#v", got)
			}
		case 13:
			jdgr100Report(t, 5)
		case 14:
			parent := newTestSession(t)
			sub := &subagent{id: "detached", status: SubagentCompleted, done: make(chan struct{})}
			run, err := parent.attachDelegateJobWithID(parent.jobManager, sub.id, "structured", sub, jobstore.NewJobID(), map[string]any{"type": "object"}, false)
			if err != nil {
				t.Fatalf("attach structured delegate: %v", err)
			}
			t.Cleanup(func() { parent.jobManager.abandonRunningJob(run.rec.JobID) })
			if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err != nil {
				t.Fatalf("detached structured finalize: %v", err)
			}
		case 15:
			parent, run, sub := jdExactAttached(t, &subagent{status: SubagentCompleted, result: "flush me"})
			run.delegateOutputWritten = len(delegateOutputBytes(sub.result))
			if err := run.output.Close(); err != nil {
				t.Fatalf("close output: %v", err)
			}
			if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err == nil {
				t.Fatal("closed reflush succeeded")
			}
		case 16:
			parent, run, sub := jdExactAttached(t, &subagent{status: SubagentCompleted, result: "append me"})
			if err := run.output.Close(); err != nil {
				t.Fatalf("close output: %v", err)
			}
			if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err == nil {
				t.Fatal("closed append succeeded")
			}
		case 17:
			if got := cloneDelegateResultSchema(struct{}{}); got != nil {
				t.Fatalf("decoded empty object = %#v, want nil", got)
			}
		}
	})
}

func jdExactAttached(t *testing.T, sub *subagent) (*Session, *runningJob, *subagent) {
	t.Helper()
	parent := newTestSession(t)
	child := newTestSession(t)
	if sub == nil {
		sub = &subagent{status: SubagentCompleted}
	}
	sub.id = child.ID()
	sub.sess = child
	sub.done = make(chan struct{})
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "exact finalize", sub)
	if err != nil {
		t.Fatalf("attach delegate: %v", err)
	}
	t.Cleanup(func() {
		parent.jobManager.abandonRunningJob(run.rec.JobID)
	})
	return parent, run, sub
}

func jdExactFinalizeRetry(t *testing.T) {
	t.Helper()
	parent, run, sub := jdExactAttached(t, &subagent{status: SubagentCompleted, result: "retry output"})
	failAppendN(parent.jobManager, jobstore.EventJobFinished, 1)
	if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err == nil {
		t.Fatal("injected finalize append succeeded")
	}
	if err := parent.finalizeDelegateOnce(parent.jobManager, run.rec.JobID, sub, false); err != nil {
		t.Fatalf("finalize after healed append: %v", err)
	}
}
