package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestOrdinaryChildStartPublishesExactIdentityOnce(t *testing.T) {
	jm := newTestJM(t)
	jm.parentJobID = "parent-job"
	steps := make([]string, 0, 4)
	var began *activityPublication
	var appended, forwarded []jobstore.Event
	rootOdd := false
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps = append(steps, "odd")
		began = &activityPublication{RootID: "root-session", Revision: 11}
		rootOdd = true
		return began, nil
	}
	jm.appendEvent = func(event jobstore.Event) error {
		steps = append(steps, "append")
		appended = append(appended, event)
		return nil
	}
	jm.forward = func(event jobstore.Event) error {
		steps = append(steps, "forward")
		forwarded = append(forwarded, event)
		return nil
	}
	jm.commitActivityPublication = func(publication *activityPublication) error {
		steps = append(steps, "even")
		if !rootOdd || publication != began || publication.Revision != 11 {
			t.Fatalf("root control lost at commit: odd=%v publication=%#v", rootOdd, publication)
		}
		rootOdd = false
		return nil
	}

	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := steps, []string{"odd", "append", "forward", "even"}; !childPublicationEqualStrings(got, want) {
		t.Fatalf("publication order = %v, want %v", got, want)
	}
	if rootOdd {
		t.Fatal("root remained odd after child start commit")
	}
	if len(appended) != 1 || len(forwarded) != 1 {
		t.Fatalf("child start count append=%d forward=%d, want one each", len(appended), len(forwarded))
	}
	assertChildEventIdentity(t, appended[0], forwarded[0], rec.JobID, "parent-job", "root-session", 11, jobstore.EventJobStarted)
}

func TestOrdinaryChildFinishPublishesExactIdentityOnce(t *testing.T) {
	jm := newTestJM(t)
	jm.parentJobID = "parent-job"
	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0, 4)
	var appended, forwarded []jobstore.Event
	rootOdd := false
	jm.beginActivityPublication = func() (*activityPublication, error) {
		steps = append(steps, "odd")
		rootOdd = true
		return &activityPublication{RootID: "root-session", Revision: 12}, nil
	}
	jm.appendEvent = func(event jobstore.Event) error {
		steps = append(steps, "append")
		appended = append(appended, event)
		return nil
	}
	jm.forward = func(event jobstore.Event) error {
		steps = append(steps, "forward")
		forwarded = append(forwarded, event)
		return nil
	}
	jm.commitActivityPublication = func(*activityPublication) error {
		steps = append(steps, "even")
		if !rootOdd {
			t.Fatal("root was not odd before finish commit")
		}
		jm.mu.Lock()
		live := jm.running[rec.JobID]
		liveStatus := jobstore.Status("")
		if live != nil {
			liveStatus = live.rec.Status
		}
		jm.mu.Unlock()
		if live == nil || liveStatus != jobstore.StatusCompleted {
			t.Fatalf("terminal live overlay not installed before even: %#v", live)
		}
		rootOdd = false
		return nil
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	// The normal finish entry point supplies the durable and forwarded child event.
	if _, err := jm.writeFinishJob(run, jobstore.StatusCompleted, "done", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := steps, []string{"odd", "append", "forward", "even"}; !childPublicationEqualStrings(got, want) {
		t.Fatalf("publication order = %v, want %v", got, want)
	}
	if len(appended) != 1 || len(forwarded) != 1 {
		t.Fatalf("child finish count append=%d forward=%d, want one each", len(appended), len(forwarded))
	}
	assertChildEventIdentity(t, appended[0], forwarded[0], rec.JobID, "parent-job", "root-session", 12, jobstore.EventJobFinished)
}

func assertChildEventIdentity(t *testing.T, appended, forwarded jobstore.Event, jobID, parent, root string, revision uint64, kind jobstore.EventKind) {
	t.Helper()
	for name, event := range map[string]jobstore.Event{"appended": appended, "forwarded": forwarded} {
		if event.Kind != kind || event.JobID != jobID || event.OwnerSessionID == "" || event.ParentJobID != parent || event.RootSessionID != root || event.TreeRevision != revision {
			t.Errorf("%s event identity = %#v", name, event)
		}
		if kind == jobstore.EventJobFinished && (event.Status != jobstore.StatusCompleted || event.EndedAt == nil) {
			t.Errorf("%s finish fields = %#v", name, event)
		}
	}
}

func childPublicationEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
