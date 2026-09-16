package hubcore

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestResumeMutexCanceledWaiterDoesNotAcquireOrReleaseOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := NewResumeLocks()
		lock := locks.For("owner")
		lock.Lock()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result := make(chan error, 1)
		go func() { result <- lock.LockContext(ctx) }()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("lock cancellation = %v", err)
		}
		if lock.TryLock() {
			lock.Unlock()
			t.Fatal("canceled waiter released the owner's lock")
		}
		lock.Unlock()
		if err := lock.LockContext(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("already canceled context acquired available lock: %v", err)
		}
		if !lock.TryLock() {
			t.Fatal("canceled acquisition retained the token")
		}
		lock.Unlock()
		if locks.For("owner") != lock {
			t.Fatal("session lock identity changed")
		}
	})
}

func TestResumeMutexOrdinaryLockStillExcludes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lock := NewResumeLocks().For("owner")
		lock.Lock()
		acquired := make(chan struct{})
		go func() {
			lock.Lock()
			close(acquired)
			lock.Unlock()
		}()
		synctest.Wait()
		select {
		case <-acquired:
			t.Fatal("ordinary Lock bypassed held ownership")
		default:
		}
		lock.Unlock()
		<-acquired
		synctest.Wait()
	})
}

func resumeEpochs(locks *ResumeLocks, aliases ...string) map[string]uint64 {
	epochs := make(map[string]uint64, len(aliases))
	for _, alias := range aliases {
		epochs[alias] = locks.RecoveryState(alias).Epoch
	}
	return epochs
}

func TestActiveResumeRejectsMalformedAliasesAndStaleEpochs(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		aliases      []string
		epochs       map[string]uint64
	}{
		{"empty aliases", "owner", nil, nil},
		{"missing target", "other", []string{"owner"}, map[string]uint64{"owner": 0}},
		{"empty alias", "owner", []string{"owner", ""}, map[string]uint64{"owner": 0, "": 0}},
		{"path alias", "owner", []string{"owner", "../other"}, map[string]uint64{"owner": 0, "../other": 0}},
		{"qualified alias", "owner", []string{"owner", "local:other"}, map[string]uint64{"owner": 0, "local:other": 0}},
		{"missing epoch", "owner", []string{"owner"}, nil},
		{"stale epoch", "owner", []string{"owner"}, map[string]uint64{"owner": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locks := NewResumeLocks()
			active, err := locks.RegisterResume(t.Context(), tc.target, tc.aliases, tc.epochs)
			if err == nil {
				active.Complete(nil)
				t.Fatal("invalid registration accepted")
			}
			if stop := locks.BeginActiveResumeStop("owner"); stop != nil {
				stop.Release()
				t.Fatal("failed registration remained indexed")
			}
		})
	}
}

func TestActiveResumeStopCancelsOverlappingAliasesAndWaitsForCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := NewResumeLocks()
		first, err := locks.RegisterResume(t.Context(), "current", []string{"stable", "current", "stable"}, resumeEpochs(locks, "stable", "current"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := locks.RegisterResume(t.Context(), "current", []string{"current", "next"}, resumeEpochs(locks, "current", "next"))
		if err != nil {
			t.Fatal(err)
		}
		other, err := locks.RegisterResume(t.Context(), "other", []string{"other"}, resumeEpochs(locks, "other"))
		if err != nil {
			t.Fatal(err)
		}
		defer other.Complete(nil)
		stop := locks.BeginActiveResumeStop("current")
		if stop == nil {
			t.Fatal("active alias did not resolve")
		}
		defer stop.Release()
		for _, active := range []*ActiveResume{first, second} {
			if !errors.Is(active.Context().Err(), context.Canceled) {
				t.Fatal("overlapping operation was not canceled")
			}
		}
		if other.Context().Err() != nil {
			t.Fatal("unrelated operation was canceled")
		}
		for _, alias := range []string{"stable", "current", "next"} {
			state := locks.RecoveryState(alias)
			if state.Stopping != 1 || state.Epoch != 1 {
				t.Fatalf("alias %s was not fenced exactly once: %+v", alias, state)
			}
			if active, err := locks.RegisterResume(t.Context(), alias, []string{alias}, resumeEpochs(locks, alias)); err == nil {
				active.Complete(nil)
				t.Fatalf("new registration crossed stop fence for %s", alias)
			}
		}
		waited := make(chan error, 1)
		go func() { waited <- stop.Wait(t.Context()) }()
		first.Complete(nil)
		synctest.Wait()
		select {
		case err := <-waited:
			t.Fatalf("Stop returned before second cleanup: %v", err)
		default:
		}
		cleanupErr := errors.New("fixture cleanup was not confirmed")
		second.Complete(cleanupErr)
		if err := <-waited; !errors.Is(err, cleanupErr) {
			t.Fatalf("cleanup error lost: %v", err)
		}
		if active := locks.BeginActiveResumeStop("current"); active != nil {
			active.Release()
			t.Fatal("completed registrations remained indexed")
		}
	})
}

func TestActiveResumeStopWaitCancellationAndFenceRelease(t *testing.T) {
	locks := NewResumeLocks()
	active, err := locks.RegisterResume(t.Context(), "owner", []string{"owner"}, resumeEpochs(locks, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	stop := locks.BeginActiveResumeStop("owner")
	if stop == nil {
		t.Fatal("missing active stop")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := stop.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Stop wait = %v", err)
	}
	active.Complete(nil)
	stop.Release()
	if state := locks.RecoveryState("owner"); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("temporary cancellation fabricated stopped authority: %+v", state)
	}
	if stale, err := locks.RegisterResume(t.Context(), "owner", []string{"owner"}, map[string]uint64{"owner": 0}); err == nil {
		stale.Complete(nil)
		t.Fatal("pre-Stop epoch admitted after fence release")
	}
	fresh, err := locks.RegisterResume(t.Context(), "owner", []string{"owner"}, resumeEpochs(locks, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	fresh.Complete(nil)
}

func TestActiveResumeSharesExistingForceStopFence(t *testing.T) {
	locks := NewResumeLocks()
	epochs := resumeEpochs(locks, "owner")
	finish := locks.BeginForceStop([]string{"owner"})
	if active, err := locks.RegisterResume(t.Context(), "owner", []string{"owner"}, epochs); err == nil {
		active.Complete(nil)
		t.Fatal("registration crossed existing ForceStop")
	}
	finish(false)
}

func TestActiveResumeReapedCleanupPreservesRecoveryAuthority(t *testing.T) {
	for _, newer := range []bool{false, true} {
		name := "own_stop_epoch"
		if newer {
			name = "newer_overlapping_authority"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			locks, err := NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			aliases := []string{"owner", "stable"}
			if err := locks.PersistForceStop(aliases, "owner"); err != nil {
				t.Fatal(err)
			}
			if err := locks.ConfirmForceStop("owner"); err != nil {
				t.Fatal(err)
			}
			before := locks.RecoveryState("owner")
			active, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
			if err != nil {
				t.Fatal(err)
			}
			if err := active.BeforeLaunch(); err != nil {
				t.Fatal(err)
			}
			stop := locks.BeginActiveResumeStop("stable")
			if stop == nil {
				t.Fatal("missing owned Stop")
			}
			cleanupErr := errors.New("fixture child kill denied")
			if err := active.LaunchFinished(true, cleanupErr); !errors.Is(err, cleanupErr) {
				t.Fatalf("launch lost cleanup failure: %v", err)
			}
			active.Complete(cleanupErr)
			if err := stop.Wait(t.Context()); !errors.Is(err, cleanupErr) {
				t.Fatalf("Stop lost unconfirmed cleanup: %v", err)
			}
			stop.Release()
			if locks.RecoveryState("owner").Epoch == before.Epoch {
				t.Fatal("fixture did not advance own Stop epoch")
			}
			if !locks.HasActiveResume(aliases) {
				t.Fatal("handler completion forgot unreaped child")
			}
			if next, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...)); err == nil {
				next.Complete(nil)
				t.Fatal("fresh Resume passed retained failure")
			}
			if newer {
				if err := locks.PersistForceStop([]string{"next", "owner"}, "next"); err != nil {
					t.Fatal(err)
				}
			}
			prior := map[string]SessionRecoveryState{}
			for _, alias := range []string{"owner", "stable", "next"} {
				prior[alias] = locks.RecoveryState(alias)
			}
			active.ChildReaped()
			<-active.CleanupDone()
			if locks.HasActiveResume(aliases) {
				t.Fatal("actual reaping retained settled child ownership")
			}
			recreated, err := NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			if newer {
				for alias, state := range prior {
					if after := locks.RecoveryState(alias); after != state {
						t.Errorf("old child changed newer authority for %s: before=%+v after=%+v", alias, state, after)
					}
				}
				if state := recreated.RecoveryState("owner"); state.ExitConfirmed || state.ResumeSessionID != "next" {
					t.Fatalf("old reaping confirmed newer durable owner: %+v", state)
				}
			} else {
				for _, alias := range aliases {
					state := locks.RecoveryState(alias)
					if !state.ExitConfirmed || !state.ResumeRequired || state.Epoch != prior[alias].Epoch || !recreated.RecoveryState(alias).ExitConfirmed {
						t.Fatalf("own Stop epoch prevented proven cleanup recovery for %s: %+v", alias, state)
					}
				}
				next, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
				if err != nil {
					t.Fatal(err)
				}
				next.Complete(nil)
			}
		})
	}
}

func TestActiveResumeQueuedLaunchCannotPassFailedChildWithoutRecovery(t *testing.T) {
	locks := NewResumeLocks()
	aliases := []string{"owner"}
	first, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
	if err != nil {
		t.Fatal(err)
	}
	defer queued.Complete(nil)
	if err := first.BeforeLaunch(); err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("fixture cleanup failure without durable recovery")
	if err := first.LaunchFinished(true, cleanupErr); !errors.Is(err, cleanupErr) {
		t.Fatal(err)
	}
	// The queued registration already exists and may acquire ownership before
	// the first handler's Complete runs. Launch itself must see the live child.
	if err := queued.BeforeLaunch(); !errors.Is(err, cleanupErr) {
		t.Fatalf("queued launch passed unconfirmed child: %v", err)
	}
	first.Complete(cleanupErr)
	first.ChildReaped()
	<-first.CleanupDone()
}

func TestActiveResumeProofPersistenceFailures(t *testing.T) {
	for _, phase := range []string{"before_launch", "after_reaping"} {
		for _, edge := range []string{"before_rename", "after_rename"} {
			t.Run(phase+"/"+edge, func(t *testing.T) {
				locks, err := NewPersistentResumeLocks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				aliases := []string{"owner"}
				if err := locks.PersistForceStop(aliases, "owner"); err != nil {
					t.Fatal(err)
				}
				if err := locks.ConfirmForceStop("owner"); err != nil {
					t.Fatal(err)
				}
				active, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
				if err != nil {
					t.Fatal(err)
				}
				boom := errors.New("fixture proof persistence failure")
				installFailure := func() {
					if edge == "before_rename" {
						locks.store.faults.BeforeRename = func() error { return boom }
					} else {
						locks.store.faults.AfterRename = func() error { return boom }
					}
				}
				if phase == "before_launch" {
					installFailure()
					if err := active.BeforeLaunch(); !errors.Is(err, boom) {
						t.Fatalf("launch permitted despite proof write failure: %v", err)
					}
					active.Complete(nil)
				} else {
					if err := active.BeforeLaunch(); err != nil {
						t.Fatal(err)
					}
					installFailure()
					cleanupErr := errors.New("fixture kill denied")
					if err := active.LaunchFinished(true, cleanupErr); !errors.Is(err, cleanupErr) {
						t.Fatal(err)
					}
					active.Complete(cleanupErr)
					active.ChildReaped()
					if err := locks.ResumeCleanupError(aliases); !errors.Is(err, boom) || !errors.Is(err, cleanupErr) {
						t.Fatalf("re-confirmation lost retained causes: %v", err)
					}
					if !locks.HasActiveResume(aliases) || locks.RecoveryState("owner").ExitConfirmed {
						t.Fatal("failed proof persistence authorized replacement")
					}
				}
				locks.store.faults.BeforeRename = nil
				locks.store.faults.AfterRename = nil
				if err := locks.ResumeCleanupError(aliases); err != nil {
					t.Fatalf("proof retry did not settle after storage recovery: %v", err)
				}
				<-active.CleanupDone()
				if locks.HasActiveResume(aliases) || !locks.RecoveryState("owner").ExitConfirmed {
					t.Fatal("settled cleanup did not restore truthful exit proof")
				}
			})
		}
	}
}
