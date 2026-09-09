package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestForkRespectsSourceRecoveryAdmission(t *testing.T) {
	for _, aside := range []bool{false, true} {
		name := "fork"
		if aside {
			name = "aside"
		}
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			parent := buildRPCParentSession(t, stateDir)
			locks := hubcore.NewResumeLocks()
			cfg := hubcore.WebConfig{StateDir: stateDir, ResumeLocks: locks}
			params := appwire.ThreadForkParams{Ref: localAppRef(parent), Aside: aside}
			if !aside {
				params.SourceTurnID, params.DeferInput = "turn_1", true
			}
			queued := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodThreadFork, params))
			finish := locks.BeginForceStop([]string{parent})
			if err := locks.PersistForceStop([]string{parent}, parent); err != nil {
				t.Fatal(err)
			}
			assertBlocked := func() {
				t.Helper()
				if _, err := hubThreadFork(queued, cfg, nil, params); !isSessionRecoveryAdmissionError(err) {
					t.Fatalf("fork recovery error = %v, want recovery admission refusal", err)
				}
				metas, err := schema.ListSessionMetas(stateDir)
				if err != nil || len(metas) != 1 {
					t.Fatalf("blocked fork created session metadata: count=%d err=%v", len(metas), err)
				}
			}
			assertBlocked()
			finish(true)
			assertBlocked()
			if err := locks.ExplicitResumeCompleted(parent, locks.RecoveryState(parent).Epoch); err != nil {
				t.Fatal(err)
			}
			assertBlocked()
			fresh := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadFork, params))
			response, err := hubThreadFork(fresh, cfg, nil, params)
			if err != nil || response.Thread.ID == "" || response.Thread.ID == parent {
				t.Fatalf("fresh fork after recovery = %+v, %v", response, err)
			}
		})
	}
}
