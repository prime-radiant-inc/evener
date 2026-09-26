package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

// The registration models an active launch at its ownership boundary; real hub
// RPC dispatch and forceStopThread must cancel it and await cleanup before the
// scripted external process controller may inspect or signal a ready daemon.
func TestLongRunningResumeStopAwaitsCleanupThenUsesProcessProof(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "no rendezvous", true: "ready handoff"}[ready], func(t *testing.T) {
			runDir := t.TempDir()
			sessionID := hubtest.SessionID(t)
			locks := hubcore.NewResumeLocks()
			active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: 0})
			if err != nil {
				t.Fatal(err)
			}
			entry := rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()}
			if ready {
				writeRendezvous(t, runDir, entry)
			}
			cleanupCompleted := make(chan struct{})
			var events []string
			controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
				select {
				case <-cleanupCompleted:
				default:
					t.Error("process discovery ran before active Resume cleanup completed")
				}
				if target.PID != entry.PID || target.SessionID != sessionID || !target.StartedAt.Equal(entry.StartedAt) {
					t.Errorf("unverified target: %+v", target)
				}
				events = append(events, "open")
				return &forceStopProcess{events: &events}, nil
			})
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: controller})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				var response appwire.EmptyResponse
				result <- client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, &response)
			}()
			select {
			case <-active.Context().Done():
			case err := <-result:
				active.Complete(nil)
				t.Fatalf("Stop returned without canceling active Resume: %v", err)
			}
			if !errors.Is(active.Context().Err(), context.Canceled) {
				t.Fatalf("Resume cancellation = %v", active.Context().Err())
			}
			close(cleanupCompleted)
			active.Complete(nil)
			err = <-result
			if (err == nil) != ready {
				t.Fatalf("Stop result ready=%t: %v", ready, err)
			}
			var want []string
			if ready {
				want = []string{"open", "kill", "wait", "close"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("process proof events=%v want=%v", events, want)
			}
			state := locks.RecoveryState(sessionID)
			if state.Stopping != 0 || state.ExitConfirmed != ready {
				t.Fatalf("incorrect stopped authority: %+v", state)
			}
		})
	}
}

// Fake only the external child process. The real Resume launcher owns log
// setup, deadline selection, rendezvous matching, failure wrapping and reaping.
func TestLongRunningResumeCompletionPolicyAndLateOutcome(t *testing.T) {
	for _, outcome := range []string{"ready", "failed", "automatic timeout"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runDir := t.TempDir()
				sessionID := hubtest.SessionID(t)
				exit := make(chan error, 1)
				defer close(exit)
				waiting := make(chan struct{})
				exited := make(chan struct{})
				killed := make(chan struct{}, 1)
				original := startResumeChild
				defer func() { startResumeChild = original }()
				const diagnostic = "FIXTURE_LATE_RESTORE_DIAGNOSTIC"
				startResumeChild = func(cmd *exec.Cmd) (resumeChild, error) {
					if _, err := fmt.Fprintln(cmd.Stderr, diagnostic); err != nil {
						return resumeChild{}, err
					}
					return resumeChild{pid: 4242, kill: func() error {
						select {
						case <-exited:
							return os.ErrProcessDone
						default:
							killed <- struct{}{}
							return nil
						}
					}, wait: func() error {
						close(waiting) // A second Wait would panic instead of silently consuming twice.
						err := <-exit
						close(exited)
						return err
					}}, nil
				}
				type result struct {
					entry rendezvous.Entry
					err   error
				}
				completed := make(chan result, 1)
				budget := DefaultConfig().SpawnTimeout
				go func() {
					entry, err := resumeDaemon(t.Context(), "fixture-evener", runDir, hubcore.ResumeRequest{
						SessionID: sessionID, CompletionOwned: outcome != "automatic timeout",
					}, budget, io.Discard)
					completed <- result{entry, err}
				}()
				<-waiting
				time.Sleep(budget) // synctest advances the existing startup deadline.
				synctest.Wait()
				select {
				case got := <-completed:
					t.Fatalf("launcher returned before readiness/exit/reaping: %+v", got)
				default:
				}
				if outcome == "automatic timeout" {
					<-killed
					exit <- nil
					if got := <-completed; !errors.Is(got.err, errRendezvousTimeout) {
						t.Fatalf("automatic Resume lost startup budget: %v", got.err)
					}
					return
				}
				select {
				case <-killed:
					t.Fatal("explicit Resume killed at ordinary startup deadline")
				default:
				}
				if outcome == "ready" {
					writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, StartedAt: time.Now()})
					got := <-completed
					if got.err != nil || got.entry.SessionID != sessionID {
						t.Fatalf("late readiness: %+v", got)
					}
					select {
					case <-killed:
						t.Fatal("successful daemon was killed instead of handed off")
					default:
					}
					exit <- nil
					<-exited
				} else {
					failure := errors.New("fixture child exit failure")
					exit <- failure
					got := <-completed
					if !errors.Is(got.err, failure) || !strings.Contains(got.err.Error(), diagnostic) {
						t.Fatalf("late child failure lost its cause/diagnostic: %v", got.err)
					}
				}
			})
		})
	}
}

func TestLongRunningResumeCancellationWaitsForReaping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := startResumeChild
		defer func() { startResumeChild = original }()
		waiting, killed, allowExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
		startResumeChild = func(*exec.Cmd) (resumeChild, error) {
			return resumeChild{pid: 4242, kill: func() error { close(killed); return nil }, wait: func() error {
				close(waiting)
				<-allowExit
				return nil
			}}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		go func() {
			_, err := resumeDaemon(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{SessionID: sessionID, CompletionOwned: true}, DefaultConfig().SpawnTimeout, io.Discard)
			completed <- err
		}()
		<-waiting
		cancel()
		<-killed
		synctest.Wait()
		select {
		case err := <-completed:
			t.Fatalf("Resume returned before child reaping: %v", err)
		default:
		}
		close(allowExit)
		if err := <-completed; !errors.Is(err, errRendezvousCanceled) || errors.Is(err, errRendezvousTimeout) {
			t.Fatalf("cancellation sentinel was replaced: %v", err)
		}
	})
}

func TestLongRunningResumeCanceledAliasWaitReleasesAcquiredPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		aliases := []string{"a-stable", "z-current"}
		finish := locks.BeginForceStop(aliases)
		if err := locks.PersistForceStop(aliases, "z-current"); err != nil {
			t.Fatal(err)
		}
		finish.Finish(true)
		blocked := locks.For("z-current")
		blocked.Lock()
		defer blocked.Unlock()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
		go func() {
			_, err := hubThreadResume(ctx, cfg, nil, appwire.ThreadResumeParams{Ref: "local:a-stable"})
			completed <- err
		}()
		synctest.Wait()
		if locks.For("a-stable").TryLock() {
			locks.For("a-stable").Unlock()
			t.Fatal("Resume did not reach the held second alias")
		}
		cancel()
		if err := <-completed; !errors.Is(err, context.Canceled) {
			t.Fatalf("ownership wait cancellation = %v", err)
		}
		if !locks.For("a-stable").TryLock() {
			t.Fatal("canceled Resume retained its acquired prefix")
		}
		locks.For("a-stable").Unlock()
		if blocked.TryLock() {
			blocked.Unlock()
			t.Fatal("canceled Resume released another owner's alias")
		}
		if stop := locks.BeginActiveResumeStop("a-stable"); stop != nil {
			stop.Release()
			t.Fatal("canceled ownership waiter remained registered")
		}
	})
}

func TestLongRunningResumeRepeatStopUsesDurableConfirmedAuthority(t *testing.T) {
	for _, recreated := range []bool{false, true} {
		t.Run(map[bool]string{false: "same hub", true: "recreated hub"}[recreated], func(t *testing.T) {
			root, runDir := t.TempDir(), t.TempDir()
			locks, err := hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			stable, current := hubtest.SessionID(t), hubtest.SessionID(t)
			aliases := []string{stable, current}
			finish := locks.BeginForceStop(aliases)
			if err := locks.PersistForceStop(aliases, current); err != nil {
				t.Fatal(err)
			}
			if err := locks.ConfirmForceStop(current); err != nil {
				t.Fatal(err)
			}
			finish.Finish(true)
			// No rendezvous remains; confirmed authority must survive hub recreation.
			if recreated {
				locks, err = hubcore.NewPersistentResumeLocks(root)
				if err != nil {
					t.Fatal(err)
				}
			}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("confirmed marker-free Stop attempted process control")
				return nil, errors.New("unexpected process control")
			})}
			for range 2 {
				if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stable}, nil); err != nil {
					t.Fatal(err)
				}
			}
			reloaded, err := hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, registry := range []*hubcore.ResumeLocks{locks, reloaded} {
				for _, alias := range aliases {
					state := registry.RecoveryState(alias)
					if !state.ResumeRequired || !state.ExitConfirmed || state.ResumeSessionID != current || state.Stopping != 0 {
						t.Fatalf("repeat Stop changed durable recovery: %+v", state)
					}
				}
			}
		})
	}
}

func TestLongRunningResumeConfirmedStopDoesNotHideOtherAliasClaims(t *testing.T) {
	for _, kind := range []string{"other durable alias", "foreign direct claim", "unconfirmed exit"} {
		t.Run(kind, func(t *testing.T) {
			locks := hubcore.NewResumeLocks()
			stable, current := hubtest.SessionID(t), hubtest.SessionID(t)
			finish := locks.BeginForceStop([]string{stable, current})
			if err := locks.PersistForceStop([]string{stable, current}, current); err != nil {
				t.Fatal(err)
			}
			if kind != "unconfirmed exit" {
				if err := locks.ConfirmForceStop(current); err != nil {
					t.Fatal(err)
				}
			}
			finish.Finish(true)
			runDir := t.TempDir()
			if kind != "unconfirmed exit" {
				entry := rendezvous.Entry{PID: 4242, SessionID: current, ThreadID: current, StartedAt: time.Now()}
				if kind == "foreign direct claim" {
					entry.SessionID, entry.ThreadID, entry.SourceID = stable, stable, "foreign"
				}
				writeRendezvous(t, runDir, entry)
			}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("incompatible ownership reached process control")
				return nil, errors.New("unexpected process control")
			})}
			if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stable}, nil); err == nil {
				t.Fatal("incompatible ownership or missing proof became no-op success")
			}
		})
	}
}

func TestLongRunningResumeStopCancelsRegistrationDuringDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()})
		canceled, allowCleanup, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var events []string
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			events = append(events, "open")
			active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
			if err != nil {
				return nil, err
			}
			locks.For(sessionID).Lock()
			go func() {
				<-active.Context().Done()
				close(canceled)
				<-allowCleanup
				locks.For(sessionID).Unlock()
				close(cleaned)
				active.Complete(nil)
			}()
			return &forceStopProcess{events: &events, onWait: func() {
				select {
				case <-cleaned:
				default:
					t.Error("verified process stop ran before active launch cleanup")
				}
			}}, nil
		})}
		completed := make(chan error, 1)
		go func() {
			completed <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		<-canceled
		synctest.Wait()
		select {
		case err := <-completed:
			t.Fatalf("Stop returned before raced Resume cleanup: %v", err)
		default:
		}
		close(allowCleanup)
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(events, []string{"open", "kill", "wait", "close"}) {
			t.Fatalf("process verification changed: %v", events)
		}
	})
}

func TestLongRunningResumeHubCancelsOwnedChild(t *testing.T) {
	for _, reason := range []string{"disconnect", "stop"} {
		t.Run(reason, func(t *testing.T) {
			root := t.TempDir()
			chdirTemp(t, root)
			t.Setenv(envvars.XDGStateHome.Name, filepath.Join(root, "state-home"))
			runDir := filepath.Join(root, "run")
			if err := os.MkdirAll(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "fixture-evener")
			writeFakeEvener(t, binary, "#!/bin/sh\nif [ \"$1\" = launch-check ]; then\n printf '%s\\n' '{\"protocol\":\"evener-appwire-v5\",\"launch_flags\":[\"api-log\"]}'\n exit 0\nfi\nexit 2\n")
			waiting, killed, allowExit := make(chan struct{}), make(chan struct{}), make(chan struct{})
			original := startResumeChild
			defer func() { startResumeChild = original }()
			startResumeChild = func(*exec.Cmd) (resumeChild, error) {
				return resumeChild{pid: 4242, kill: func() error { close(killed); return nil }, wait: func() error {
					close(waiting)
					<-allowExit
					return nil
				}}, nil
			}
			locks := hubcore.NewResumeLocks()
			spawner := &HubSpawner{Cfg: DefaultConfig(), RunDir: runDir, EvenerBinary: binary}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, Spawner: spawner})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			sessionID := hubtest.SessionID(t)
			resumed := make(chan error, 1)
			go func() {
				var response appwire.ThreadResumeResponse
				resumed <- client.Request(t.Context(), "thread/resume", appwire.ThreadResumeParams{Ref: "local:" + sessionID}, &response)
			}()
			select {
			case <-waiting:
			case err := <-resumed:
				t.Fatalf("Resume did not reach child launch: %v", err)
			}
			var stopped chan error
			if reason == "disconnect" {
				client.Close()
			} else {
				recovery := dialHubRPC(t, hub)
				defer recovery.Close()
				if _, err := recovery.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				stopped = make(chan error, 1)
				go func() {
					var response appwire.EmptyResponse
					stopped <- recovery.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, &response)
				}()
			}
			<-killed
			close(allowExit)
			if err := <-resumed; err == nil {
				t.Fatal("canceled Resume reported success")
			}
			if stopped != nil {
				if err := <-stopped; err == nil {
					t.Fatal("pre-ready cleanup alone fabricated durable stopped authority")
				}
			}
			// Ownership cannot become available until the real launcher waited for
			// its scripted child's exit; a leaked alias mutex blocks this await.
			if err := locks.For(sessionID).LockContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			locks.For(sessionID).Unlock()
		})
	}
}

func TestLongRunningResumeShutdownProofNeverCancelsOrKills(t *testing.T) {
	for _, reason := range []string{"active resume", "daemon claim"} {
		t.Run(reason, func(t *testing.T) {
			locks := hubcore.NewResumeLocks()
			sessionID := hubtest.SessionID(t)
			finish := locks.BeginForceStop([]string{sessionID})
			if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
				t.Fatal(err)
			}
			if err := locks.ConfirmForceStop(sessionID); err != nil {
				t.Fatal(err)
			}
			finish.Finish(true)
			before := locks.RecoveryState(sessionID)
			runDir := t.TempDir()
			var active *hubcore.ActiveResume
			if reason == "active resume" {
				var err error
				active, err = locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: before.Epoch})
				if err != nil {
					t.Fatal(err)
				}
				defer active.Complete(nil)
			} else {
				writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()})
			}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("ordinary shutdown attempted force-stop process control")
				return nil, errors.New("unexpected process control")
			})}
			if err := shutdownThreadTolerateExited(t.Context(), cfg, nil, appwire.ThreadShutdownParams{Ref: "local:" + sessionID}); err == nil {
				t.Fatal("unproven shutdown became no-op success")
			}
			if active != nil && active.Context().Err() != nil {
				t.Fatal("ordinary shutdown canceled active Resume")
			}
			if after := locks.RecoveryState(sessionID); after != before {
				t.Fatalf("ordinary shutdown mutated recovery: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestLongRunningResumeCleanupErrorPreservesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := startResumeChild
		defer func() { startResumeChild = original }()
		waiting, allowExit := make(chan struct{}), make(chan struct{})
		defer close(allowExit)
		killErr := errors.New("fixture kill denied")
		startResumeChild = func(*exec.Cmd) (resumeChild, error) {
			return resumeChild{pid: 4242, kill: func() error { return killErr }, wait: func() error {
				close(waiting)
				<-allowExit
				return nil
			}}, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		go func() {
			_, err := resumeDaemon(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{SessionID: sessionID, CompletionOwned: true}, DefaultConfig().SpawnTimeout, io.Discard)
			completed <- err
		}()
		<-waiting
		cancel()
		err := <-completed
		var cleanup *resumeCleanupError
		if !errors.Is(err, errRendezvousCanceled) || !errors.Is(err, killErr) || !errors.As(err, &cleanup) {
			t.Fatalf("cleanup replaced the original cancellation or signal cause: %v", err)
		}
	})
}

// TestLongRunningResumeCleanupErrorIsNotNested pins finishLaunch's
// classification boundary: LaunchFinished already retains the child-cleanup
// failure as a *resumeCleanupError, so finishLaunch must return that
// classification unchanged instead of wrapping it in a second one and
// duplicating the "cleanup is unconfirmed" text.
func TestLongRunningResumeCleanupErrorIsNotNested(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := startResumeChild
		defer func() { startResumeChild = original }()
		waiting, allowExit := make(chan struct{}), make(chan struct{})
		defer close(allowExit)
		killErr := errors.New("fixture kill denied")
		startResumeChild = func(*exec.Cmd) (resumeChild, error) {
			return resumeChild{pid: 4242, kill: func() error { return killErr }, wait: func() error {
				close(waiting)
				<-allowExit
				return nil
			}}, nil
		}
		locks := hubcore.NewResumeLocks()
		sessionID := hubtest.SessionID(t)
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		runDir := t.TempDir()
		go func() {
			_, err := resumeDaemon(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{SessionID: sessionID, CompletionOwned: true, ActiveResume: active}, DefaultConfig().SpawnTimeout, io.Discard)
			completed <- err
		}()
		<-waiting
		cancel()
		err = <-completed
		var cleanup *resumeCleanupError
		if !errors.As(err, &cleanup) || !errors.Is(err, killErr) {
			t.Fatalf("cleanup failure lost its classification or cause: %v", err)
		}
		if _, ok := errors.AsType[*resumeCleanupError](cleanup.Unwrap()); ok {
			t.Fatalf("cleanup failure classified twice: %v", err)
		}
		if count := strings.Count(err.Error(), "resume child cleanup is unconfirmed"); count != 1 {
			t.Fatalf("cleanup message duplicated %d times: %v", count, err)
		}
		active.Complete(nil)
	})
}

func TestLongRunningResumeFailedCleanupRetainsOwnership(t *testing.T) {
	root := t.TempDir()
	chdirTemp(t, root)
	t.Setenv(envvars.XDGStateHome.Name, filepath.Join(root, "state-home"))
	runDir, recoveryRoot := filepath.Join(root, "run"), filepath.Join(root, "recovery")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture-evener")
	writeFakeEvener(t, binary, "#!/bin/sh\nif [ \"$1\" = launch-check ]; then\n printf '%s\\n' '{\"protocol\":\"evener-appwire-v5\",\"launch_flags\":[\"api-log\"]}'\n exit 0\nfi\nexit 2\n")
	locks, err := hubcore.NewPersistentResumeLocks(recoveryRoot)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := hubtest.SessionID(t)
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(sessionID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	waiting, allowExit, reaped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseChild := sync.OnceFunc(func() { close(allowExit) })
	defer releaseChild()
	var launches atomic.Int32
	killErr := errors.New("FIXTURE_KILL_DENIED")
	retryErr := errors.New("FIXTURE_RETRY_REACHED_LAUNCHER")
	original := startResumeChild
	defer func() { startResumeChild = original }()
	startResumeChild = func(*exec.Cmd) (resumeChild, error) {
		if launches.Add(1) != 1 {
			return resumeChild{}, retryErr
		}
		return resumeChild{pid: 4242, kill: func() error { return killErr }, wait: func() error {
			close(waiting)
			<-allowExit
			close(reaped)
			return nil
		}}, nil
	}
	spawner := &HubSpawner{Cfg: DefaultConfig(), RunDir: runDir, EvenerBinary: binary}
	operations := make(chan *hubcore.ActiveResume, 1)
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, Spawner: &fakeRPCSpawner{resume: func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		select {
		case operations <- req.ActiveResume:
		default:
		}
		return spawner.Resume(ctx, req)
	}}}
	hub := newHubRPCTestServer(t, cfg)
	defer func() { releaseChild(); hub.Close() }()
	newClient := func() *appwire.Client {
		client := dialHubRPC(t, hub)
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
			t.Fatal(err)
		}
		return client
	}
	primary := newClient()
	resumed := make(chan error, 1)
	go func() {
		var response appwire.ThreadResumeResponse
		resumed <- primary.Request(t.Context(), "thread/resume", appwire.ThreadResumeParams{Ref: "local:" + sessionID}, &response)
	}()
	select {
	case <-waiting:
	case err := <-resumed:
		t.Fatalf("initial Resume did not launch its child: %v", err)
	}
	active := <-operations
	var response appwire.EmptyResponse
	if err := newClient().Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, &response); err == nil || !strings.Contains(err.Error(), killErr.Error()) {
		t.Fatalf("current Stop lost cleanup failure: %v", err)
	}
	if err := <-resumed; err == nil || !strings.Contains(err.Error(), killErr.Error()) {
		t.Fatalf("current Resume did not report cleanup failure: %v", err)
	}
	// Fresh connections deliberately avoid stale-epoch rejection masking a
	// forgotten live child. Every later operation must still see that ownership.
	for _, method := range []string{appwire.MethodEvenerThreadForceStop, "thread/shutdown", "thread/resume"} {
		var out appwire.EmptyResponse
		err := newClient().Request(t.Context(), method, map[string]string{"ref": "local:" + sessionID}, &out)
		if err == nil {
			t.Errorf("%s falsely succeeded while the owned child remained alive", method)
		} else if !strings.Contains(err.Error(), killErr.Error()) {
			t.Errorf("%s lost retained cleanup error: %v", method, err)
		}
		if launches.Load() != 1 {
			t.Errorf("%s launched a replacement before child reaping: launches=%d", method, launches.Load())
		}
	}
	releaseChild()
	<-reaped
	<-active.CleanupDone()
	var out appwire.EmptyResponse
	err = newClient().Request(t.Context(), "thread/resume", map[string]string{"ref": "local:" + sessionID}, &out)
	if err == nil || !strings.Contains(err.Error(), retryErr.Error()) || launches.Load() != 2 {
		t.Fatalf("actual reaping did not release Resume ownership: launches=%d err=%v", launches.Load(), err)
	}
}

// TestResumeStopRetriesFailedChildKillUntilReaped is the Medium RoboRev
// reported against reapResumeChild: when the child kill failed, the launcher
// returned immediately and the kill handle was lost with it, while the
// active Resume stayed retained on its unconfirmed cleanup — refusing every
// later resume and the force stop that canceled the launch — with a
// potentially live child and no recovery path. The stop that drains the
// Resume is that recovery path: it must retry the retained handle until the
// child is confirmed reaped, releasing the aliases for the next Resume.
func TestResumeStopRetriesFailedChildKillUntilReaped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: 0})
		if err != nil {
			t.Fatal(err)
		}
		waiting, retryKilled, abandon := make(chan struct{}), make(chan struct{}), make(chan struct{})
		defer close(abandon)
		var kills atomic.Int32
		original := startResumeChild
		defer func() { startResumeChild = original }()
		startResumeChild = func(*exec.Cmd) (resumeChild, error) {
			return resumeChild{pid: 4242, kill: func() error {
				if kills.Add(1) == 1 {
					return errors.New("FIXTURE_KILL_DENIED")
				}
				close(retryKilled)
				return nil
			}, wait: func() error {
				close(waiting)
				select {
				case <-retryKilled:
					return nil
				case <-abandon:
					return nil
				}
			}}, nil
		}
		launched := make(chan error, 1)
		go func() {
			_, err := resumeDaemon(active.Context(), "fixture-evener", runDir, hubcore.ResumeRequest{SessionID: sessionID, ActiveResume: active, CompletionOwned: true}, time.Hour, io.Discard)
			launched <- err
		}()
		<-waiting
		stop := locks.BeginActiveResumeStop(sessionID)
		if stop == nil {
			t.Fatal("force stop found no active Resume to drain")
		}
		var launchErr error
		select {
		case launchErr = <-launched:
		case <-time.After(time.Second):
			t.Fatal("canceled launch did not return")
		}
		if launchErr == nil || !strings.Contains(launchErr.Error(), "resume child cleanup is unconfirmed") {
			t.Fatalf("canceled launch error = %v, want the unconfirmed cleanup failure", launchErr)
		}
		// The handler completes exactly as resumeThread's deferred Complete does.
		active.Complete(nil)
		// The stop that canceled the launch is the only remaining recovery path
		// for the live child: it must retry the failed kill and wait for the
		// confirmed reap instead of refusing on the retained error alone.
		if err := stop.Wait(t.Context()); err != nil {
			t.Fatalf("stop could not recover the failed child kill: %v", err)
		}
		stop.Release()
		if kills.Load() != 2 {
			t.Fatalf("child kill attempts = %d, want the failed attempt plus the stop's retry", kills.Load())
		}
		if locks.HasActiveResume([]string{sessionID}) {
			t.Fatal("confirmed reap left the active Resume retained")
		}
		epochs := map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch}
		if _, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, epochs); err != nil {
			t.Fatalf("later resume still refused after the child was confirmed reaped: %v", err)
		}
	})
}

// TestRegisterResumeCleanupFailureIsUnavailable pins the RegisterResume
// boundary: a registration refused because another active Resume's child
// cleanup is unconfirmed is a retryable "cleanup remains unconfirmed" state and
// must reach the client as Unavailable, not as a raw resumeCleanupError that
// maps to InternalError. This is the handler-not-yet-done window that
// ResumeCleanupError's own handlerDone gate cannot see.
func TestRegisterResumeCleanupFailureIsUnavailable(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	sessionID := hubtest.SessionID(t)
	epochs := map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch}
	active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, epochs)
	if err != nil {
		t.Fatal(err)
	}
	// LaunchFinished records the cleanup failure before the handler's deferred
	// Complete sets handlerDone, exactly the window the pre-check bypasses.
	active.LaunchFinished(true, &resumeCleanupError{cause: errors.New("fixture child cleanup denied")})

	_, err = locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, epochs)
	if err == nil {
		t.Fatal("RegisterResume admitted a registration with unconfirmed cleanup")
	}
	if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
		t.Fatalf("RegisterResume cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
	}
}

// TestShutdownRefusesCleanupBeforeHandlerDone pins the ordinary-shutdown half of
// the same window RegisterResume already closes: a failed launch records its
// unconfirmed child cleanup through LaunchFinished before the handler's deferred
// Complete sets handlerDone. The handlerDone-gated ResumeCleanupError cannot see
// that window, so shutdown would fall through to the source shutdown path while
// child cleanup was still unconfirmed.
func TestShutdownRefusesCleanupBeforeHandlerDone(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	sessionID := hubtest.SessionID(t)
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(sessionID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("FIXTURE_CHILD_CLEANUP_UNCONFIRMED")
	// handlerDone stays false: the handler has not reached its deferred Complete.
	active.LaunchFinished(true, cleanupErr)
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
	err = shutdownThreadTolerateExited(t.Context(), cfg, appsource.NewRegistry(), appwire.ThreadShutdownParams{Ref: "local:" + sessionID})
	if err == nil {
		t.Fatal("ordinary shutdown proceeded while a failed launch's child cleanup was unconfirmed")
	}
	if !strings.Contains(err.Error(), cleanupErr.Error()) {
		t.Fatalf("shutdown lost the retained cleanup error: %v", err)
	}
	if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
		t.Fatalf("shutdown cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
	}
}

// shutdownScriptedSource is a scriptedAppSource whose shutdown action
// succeeds, so a shutdown that proceeds to the source reports success.
type shutdownScriptedSource struct {
	*scriptedAppSource
	shutdowns int
}

func (s *shutdownScriptedSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	s.shutdowns++
	return nil
}

// TestShutdownUncertainDiscoveryAttemptsSource pins the unreachable-fallback
// RoboRev finding: when strict rendezvous discovery fails for a session already
// confirmed exited, confirmedStoppedWithoutClaim falls through to the tolerant
// source attempt — but the fall-through used to die at the session-action
// ownership gate, which refuses every action while the session stays
// ResumeRequired, so thread/shutdown failed with the spurious
// explicit-resume refusal instead of attempting the source operation.
// Shutdown never resurrects the session and its goal (a stopped daemon) is
// what durable authority already asserts, so the uncertain-discovery
// fall-through must reach the source shutdown under deletion-fence ownership.
func TestShutdownUncertainDiscoveryAttemptsSource(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	sessionID := hubtest.SessionID(t)
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(sessionID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	runDir := t.TempDir()
	// A pid-named but undecodable rendezvous file fails the strict read.
	if err := os.WriteFile(filepath.Join(runDir, "9999.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rendezvous.ListStrict(runDir); err == nil {
		t.Fatal("fixture discovery did not fail")
	}
	source := &shutdownScriptedSource{scriptedAppSource: &scriptedAppSource{
		id: "local",
		thread: appwire.Thread{
			ID:     sessionID,
			Source: "local",
			Evener: appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Shutdown: true}},
		},
	}}
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks}
	if err := shutdownThreadTolerateExited(t.Context(), cfg, sources, appwire.ThreadShutdownParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("uncertain-discovery shutdown refused before the source attempt: %v", err)
	}
	if source.shutdowns != 1 {
		t.Fatalf("uncertain-discovery shutdown invoked the source %d times, want 1", source.shutdowns)
	}
}

// TestShutdownRechecksCleanupUnderOwnership pins the race RoboRev found: the
// pre-ownership ResumeCleanupErrorStrict check has already passed when a
// Resume retains failed child cleanup while shutdown is still waiting for
// alias ownership. The under-ownership recheck must refuse the shutdown
// rather than let the source action report the session stopped.
func TestShutdownRechecksCleanupUnderOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		sessionID := hubtest.SessionID(t)
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		// The in-flight launch holds the alias reservation across its launch,
		// so shutdown blocks waiting for ownership after its pre-check passed.
		locks.For(sessionID).Lock()
		cleanupErr := errors.New("FIXTURE_CHILD_CLEANUP_UNCONFIRMED")
		waiting := make(chan struct{})
		go func() {
			<-waiting
			active.LaunchFinished(true, cleanupErr)
			locks.For(sessionID).Unlock()
		}()
		source := &shutdownScriptedSource{scriptedAppSource: &scriptedAppSource{
			id: "local",
			thread: appwire.Thread{
				ID:     sessionID,
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Shutdown: true}},
			},
		}}
		sources := appsource.NewRegistry()
		sources.Add(source)
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
		stopped := make(chan error, 1)
		go func() {
			stopped <- shutdownThreadTolerateExited(t.Context(), cfg, sources, appwire.ThreadShutdownParams{Ref: "local:" + sessionID})
		}()
		synctest.Wait() // shutdown is blocked acquiring the alias reservation
		close(waiting)
		err = <-stopped
		if err == nil {
			t.Fatal("shutdown reported success while a failed launch's child cleanup was unconfirmed")
		}
		if !strings.Contains(err.Error(), cleanupErr.Error()) {
			t.Fatalf("shutdown lost the retained cleanup error: %v", err)
		}
		if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
			t.Fatalf("shutdown cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
		}
		if source.shutdowns != 0 {
			t.Fatal("shutdown invoked the source action with child cleanup unconfirmed")
		}
	})
}

// TestShutdownUncertainDiscoveryInvalidatesWaitingResumeRegistration pins the
// admission race RoboRev found between the uncertain-discovery decision and
// the tolerant shutdown attempt: checkConfirmedStoppedWithoutClaim releases the
// alias reservations when it reports the discovery uncertainty, and the
// tolerant attempt reacquires only the request's alias afterwards, so a Resume
// registration waiting for those reservations lands inside the gap and is
// already registered while the source attempt runs — the session then either
// launches after shutdown has reported success, or the daemon the Resume just
// started gets shut down. Like the confirmed-stopped no-op, the uncertain
// decision must publish itself while it still holds the reservations, so the
// waiting registration re-admits on a snapshot taken after the decision instead
// of launching on one taken before it.
func TestShutdownUncertainDiscoveryInvalidatesWaitingResumeRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		sessionID := hubtest.SessionID(t)
		finish := locks.BeginForceStop([]string{sessionID})
		if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
			t.Fatal(err)
		}
		if err := locks.ConfirmForceStop(sessionID); err != nil {
			t.Fatal(err)
		}
		finish.Finish(true)
		runDir := t.TempDir()
		// A pid-named but undecodable rendezvous file fails the strict read, so
		// the confirmed-exited session resolves through the uncertain path.
		if err := os.WriteFile(filepath.Join(runDir, "9999.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := rendezvous.ListStrict(runDir); err == nil {
			t.Fatal("fixture discovery did not fail")
		}
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Block the check on its under-reservation deletion check, so it holds
		// the alias reservation while the registration waits for it.
		entered, release := make(chan struct{}), make(chan struct{})
		blocked := false
		original := deletionTargetState
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			if !blocked {
				blocked = true
				close(entered)
				<-release
			}
			return "", false
		}
		defer func() { deletionTargetState = original }()
		source := &shutdownScriptedSource{scriptedAppSource: &scriptedAppSource{
			id: "local",
			thread: appwire.Thread{
				ID:     sessionID,
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Shutdown: true}},
			},
		}}
		sources := appsource.NewRegistry()
		sources.Add(source)
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store}
		stopped := make(chan error, 1)
		go func() {
			stopped <- shutdownThreadTolerateExited(t.Context(), cfg, sources, appwire.ThreadShutdownParams{Ref: "local:" + sessionID})
		}()
		<-entered // the uncertain check holds the alias reservation
		epochs := map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch}
		registered := make(chan error, 1)
		go func() {
			_, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, epochs)
			registered <- err
		}()
		synctest.Wait() // the registration is now waiting for the held alias
		close(release)
		if err := <-stopped; err != nil {
			t.Fatalf("uncertain-discovery shutdown: %v", err)
		}
		if err := <-registered; !errors.Is(err, hubcore.ErrResumeInvalidated) {
			t.Fatalf("registration waiting across the uncertain decision = %v, want ErrResumeInvalidated", err)
		}
		// The decision is published: a fresh admission snapshot registers.
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		active.Complete(nil)
	})
}

// TestShutdownUncertainOwnershipRefusesRegisteredResume pins the other half of
// the same gap: the tolerant attempt reacquires only the request's alias after
// the uncertain decision released the whole reservation group, so a Resume
// admitted while that window was open can already be registered when the
// reacquire completes. The under-ownership recheck must refuse the source
// attempt while such a Resume is active — reporting the already-exited session
// stopped would let the launch finish after shutdown's success, and running
// the source action would shut down the daemon the Resume just started. The
// refusal must be a session-recovery admission error rather than a
// session-unavailable error, which the tolerant fallback would mask as a no-op
// success.
func TestShutdownUncertainOwnershipRefusesRegisteredResume(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	sessionID := hubtest.SessionID(t)
	active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	defer active.Complete(nil)
	called := false
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
	_, err = withShutdownDiscoveryUncertainOwnership(t.Context(), cfg, "local:"+sessionID, "", func() (struct{}, error) {
		called = true
		return struct{}{}, nil
	})
	if err == nil {
		t.Fatal("uncertain-discovery shutdown ran the tolerant source attempt while an explicit Resume was registered")
	}
	if called {
		t.Fatal("uncertain-discovery shutdown invoked the source action while an explicit Resume was registered")
	}
	if !isSessionRecoveryAdmissionError(err) {
		t.Fatalf("refusal = %v, want a session-recovery admission error", err)
	}
	if isSessionUnavailableError(err) {
		t.Fatal("refusal is classified session-unavailable, so the tolerant fallback would mask it as a no-op success")
	}
}

// The Low RoboRev reported against resumeDaemon's CompletionOwned timeout=0:
// a completion-owned resume whose client never cancels held the session's
// ownership aliases indefinitely while the child never published rendezvous.
// The hub now bounds the wait itself (completionOwnedResumeTimeout, mirroring
// the client's RESUME_REQUEST_TIMEOUT_MS), so the lock lifetime does not
// depend on caller behavior.
func TestCompletionOwnedResumeRendezvousWaitIsHubBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		exit := make(chan error, 1)
		defer close(exit)
		waiting := make(chan struct{})
		killed := make(chan struct{}, 1)
		original := startResumeChild
		defer func() { startResumeChild = original }()
		startResumeChild = func(cmd *exec.Cmd) (resumeChild, error) {
			return resumeChild{pid: 4242, kill: func() error {
				killed <- struct{}{}
				return nil
			}, wait: func() error {
				close(waiting)
				return <-exit
			}}, nil
		}
		originalBudget := completionOwnedResumeTimeout
		defer func() { completionOwnedResumeTimeout = originalBudget }()
		completionOwnedResumeTimeout = time.Second
		type result struct {
			entry rendezvous.Entry
			err   error
		}
		completed := make(chan result, 1)
		go func() {
			entry, err := resumeDaemon(t.Context(), "fixture-evener", runDir, hubcore.ResumeRequest{
				SessionID: sessionID, CompletionOwned: true,
			}, DefaultConfig().SpawnTimeout, io.Discard)
			completed <- result{entry, err}
		}()
		<-waiting
		// Inside the budget the launcher keeps waiting even though the caller
		// has no deadline of its own.
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		select {
		case got := <-completed:
			t.Fatalf("completion-owned resume returned inside its hub budget: %+v", got)
		default:
		}
		// Past the budget the hub's own cap settles the wait and retires the
		// child, exactly like the ordinary spawn timeout does.
		time.Sleep(time.Second)
		synctest.Wait()
		<-killed
		exit <- nil
		if got := <-completed; !errors.Is(got.err, errRendezvousTimeout) {
			t.Fatalf("completion-owned resume did not settle on the hub cap: %+v", got)
		}
	})
}
