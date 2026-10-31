package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestManagerStopJobOwner(t *testing.T) {
	for _, includeChildren := range []bool{false, true} {
		t.Run(map[bool]string{false: "job_only", true: "immediate_children"}[includeChildren], func(t *testing.T) {
			jm := newTestJM(t)
			s := &Session{jobManager: jm, id: jm.sessionID}
			parent, err := jm.createShell(createShellOpts{Command: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			jm.setParentJobID(parent.JobID)
			child, err := jm.createShell(createShellOpts{Command: "child"})
			if err != nil {
				t.Fatal(err)
			}
			jm.setParentJobID(child.JobID)
			grandchild, err := jm.createShell(createShellOpts{Command: "grandchild"})
			if err != nil {
				t.Fatal(err)
			}
			jm.setParentJobID("")
			before, err := jm.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.StopJobForManager(context.Background(), appwire.SessionJobStopParams{JobID: parent.JobID, IncludeChildren: includeChildren})
			if err != nil || result.JobID != parent.JobID {
				t.Fatalf("job stop=%+v err=%v", result, err)
			}
			after, err := jm.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != string(jobstore.StatusCancelled) || jm.running[parent.JobID].stopStatus != jobstore.StatusCancelled {
				t.Fatalf("parent stop not admitted: %+v", result)
			}
			if (jm.running[child.JobID].stopStatus == jobstore.StatusCancelled) != includeChildren {
				t.Fatalf("child stop flag mismatch: %+v", jm.running[child.JobID])
			}
			if !reflect.DeepEqual(before[grandchild.JobID], after[grandchild.JobID]) {
				t.Fatal("grandchild stopped, but human include_children stops only immediate children")
			}
			if !includeChildren && !reflect.DeepEqual(before[child.JobID], after[child.JobID]) {
				t.Fatal("unflagged stop changed child")
			}
			if err := jm.finalize(parent.JobID, jobstore.StatusCancelled, "stopped_by_parent", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := s.StopJobForManager(context.Background(), appwire.SessionJobStopParams{JobID: parent.JobID}); err == nil {
				t.Fatal("terminal job accepted")
			}
			settled, err := jm.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if settled[parent.JobID].Status != jobstore.StatusCancelled || !reflect.DeepEqual(after[child.JobID], settled[child.JobID]) {
				t.Fatal("terminal refusal changed journal")
			}
			baseline := settled
			if _, err := s.StopJobForManager(context.Background(), appwire.SessionJobStopParams{JobID: "job_unknown"}); err == nil {
				t.Fatal("unknown job accepted")
			}
			if _, err := s.StopJobForManager(context.Background(), appwire.SessionJobStopParams{JobID: "dlg_other"}); err == nil {
				t.Fatal("delegate ID accepted as shell job")
			}
			settled, err = jm.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(baseline, settled) {
				t.Fatal("rejected stop changed journal")
			}
		})
	}
}

func TestManagerStopOwnedDelegateOwner(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	seedStableToolRunningDelegate(t, s, "dlg_manager_parent", "", time.Unix(10, 0).UTC())
	seedStableToolRunningDelegate(t, s, "dlg_manager_child", "dlg_manager_parent", time.Unix(11, 0).UTC())
	result, err := s.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: "dlg_manager_parent"})
	if err != nil || result.JobID != "dlg_manager_parent" {
		t.Fatalf("delegate stop=%+v err=%v", result, err)
	}
	s.delegateController.mu.Lock()
	_, childCovered := s.delegateController.stop.members["dlg_manager_child"]
	s.delegateController.mu.Unlock()
	if !childCovered {
		t.Fatal("owned delegate subtree not stopped")
	}
	if _, err := s.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: "job_wrong"}); err == nil {
		t.Fatal("job ID accepted as stable delegate")
	}
	if _, err := s.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: "dlg_unknown"}); err == nil {
		t.Fatal("unknown stable delegate accepted")
	}
}

func TestManagerStopJobOwnedDelegateSession(t *testing.T) {
	for _, includeChildren := range []bool{false, true} {
		root := newSession(t, withoutGitSnapshot())
		child := newSession(t, withoutGitSnapshot())
		child.owningDelegateID = "dlg_owned_child"
		child.jobManager.parentDelegateID = child.owningDelegateID
		root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
		child.jobManager.forward = root.jobManager.store.Append
		parent, err := child.jobManager.createShell(createShellOpts{Command: "owned parent"})
		if err != nil {
			t.Fatal(err)
		}
		child.jobManager.setParentJobID(parent.JobID)
		junior, err := child.jobManager.createShell(createShellOpts{Command: "owned junior"})
		if err != nil {
			t.Fatal(err)
		}
		child.jobManager.setParentJobID(junior.JobID)
		grandchild, err := child.jobManager.createShell(createShellOpts{Command: "owned grandchild"})
		if err != nil {
			t.Fatal(err)
		}
		child.jobManager.setParentJobID("")
		result, err := root.StopJobForManager(t.Context(), appwire.SessionJobStopParams{JobID: parent.JobID, IncludeChildren: includeChildren})
		if err != nil || result.JobID != parent.JobID || child.jobManager.running[parent.JobID].stopStatus != jobstore.StatusCancelled {
			t.Fatalf("direct owned job stop=%+v err=%v", result, err)
		}
		if got := child.jobManager.running[junior.JobID].stopStatus == jobstore.StatusCancelled; got != includeChildren {
			t.Fatalf("child stopped=%v, includeChildren=%v", got, includeChildren)
		}
		if child.jobManager.running[grandchild.JobID].stopStatus == jobstore.StatusCancelled {
			t.Fatal("grandchild stopped outside human stopChildren depth")
		}
	}
}

func TestManagerStopOwnedDelegateAlreadySettled(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	const id = "dlg_already_settled"
	descriptor := stableReadonlyDescriptor(s, id)
	started := time.Unix(10, 0).UTC()
	finish := delegateFinishFromRun(delegateTerminalRunInputs{result: "done", communicated: true, descriptor: descriptor, startedAt: started, latestActivityAt: started, endedAt: time.Unix(11, 0).UTC()})
	seedStableReadonlyFinish(t, s, id, descriptor, started, finish, true)
	before := s.delegateController.Snapshot().rows
	for range 2 {
		result, err := s.StopOwnedDelegateForManager(t.Context(), appwire.SessionOwnedDelegateStopParams{DelegateID: id})
		if err != nil || result.Outcome != "already_stopped" {
			t.Fatalf("settled stop=%+v err=%v", result, err)
		}
	}
	if after := s.delegateController.Snapshot().rows; !reflect.DeepEqual(before, after) {
		t.Fatal("settled stop wrote another stop event")
	}
	if s.delegateController.stop != nil {
		t.Fatal("settled stop started another cancellation cascade")
	}
}
