//go:build serffuzz

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// FuzzJobDelegateExactRunningAttach exercises deterministic edge states in the
// running-delegate control and attach paths. The byte selects a fixed seed case;
// it is not used as ambient input to stores, clocks, or providers.
func FuzzJobDelegateExactRunningAttach(f *testing.F) {
	for i := byte(0); i < 15; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, op byte) {
		switch op % 15 {
		case 0:
			if delegateResultSchemaMap(struct{}{}) != nil {
				t.Fatal("empty marshaled schema should be nil")
			}
		case 1:
			jm := newTestJM(t)
			if err := os.WriteFile(filepath.Join(jm.dir, "jobs.jsonl"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := findRunningDelegateByTranscriptRef(jm, "local:x"); err == nil {
				t.Fatal("corrupt job log should fail lookup")
			}
		case 2:
			jdraSeedAmbiguous(t)
		case 3:
			p, c, sub, rec := jdraRunningFixture(t, false)
			sub.running = false
			if got := p.sendRunningDelegateMessage(rec.JobID, "x", rec, false, nil); got.Err == nil {
				t.Fatal("non-live delegate accepted message")
			}
			_ = c
		case 4:
			p, _, _, rec := jdraRunningFixture(t, true)
			got := p.sendRunningDelegateMessage(rec.JobID, "", rec, true, nil)
			if got.Delivered || got.WatchSendDeliveryClass != watchSendBusy {
				t.Fatalf("watch busy result = %+v", got)
			}
		case 5:
			p, _, sub, rec := jdraRunningFixture(t, true)
			got := p.sendRunningDelegateMessage(rec.JobID, "steer", rec, true, nil)
			if got.Err != nil || got.TranscriptRef == "" || !sub.runFromWatch {
				t.Fatalf("watch steer result = %+v", got)
			}
		case 6:
			p, c, sub, rec := jdraRunningFixture(t, false)
			sub.driving = true
			got := p.sendRunningDelegateMessage(rec.JobID, "drive", rec, true, nil)
			if got.Err != nil {
				t.Fatalf("driving steer: %v", got.Err)
			}
			_ = c
		case 7:
			jdraResumeRunning(t)
		case 8:
			jdraResumeClosed(t)
		case 9:
			jdraAttachCapacity(t)
		case 10:
			jdraAttachClosing(t)
		case 11:
			jdraAttachForwardFailure(t)
		case 12:
			jdraAttachWrapper(t, false)
		case 13:
			jdraAttachWrapper(t, true)
		case 14:
			for _, status := range []jobstore.Status{jobstore.StatusCompleted, jobstore.StatusCancelled, jobstore.StatusFailed, jobstore.StatusRunning} {
				_ = subagentStatusFromJobStatus(status)
			}
		}
	})
}

func jdraRunningFixture(t *testing.T, installRun bool) (*Session, *Session, *subagent, *jobstore.JobRecord) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	sub := w3dlg_attachSub(c)
	sub.running = true
	sub.status = SubagentRunning
	p.subagents.track(sub)
	rec := &jobstore.JobRecord{JobID: jobstore.NewJobID(), DelegateID: jobstore.NewDelegateID(), Type: jobstore.JobDelegate, Status: jobstore.StatusRunning, TranscriptRef: encodeRef("", c.ID())}
	if installRun {
		p.jobManager.mu.Lock()
		p.jobManager.running[rec.JobID] = &runningJob{rec: rec, done: make(chan struct{})}
		p.jobManager.mu.Unlock()
	}
	return p, c, sub, rec
}

func jdraSeedAmbiguous(t *testing.T) {
	t.Helper()
	s := newTestSession(t)
	now, ref := s.jobManager.now(), encodeRef("", "same")
	for i := 0; i < 2; i++ {
		if err := s.jobManager.appendEvent(jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: jobstore.NewJobID(), Type: jobstore.JobDelegate, Status: jobstore.StatusRunning, StartedAt: &now, TranscriptRef: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := findRunningDelegateByTranscriptRef(s.jobManager, ref); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous lookup error = %v", err)
	}
}

func jdraResumeRunning(t *testing.T) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	sub := w3dlg_attachSub(c)
	run, err := p.attachDelegateJob(p.jobManager, c.ID(), "running", sub)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.jobManager.abandonRunningJob(run.rec.JobID) })
	sub.running = true
	got, fin, active, err := p.resumeOrFindRunningDelegate(p.jobManager, c.ID(), "x", sub, run.rec.TranscriptRef, run.rec.DelegateID, nil, nil, false, nil)
	if err != nil || got != nil || fin != nil || active == nil {
		t.Fatalf("resume fast path: run=%v fin=%v active=%v err=%v", got, fin, active, err)
	}
}

func jdraResumeClosed(t *testing.T) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	sub := w3dlg_attachSub(c)
	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()
	_, _, _, err := p.resumeOrFindRunningDelegate(p.jobManager, c.ID(), "x", sub, encodeRef("", c.ID()), "dlg_x", nil, nil, false, nil)
	if err == nil {
		t.Fatal("closed session resumed delegate")
	}
}

func jdraAttachCapacity(t *testing.T) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	for p.treeCounter.reserve(slotKindJob) {
	}
	t.Cleanup(func() {
		for p.treeCounter.n.Load() > 0 {
			p.treeCounter.releaseKind(slotKindJob)
		}
	})
	if run, err := p.attachDelegateJob(p.jobManager, c.ID(), "full", w3dlg_attachSub(c)); run != nil || !errors.Is(err, errTreeAtCapacity) {
		t.Fatalf("capacity run=%v err=%v", run, err)
	}
}

func jdraAttachClosing(t *testing.T) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	p.jobManager.mu.Lock()
	p.jobManager.closing = true
	p.jobManager.mu.Unlock()
	if run, err := p.attachDelegateJobWithID(p.jobManager, c.ID(), "closing", w3dlg_attachSub(c), jobstore.NewJobID(), nil, false); run != nil || !errors.Is(err, errJobManagerClosing) {
		t.Fatalf("closing run=%v err=%v", run, err)
	}
}

func jdraAttachForwardFailure(t *testing.T) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	want := errors.New("forward seed")
	p.jobManager.setParentJobID("job_parent")
	p.jobManager.forward = func(jobstore.Event) error { return want }
	if run, err := p.attachDelegateJob(p.jobManager, c.ID(), "forward", w3dlg_attachSub(c)); run != nil || !errors.Is(err, errDelegateStartForwardFailed) || !errors.Is(err, want) {
		t.Fatalf("forward run=%v err=%v", run, err)
	}
}

func jdraAttachWrapper(t *testing.T, prepared bool) {
	t.Helper()
	p, c := newTestSession(t), newTestSession(t)
	sub := w3dlg_attachSub(c)
	jobID := jobstore.NewJobID()
	var run *runningJob
	var err error
	if prepared {
		slot, ok := p.reserveTreeSlot()
		if !ok {
			t.Fatal("reserve prepared slot")
		}
		prep := &preparedSubagentRun{treeSlot: slot}
		run, err = p.attachDelegateJobWithPrepared(p.jobManager, c.ID(), "prepared", sub, jobID, nil, false, prep)
	} else {
		run, err = p.attachDelegateJobWithID(p.jobManager, c.ID(), "id", sub, jobID, nil, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	p.jobManager.abandonRunningJob(run.rec.JobID)
}
