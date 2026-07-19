//go:build serffuzz

package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func seed100JobsRangeA(t *testing.T) {
	t.Helper()

	jm := newTestJM(t)
	stamp := &runningJob{rec: &jobstore.JobRecord{JobID: "stamp", Status: jobstore.StatusRunning}}
	jm.running[stamp.rec.JobID] = stamp
	jm.stampLastActivityLocked(stamp.rec.JobID)
	if stamp.rec.LastActivity == nil {
		t.Fatal("stampLastActivityLocked did not stamp a running job")
	}

	jm.running["delegate"] = &runningJob{rec: &jobstore.JobRecord{
		JobID: "delegate",
		Type:  jobstore.JobDelegate,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{
			WorkingDir: "/tmp/seed100-delegate",
		},
	}}
	handles := jm.liveWorkHandles()
	if len(handles) != 1 || handles[0].dir != "/tmp/seed100-delegate" {
		t.Fatalf("live delegate handles = %#v", handles)
	}
	delete(jm.running, "stamp")
	delete(jm.running, "delegate")

	closedDone := make(chan struct{})
	close(closedDone)
	closing := &runningJob{
		rec:  &jobstore.JobRecord{JobID: "closing", Status: jobstore.StatusRunning},
		done: closedDone,
	}
	jm.running[closing.rec.JobID] = closing
	if err := jm.closeRuntimeState(); err != nil {
		t.Fatalf("closeRuntimeState: %v", err)
	}
	if closing.stopStatus != jobstore.StatusCancelled || closing.stopReason != "stopped_by_parent" {
		t.Fatalf("close cancellation = %q, %q", closing.stopStatus, closing.stopReason)
	}
	delete(jm.running, closing.rec.JobID)

	abandon := newTestJM(t)
	done := make(chan struct{})
	output, err := abandon.openOutput(filepath.Join(abandon.dir, "jobs", "abandon.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	abandon.running["abandon"] = &runningJob{
		rec:    &jobstore.JobRecord{JobID: "abandon", Status: jobstore.StatusRunning},
		done:   done,
		output: output,
	}
	abandon.abandonRunningJob("abandon")
	select {
	case <-done:
	default:
		t.Fatal("abandonRunningJob did not close done")
	}

	listed := newTestJM(t)
	for _, id := range []string{"b", "a"} {
		listed.running[id] = &runningJob{
			rec: &jobstore.JobRecord{
				JobID:          id,
				Type:           jobstore.JobShell,
				Status:         jobstore.StatusRunning,
				OwnerSessionID: listed.sessionID,
			},
			durableStarted: true,
		}
	}
	jobs, _, err := listed.listWithError(listFilter{Limit: 1})
	if err != nil {
		t.Fatalf("listWithError: %v", err)
	}
	if len(jobs) != 1 || jobs[0].JobID != "a" {
		t.Fatalf("listed jobs = %#v", jobs)
	}
}
