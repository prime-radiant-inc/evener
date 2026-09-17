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
		finish(true)
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
			finish(true)
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
			finish(true)
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
			t.Chdir(root)
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
			finish(true)
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

func TestLongRunningResumeFailedCleanupRetainsOwnership(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
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
	finish(true)
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
