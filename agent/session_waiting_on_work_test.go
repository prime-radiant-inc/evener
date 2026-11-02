package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

// TestSessionHasWakeableWork pins every wake source hasWakeableWork counts
// for a resting session, plus the bare session it must refuse: a wake exists
// when autonomous work or user steering is runnable, a managed job runs or
// finalizes, a question waits on the user, or any holder manager keeps an armed
// watch that can deliver a wake. Detached processes and held or daemon steering
// do not count.
func TestSessionHasWakeableWork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
		seed func(t *testing.T, s *Session)
	}{
		{
			name: "bare session has no wakeable work",
			want: false,
			seed: func(_ *testing.T, _ *Session) {},
		},
		{
			name: "pending job notification wakes",
			want: true,
			seed: func(_ *testing.T, s *Session) {
				s.enqueueJobNotificationAndNotify(jobNotification{})
			},
		},
		{
			name: "working subagent wakes",
			want: true,
			seed: func(t *testing.T, s *Session) {
				child := newTestSessionForState(t)
				s.subagents.track(&subagent{id: child.ID(), sess: child, running: true})
			},
		},
		{
			name: "running detached process alone cannot wake",
			want: false,
			seed: func(_ *testing.T, s *Session) {
				s.recordDetachedProcess(execenv.DetachedProcess{PID: 4321, Done: make(chan struct{})})
			},
		},
		{
			name: "running managed job wakes",
			want: true,
			seed: func(t *testing.T, s *Session) {
				now := time.Now().UTC()
				if err := s.jobManager.appendEvent(jobstore.Event{
					Kind: jobstore.EventJobStarted, TS: now, JobID: "job_x",
					Type: jobstore.JobShell, OwnerSessionID: s.ID(), StartedAt: &now,
				}); err != nil {
					t.Fatal(err)
				}
				if ids := sessionRunningJobIDs(s); len(ids) != 1 || ids[0] != "job_x" {
					t.Fatalf("sessionRunningJobIDs = %v, want [job_x]", ids)
				}
			},
		},
		{
			name: "runnable user steering wakes",
			want: true,
			seed: func(t *testing.T, s *Session) {
				if err := s.SteerFromUser("please continue"); err != nil {
					t.Fatal(err)
				}
				if !s.hasRunnableUserSteering() {
					t.Fatal("user steering was not admitted")
				}
			},
		},
		{
			name: "Stop-held user steering cannot wake",
			want: false,
			seed: func(t *testing.T, s *Session) {
				if err := s.SteerFromUser("please continue"); err != nil {
					t.Fatal(err)
				}
				if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
					snapshot.SteeringHeld = true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if !s.hasPendingUserSteering() || s.hasRunnableUserSteering() {
					t.Fatal("user steering must remain pending but held")
				}
			},
		},
		{
			name: "daemon steering alone cannot wake",
			want: false,
			seed: func(t *testing.T, s *Session) {
				if err := s.Steer("context for the next turn"); err != nil {
					t.Fatal(err)
				}
				if !s.hasPendingSteering() || s.hasRunnableUserSteering() {
					t.Fatal("only daemon steering must be pending")
				}
			},
		},
		{
			name: "live managed run wakes without a running journal record",
			want: true,
			seed: func(_ *testing.T, s *Session) {
				jm := s.jobManager
				jm.mu.Lock()
				jm.running["job_finalizing"] = &runningJob{}
				jm.mu.Unlock()
			},
		},
		{
			name: "pending question wakes",
			want: true,
			seed: func(_ *testing.T, s *Session) {
				s.mu.Lock()
				s.askPending = append(s.askPending, askQuestion{})
				s.mu.Unlock()
			},
		},
		{
			name: "armed timer watch in own manager wakes",
			want: true,
			seed: func(t *testing.T, s *Session) {
				t.Cleanup(s.Close)
				if _, err := s.jobManager.configureWatch(watchArgs{
					Target:       runtimeMessageAliasCaller,
					Source:       "self",
					AfterSeconds: 3600,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "idle child manager watch wakes root receiver",
			want: true,
			seed: func(t *testing.T, s *Session) {
				child := newTestSessionForState(t)
				t.Cleanup(child.Close)
				s.subagents.track(&subagent{id: child.ID(), sess: child})
				if _, err := child.jobManager.configureWatch(watchArgs{
					Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600,
					ReceiverSessionID: s.ID(), ReceiverNotify: s.enqueueJobNotificationAndNotify,
				}); err != nil {
					t.Fatal(err)
				}
				if s.hasWorkingSubagent() || child.autonomyInFlight() {
					t.Fatal("child must be idle")
				}
				if s.jobManager.hasArmedWatch() || !child.jobManager.hasArmedWatch() {
					t.Fatal("only the child's manager must hold an armed watch")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestSessionForState(t)
			tc.seed(t, s)
			if got := s.hasWakeableWork(); got != tc.want {
				t.Fatalf("hasWakeableWork = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionHasWakeableWorkTimerHandoff(t *testing.T) {
	t.Parallel()
	s := newTestSessionForState(t)
	t.Cleanup(s.Close)
	jm := s.jobManager
	res, err := jm.configureWatch(watchArgs{
		Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatal("timer was not registered")
	}
	enqueue := jm.enqueue
	observed := false
	jm.enqueue = func(n jobNotification) {
		observed = true
		if !jm.hasNoticeEmissionInFlight() {
			t.Error("timer emission must remain in flight during enqueue")
		}
		if jm.hasArmedWatch() || s.peekNotifications() != 0 {
			t.Error("timer must be spent before its notification reaches the queue")
		}
		if !s.hasWakeableWork() {
			t.Error("firing timer must remain wakeable until enqueue")
		}
		enqueue(n)
	}
	if jm.fireProgressTick(key, cfg) {
		t.Fatal("one-shot timer must retire")
	}
	jm.mu.Lock()
	inFlight := jm.noticesInFlight
	jm.mu.Unlock()
	if inFlight != 0 || jm.hasNoticeEmissionInFlight() || jm.hasArmedWatch() {
		t.Fatal("spent timer must have no remaining producer signal")
	}
	if !observed || s.peekNotifications() != 1 || !s.hasWakeableWork() {
		t.Fatal("timer must hand off to one queued notification")
	}
}

func TestSessionHasWakeableWorkEventWatchBudgetHandoff(t *testing.T) {
	t.Parallel()
	root := newTestSessionForState(t)
	t.Cleanup(root.Close)
	child := newTestSessionForState(t)
	t.Cleanup(child.Close)
	root.subagents.track(&subagent{id: child.ID(), sess: child})
	jm := child.jobManager
	observed := 0
	res, err := jm.configureWatch(watchArgs{
		Target: "*", Events: []string{"job.notification"}, ReceiverSessionID: root.ID(),
		ReceiverNotify: func(n jobNotification) {
			observed++
			if jm.hasArmedWatch() {
				t.Error("event watch must be budget-cleared before enqueue")
			}
			if observed == 1 && root.peekNotifications() != 0 {
				t.Error("event watch notification must not yet be queued")
			}
			if !root.hasWakeableWork() {
				t.Error("budget-cleared event watch must remain wakeable until enqueue")
			}
			root.enqueueJobNotificationAndNotify(n)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(res.WatchID)
	if ok {
		cfg.conditionFires = watchDeliveryBudget - 1
	}
	jm.mu.Unlock()
	if !ok {
		t.Fatal("event watch was not registered")
	}
	if root.hasWorkingSubagent() || child.autonomyInFlight() {
		t.Fatal("child must be idle")
	}
	jm.onSessionEvent(events.SessionEvent{
		Kind: events.EventJobFinished, SessionID: child.ID(), Data: events.JobFinishedData{},
	})
	jm.mu.Lock()
	inFlight := jm.noticesInFlight
	jm.mu.Unlock()
	if inFlight != 0 || jm.hasNoticeEmissionInFlight() || jm.hasArmedWatch() {
		t.Fatal("budget-cleared event watch must have no remaining producer signal")
	}
	if observed != 2 || root.peekNotifications() != 2 || !root.hasWakeableWork() {
		t.Fatal("event watch must hand off its event and budget-clear notifications")
	}
}

func TestSessionHasWakeableWorkJobFinalizationHandoff(t *testing.T) {
	t.Parallel()
	s := newTestSessionForState(t)
	t.Cleanup(s.Close)
	jm := s.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	pendingObserved := false
	jm.testOnlyAfterNotifyPendingAppend = func(jobID string) {
		pendingObserved = true
		if !jm.hasLiveRuns() {
			t.Error("finalizing run must stay live at the notification-pending seam")
		}
		if !jm.hasNoticeEmissionInFlight() {
			t.Error("job emission must already be in flight at the notification-pending seam")
		}
		if jobID != rec.JobID || len(sessionRunningJobIDs(s)) != 0 || s.peekNotifications() != 0 {
			t.Error("terminal job must no longer be journaled running, with notification not yet queued")
		}
		if !s.hasWakeableWork() {
			t.Error("finalizing live run must remain wakeable")
		}
	}
	enqueue := jm.enqueue
	enqueueObserved := false
	jm.enqueue = func(n jobNotification) {
		enqueueObserved = true
		if !jm.hasNoticeEmissionInFlight() {
			t.Error("job emission must remain in flight during enqueue")
		}
		if jm.hasLiveRuns() || s.peekNotifications() != 0 {
			t.Error("run must be removed before its notification reaches the queue")
		}
		if !s.hasWakeableWork() {
			t.Error("removed run must remain wakeable until enqueue")
		}
		enqueue(n)
	}
	if err := jm.finalizeWithRun(rec.JobID, func(*runningJob) (jobstore.Status, string, *int, error) {
		return jobstore.StatusCompleted, "", nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	inFlight := jm.noticesInFlight
	jm.mu.Unlock()
	if inFlight != 0 || jm.hasNoticeEmissionInFlight() || jm.hasLiveRuns() {
		t.Fatal("finalized job must have no remaining producer signal")
	}
	if !pendingObserved || !enqueueObserved || s.peekNotifications() != 1 || !s.hasWakeableWork() {
		t.Fatal("job finalization must hand off to one queued notification")
	}
}

func TestWaitingOnWorkRefusalKeepsTheSessionAlive(t *testing.T) {
	t.Parallel()
	s, adapter := newExecutionSession(t)
	adapter.script(
		respond(toolCallResponse(communicateCallArgs("comm-1", map[string]any{"message": "starting the rebase now", "end_turn": true, "end_reason": "waiting_on_work"}))),
		respond(toolCallResponse(communicateCallArgs("comm-2", map[string]any{"message": "done", "end_turn": true}))),
	)
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	communicates := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate)
	if len(communicates) != 1 || communicates[0].Communicate.EndReason != "done" {
		t.Fatalf("communicates = %+v; want exactly the done call from the correction round", communicates)
	}
}
