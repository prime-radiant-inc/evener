package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// TestSessionHasWakeableWork pins every wake source hasWakeableWork counts
// for a resting session, plus the bare session it must refuse: a wake exists
// when autonomous work is in flight, a question waits on the user, a detached
// process still owns an open completion receipt, or any holder manager keeps
// an armed watch that can deliver a wake.
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
			name: "running detached process wakes",
			want: true,
			seed: func(_ *testing.T, s *Session) {
				s.recordDetachedProcess(execenv.DetachedProcess{PID: 4321, Done: make(chan struct{})})
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
