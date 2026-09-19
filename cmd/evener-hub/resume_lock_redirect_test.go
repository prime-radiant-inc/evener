package hub

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

// endRedirectTarget is the shape of a redirected-to session that has itself
// ended: its daemon was force-stopped and its group now awaits an explicit
// resume. RecordResolvedSession already holds a completed redirect onto it.
func endRedirectTarget(t *testing.T, locks *hubcore.ResumeLocks, targetID string) {
	t.Helper()
	finish := locks.BeginForceStop([]string{targetID})
	if err := locks.PersistForceStop([]string{targetID}, targetID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	if state := locks.RecoveryState(targetID); !state.ResumeRequired {
		t.Fatalf("force-stopped target is not awaiting an explicit resume: %+v", state)
	}
}

// A completed recovery redirect names the session a resume settled on, and the
// fork resolver follows it so a stopped alias still branches its successor.
// The redirect is written once and never cleared, so once the session it names
// has itself ended the alias has to fall back to itself: following the redirect
// routes every fork into the ended session's recovery fence, which nothing
// retires short of a hub restart.
func TestForkRedirectStopsAtARedirectWhoseTargetEnded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		end      func(t *testing.T, locks *hubcore.ResumeLocks, aliasID, targetID string)
		wantSelf bool
	}{
		{
			name: "target still settled",
		},
		{
			name:     "target awaits an explicit resume",
			end:      func(t *testing.T, l *hubcore.ResumeLocks, _, targetID string) { endRedirectTarget(t, l, targetID) },
			wantSelf: true,
		},
		{
			// A temporary stop fence is not evidence the target ended: the
			// redirect still resolves, and the target's own fence refuses the
			// fork, so the older alias is never branched underneath a running
			// daemon.
			name: "target under a held stop fence",
			end: func(t *testing.T, l *hubcore.ResumeLocks, _, targetID string) {
				l.BeginForceStop([]string{targetID})
			},
		},
		{
			// A refused force stop gives the fence back and leaves the target
			// running, so the redirect is still the right route.
			name: "target's stop fence is given back",
			end: func(t *testing.T, l *hubcore.ResumeLocks, _, targetID string) {
				finish := l.BeginForceStop([]string{targetID})
				finish.Reject()
				finish.Finish(false)
			},
		},
		{
			name: "pending recovery target is still followed",
			end: func(t *testing.T, l *hubcore.ResumeLocks, aliasID, targetID string) {
				finish := l.BeginForceStop([]string{aliasID, targetID})
				if err := l.PersistForceStop([]string{aliasID, targetID}, targetID); err != nil {
					t.Fatal(err)
				}
				finish.Finish(true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliasID, targetID, _ := resumeThreeSessionIDs(t)
			locks := hubcore.NewResumeLocks()
			recordResumeRedirect(t, locks, aliasID, targetID)
			if tc.end != nil {
				tc.end(t, locks, aliasID, targetID)
			}
			want := targetID
			if tc.wantSelf {
				want = aliasID
			}
			cfg := hubcore.WebConfig{ResumeLocks: locks}
			if got := forkRedirectSessionID(cfg, aliasID); got != want {
				t.Fatalf("redirect resolved to %q, want %q", got, want)
			}
		})
	}
}

// The ended target's own id is still fenced, so a fork of it is refused; only
// the alias's stale redirect is retired. The same resolver answers the fork
// capability projection and the fork RPC, so both stop routing through the
// ended session.
func TestHubForkRetiresARedirectWhoseTargetEnded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endTarget  bool
		stopTarget bool
		refuseStop bool
		wantTarget bool
	}{
		{name: "target still settled", wantTarget: true},
		{name: "target awaits an explicit resume", endTarget: true},
		// A temporary stop fence leaves the redirect in force, so the fork is
		// still refused through the target's own fence rather than branching
		// the older alias while the target's daemon may still be running.
		{name: "target under a held stop fence", stopTarget: true},
		// The failed force stop gives its fence back and leaves the target
		// running, so the redirect is still the right route and the fork
		// branches the target rather than falling back to the alias.
		{name: "target's stop fence was refused", refuseStop: true, wantTarget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			aliasID := buildRPCParentSession(t, stateDir)
			targetID, err := identifier.NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			buildRPCSessionWithWorkingDir(t, stateDir, targetID, t.TempDir())
			locks := hubcore.NewResumeLocks()
			recordResumeRedirect(t, locks, aliasID, targetID)
			if tc.endTarget {
				endRedirectTarget(t, locks, targetID)
			}
			if tc.stopTarget {
				finish := locks.BeginForceStop([]string{targetID})
				t.Cleanup(func() { finish.Finish(false) })
			}
			if tc.refuseStop {
				finish := locks.BeginForceStop([]string{targetID})
				finish.Reject()
				finish.Finish(false)
			}
			cfg := hubcore.WebConfig{
				StateDir: stateDir, RunDir: t.TempDir(),
				Roster: hubcore.NewRosterWithEntries(), ResumeLocks: locks,
			}
			resp, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + aliasID, SourceTurnID: "turn_1", EditedInput: "forked input",
			})
			if tc.stopTarget {
				if !isSessionRecoveryAdmissionError(err) {
					t.Fatalf("fork under a held stop fence error=%v, want the recovery refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("fork through a redirect whose target ended: %v", err)
			}
			meta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := aliasID
			if tc.wantTarget {
				want = targetID
			}
			if meta.ParentSessionID != want {
				t.Fatalf("fork branched %q, want %q", meta.ParentSessionID, want)
			}
			if !tc.endTarget {
				return
			}
			// Retiring the alias's redirect does not unfence the ended
			// target itself: a fork of it is still refused.
			if _, err := hubThreadFork(t.Context(), cfg, nil, appwire.ThreadForkParams{
				Ref: "local:" + targetID, SourceTurnID: "turn_1", EditedInput: "forked input",
			}); !isSessionRecoveryAdmissionError(err) {
				t.Fatalf("fork of the ended target error=%v, want the recovery refusal", err)
			}
		})
	}
}
