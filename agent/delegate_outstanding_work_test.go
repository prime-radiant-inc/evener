package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestOutstandingDelegateWorkEmpty(t *testing.T) {
	got, err := (&Session{}).outstandingDelegateWork()
	if err != nil {
		t.Fatalf("outstandingDelegateWork on empty session: %v", err)
	}
	if !got.Empty() {
		t.Fatalf("empty session inventory = %#v, want Empty", got)
	}
	if ids := got.IDs(); len(ids) != 0 {
		t.Fatalf("empty session IDs = %v, want none", ids)
	}
}

func TestOutstandingDelegateWorkItemizesEachCategory(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "jobs.jsonl")
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatalf("create empty job journal: %v", err)
	}

	jm := &jobManager{dir: dir, sessionID: "s1"}
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.watches = map[watchKey]*watchConfig{
		{VisibleSessionID: "s1", Target: "job-1", ReceiverSessionID: "s1"}: {watchID: "watch-1", receiverSessionID: "s1"},
	}
	jm.mu.Unlock()

	s := &Session{
		id:         "s1",
		stateDir:   dir,
		jobManager: jm,
		inputQueue: []queuedInput{
			{ID: "input-1"},
			{ID: "dup"},
		},
		steeringQueue: []steeringMessage{
			{StableTurnID: "steer-1"},
		},
		pendingJobNotifs: []jobNotification{
			{JobID: "notif-1"},
			{JobID: "dup"},
		},
		delegateAttentionArmIDs: map[string]struct{}{
			"attention-arm": {},
		},
		rootAttentionWakeIDs: map[string]struct{}{
			"attention-root": {},
		},
		delegateController: &delegateTreeController{
			durable: delegatestore.State{
				"dlg-1": {
					DelegateID:        "dlg-1",
					Phase:             delegatestore.PhaseRunning,
					CurrentRunOpen:    true,
					PendingDeliveries: []delegatestore.PendingDelivery{{DeliveryID: "delivery-1"}},
				},
			},
		},
	}
	s.getOrCreateGoalStore().Set("test", time.Now())

	got, err := s.outstandingDelegateWork()
	if err != nil {
		t.Fatalf("outstandingDelegateWork: %v", err)
	}
	if got.Empty() {
		t.Fatal("inventory is empty, want one entry in each category")
	}

	want := []string{
		"job:job-1",
		"watch:watch-1",
		"notification:notif-1",
		"notification:dup",
		"input:input-1",
		"input:steer-1",
		"input:dup",
		"child-run:dlg-1",
		"child-delivery:delivery-1",
		"attention:attention-arm",
		"attention:attention-root",
		"goal:test",
	}
	ids := got.IDs()
	for _, id := range want {
		if !slices.Contains(ids, id) {
			t.Errorf("IDs() = %v, missing %q", ids, id)
		}
	}
	if len(ids) != len(want) {
		t.Errorf("IDs() = %v, want exactly %v", ids, want)
	}
}

func TestOutstandingDelegateWorkReadFailureIsUnknown(t *testing.T) {
	t.Run("job journal", func(t *testing.T) {
		s := &Session{id: "s1"}
		s.jobManager = &jobManager{dir: t.TempDir(), sessionID: "s1"}

		got, err := s.outstandingDelegateWork()
		if err == nil {
			t.Fatal("outstandingDelegateWork error = nil, want job journal error")
		}
		if got.Empty() {
			t.Fatal("inventory with unreadable job journal is Empty, want Unknown to count as work")
		}
		if !slices.Contains(got.Unknown, "job-journal") {
			t.Fatalf("Unknown = %v, want job-journal", got.Unknown)
		}
	})

	t.Run("attention fold", func(t *testing.T) {
		s := &Session{id: "s1", stateDir: t.TempDir(), transcriptReady: true}
		s.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			return delegateAttentionFold{}, errors.New("delegate transcript read failed")
		}

		got, err := s.outstandingDelegateWork()
		if err == nil {
			t.Fatal("outstandingDelegateWork error = nil, want attention fold error")
		}
		if got.Empty() {
			t.Fatal("inventory with unreadable attention fold is Empty, want Unknown to count as work")
		}
		if !slices.Contains(got.Unknown, "attention-fold") {
			t.Fatalf("Unknown = %v, want attention-fold", got.Unknown)
		}
	})
}

func TestOutstandingDelegateWorkIncludesInFlightWatchCallback(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.watchCallbacks = 1
	jm.mu.Unlock()

	work, err := s.outstandingDelegateWork()
	if err != nil {
		t.Fatalf("outstandingDelegateWork: %v", err)
	}
	if !slices.Contains(work.Notifications, "watch-callback") {
		t.Fatalf("notifications = %v, want watch-callback", work.Notifications)
	}
}
