package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type forceStopControllerFunc func(daemonprocess.Target) (daemonprocess.Process, error)

func (f forceStopControllerFunc) Open(target daemonprocess.Target) (daemonprocess.Process, error) {
	return f(target)
}

type forceStopProcess struct {
	events           *[]string
	killErr, waitErr error
	onWait           func()
}

func (p *forceStopProcess) Kill() error { *p.events = append(*p.events, "kill"); return p.killErr }
func (p *forceStopProcess) Wait(context.Context) error {
	*p.events = append(*p.events, "wait")
	if p.onWait != nil {
		p.onWait()
	}
	return p.waitErr
}
func (p *forceStopProcess) Close() error { *p.events = append(*p.events, "close"); return nil }

func TestHubForceStopConfirmsExitAndPreservesSavedData(t *testing.T) {
	for _, protocol := range []string{"evener-appwire-v3", appwire.ProtocolVersion} {
		t.Run(protocol, func(t *testing.T) {
			stateDir, runDir := t.TempDir(), t.TempDir()
			sessionID := buildRPCParentSession(t, stateDir)
			saved, err := os.ReadDir(filepath.Join(stateDir, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			contents := map[string][]byte{}
			for _, file := range saved {
				if !file.IsDir() {
					contents[file.Name()], err = os.ReadFile(filepath.Join(stateDir, "sessions", file.Name()))
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			entry := rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID, StateDir: stateDir, Protocol: protocol, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
			writeRendezvous(t, runDir, entry)
			var events []string
			controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				if target.PID != entry.PID || target.SessionID != sessionID || target.StateDir != stateDir || !target.StartedAt.Equal(entry.StartedAt) {
					t.Errorf("target=%+v", target)
				}
				return &forceStopProcess{events: &events}, nil
			})
			hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: controller})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			var response appwire.EmptyResponse
			if err := client.Request(t.Context(), "evener/thread/forceStop", map[string]string{"ref": "local:" + sessionID}, &response); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, []string{"open", "kill", "wait", "close"}) {
				t.Fatalf("events=%v", events)
			}
			for name, want := range contents {
				got, err := os.ReadFile(filepath.Join(stateDir, "sessions", name))
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("saved file %s changed: %v", name, err)
				}
			}
			if entries, err := rendezvous.ListStrict(runDir); err != nil || len(entries) != 1 {
				t.Fatalf("stop removed ownership data instead of confirming exit: %v %v", entries, err)
			}
		})
	}
}

func TestForceStopRevalidatesExitedProcessUnderOwnership(t *testing.T) {
	for _, current := range []string{"exited", "live", "ambiguous"} {
		t.Run(current, func(t *testing.T) {
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 4242, SessionID: "current", ThreadID: "current", WorkspaceRef: "local:stable", StateDir: t.TempDir(), StartedAt: time.Now()}
			writeRendezvous(t, runDir, entry)
			locks := hubcore.NewResumeLocks()
			var events []string
			opens := 0
			controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				opens++
				if opens == 1 {
					return nil, daemonprocess.ErrExited
				}
				for _, alias := range []string{"stable", "current"} {
					if locks.For(alias).TryLock() {
						locks.For(alias).Unlock()
						t.Errorf("exit revalidation did not hold %s ownership", alias)
					}
				}
				switch current {
				case "live":
					return &forceStopProcess{events: &events}, nil
				case "ambiguous":
					return nil, errors.New("process identity is unresolved")
				default:
					return nil, daemonprocess.ErrExited
				}
			})
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: controller}
			err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:stable"}, nil)
			if opens != 2 || (err == nil) != (current == "exited") {
				t.Fatalf("exit result ignored current process: opens=%d err=%v", opens, err)
			}
			var want []string
			if current == "live" {
				want = []string{"close"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("unexpected generation was signaled or leaked: %v", events)
			}
			if state := locks.RecoveryState("stable"); state.Stopping != 0 || state.ResumeRequired != (current == "exited") {
				t.Fatalf("false recovery success: %+v", state)
			}
		})
	}
}

func TestForceStopFailuresDoNotPretendExit(t *testing.T) {
	for _, stage := range []string{"identity", "signal", "exit", "alreadyExited"} {
		t.Run(stage, func(t *testing.T) {
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 4242, SessionID: webTestSessionID, ThreadID: webTestSessionID, StateDir: t.TempDir(), StartedAt: time.Now()}
			writeRendezvous(t, runDir, entry)
			var events []string
			controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				if stage == "identity" {
					return nil, errors.New("identity changed")
				}
				if stage == "alreadyExited" {
					return nil, daemonprocess.ErrExited
				}
				p := &forceStopProcess{events: &events}
				if stage == "signal" {
					p.killErr = errors.New("signal denied")
				}
				if stage == "exit" {
					p.waitErr = context.DeadlineExceeded
				}
				return p, nil
			})
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: controller}
			err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + webTestSessionID}, nil)
			state := cfg.ResumeLocks.RecoveryState(webTestSessionID)
			if state.Stopping != 0 || state.ResumeRequired != (stage == "signal" || stage == "exit" || stage == "alreadyExited") {
				t.Fatalf("incorrect recovery requirement after %s: %+v", stage, state)
			}
			if (err == nil) != (stage == "alreadyExited") {
				t.Fatalf("error=%v", err)
			}
			want := map[string][]string{"identity": {"open"}, "signal": {"open", "kill", "close"}, "exit": {"open", "kill", "wait", "close"}, "alreadyExited": {"open", "open"}}[stage]
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events=%v want=%v", events, want)
			}
		})
	}
}

func TestForceStopRejectsAmbiguousAndForeignTargets(t *testing.T) {
	for _, tc := range []struct {
		name, ref string
		entries   []rendezvous.Entry
	}{
		{"foreign", "remote:owner", nil}, {"invalid", "owner", nil}, {"missing", "local:owner", nil},
		{"multiple", "local:owner", []rendezvous.Entry{{PID: 4242, SessionID: "owner"}, {PID: 4243, SessionID: "owner"}}},
		{"overlapping aliases", "local:stable", []rendezvous.Entry{{PID: 4242, SessionID: "current", ThreadID: "current", WorkspaceRef: "local:stable"}, {PID: 4243, SessionID: "current", ThreadID: "current"}}},
		{"foreign claim", "local:owner", []rendezvous.Entry{{PID: 4242, SessionID: "owner", SourceID: "remote"}}},
		{"descendant", "local:child", []rendezvous.Entry{{PID: 4242, SessionID: "owner", ThreadID: "owner"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			for _, entry := range tc.entries {
				writeRendezvous(t, runDir, entry)
			}
			controller := forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				if tc.name != "multiple" && tc.name != "overlapping aliases" {
					t.Error("unsafe target reached process controller")
				}
				return nil, errors.New("unexpected")
			})
			if err := forceStopThread(t.Context(), hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: controller}, appwire.ThreadForceStopParams{Ref: tc.ref}, nil); err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
}

type waitingForceStopProcess struct {
	entered, release chan struct{}
	confirmed        atomic.Bool
}

func (p *waitingForceStopProcess) Kill() error { return nil }
func (p *waitingForceStopProcess) Wait(ctx context.Context) error {
	close(p.entered)
	select {
	case <-p.release:
		p.confirmed.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *waitingForceStopProcess) Close() error { return nil }

func TestForceStopRevalidationComparesTimestampInstants(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4242, SessionID: "current", WorkspaceRef: "local:stable", StartedAt: time.Date(2026, 9, 7, 12, 0, 0, 0, time.FixedZone("offset", 1200))}
	writeRendezvous(t, runDir, entry)
	previous, err := forceStopEntry(runDir, "stable", nil, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := forceStopOwnershipUnchanged(runDir, "stable", previous, nil, ""); err != nil {
		t.Fatalf("identical persisted timestamp rejected: %v", err)
	}
	entry.StartedAt = entry.StartedAt.Add(time.Second)
	writeRendezvous(t, runDir, entry)
	if err := forceStopOwnershipUnchanged(runDir, "stable", previous, nil, ""); err == nil {
		t.Fatal("changed start instant accepted")
	}
}

func TestForceStopRejectsDiscoveryChangeDuringLockedRevalidation(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4242, SessionID: "current", WorkspaceRef: "local:stable"}
	writeRendezvous(t, runDir, entry)
	previous, err := forceStopEntry(runDir, "stable", nil, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	entry.InstanceID = "replacement"
	writeRendezvous(t, runDir, entry)
	if err := forceStopOwnershipUnchanged(runDir, "stable", previous, nil, ""); err == nil {
		t.Fatal("changed discovery accepted")
	}
}

// A failed ancillary discovery refresh cannot undo the verified exit.
func TestForceStopPreservesSuccessAfterRosterRefreshFailure(t *testing.T) {
	// Keep this top-level test sequential: captureHubLog uses the shared logger.
	// Compare the entire output so unrelated or repeated warnings also fail.
	logged := captureHubLog(t)
	flags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() { log.SetFlags(flags) })
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: "owner"})
	roster := hubcore.NewRoster(runDir, failedRPCProber{})
	poked := false
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), Roster: roster,
		PokeAttention: func() { poked = true },
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: &events, onWait: func() {
				if err := os.WriteFile(filepath.Join(runDir, "4243.json"), []byte("{"), 0600); err != nil {
					t.Error(err)
				}
			}}, nil
		}),
	}
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:owner"}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"kill", "wait", "close"}) {
		t.Fatalf("stop events=%v", events)
	}
	if !poked {
		t.Fatal("successful stop did not invalidate attention")
	}
	if err := roster.RefreshAndWait(t.Context()); err == nil {
		t.Fatal("fixture did not fail discovery")
	}
	const expected = "daemon stopped; roster refresh remains incomplete: decode rendezvous 4243.json: unexpected end of JSON input\n"
	if got := logged.String(); got != expected {
		t.Fatalf("expected exactly one incomplete-roster warning, got %q", got)
	}
}

// TestConfirmedStoppedShortcutRefreshesAfterStop is the Low regression: the
// confirmed-stopped fast path returns success without running the same
// post-stop refresh/invalidation every other successful stop path runs, so the
// roster, inputs, and attention are left stale.
func TestConfirmedStoppedShortcutRefreshesAfterStop(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{webTestSessionID})
	if err := locks.PersistForceStop([]string{webTestSessionID}, webTestSessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(webTestSessionID); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	inputs := &hubcore.InputsVersion{}
	poked := false
	cfg := hubcore.WebConfig{
		RunDir:        t.TempDir(),
		ResumeLocks:   locks,
		Inputs:        inputs,
		PokeAttention: func() { poked = true },
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			t.Error("confirmed-stopped shortcut attempted process control")
			return nil, errors.New("unexpected process control")
		}),
	}
	before := inputs.Load()
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + webTestSessionID}, nil); err != nil {
		t.Fatal(err)
	}
	if !poked {
		t.Error("confirmed-stopped shortcut did not invalidate attention")
	}
	if got := inputs.Load(); got <= before {
		t.Errorf("confirmed-stopped shortcut did not bump inputs: %d <= %d", got, before)
	}
}

type forceStopProberFunc func(rendezvous.Entry) hubcore.ProbeResult

func (f forceStopProberFunc) Probe(entry rendezvous.Entry) hubcore.ProbeResult { return f(entry) }

type fixtureExitProcess struct {
	input   io.Closer
	command *exec.Cmd
}

func (p *fixtureExitProcess) Kill() error                { return p.input.Close() }
func (p *fixtureExitProcess) Wait(context.Context) error { return p.command.Wait() }
func (p *fixtureExitProcess) Close() error               { return nil }

func TestHubForceStopUnconfirmedRootReadAndExplicitResume(t *testing.T) {
	var sessionID string
	shutdownCalls := 0
	cfg, sid, resumes := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Status: appwire.ThreadStatus{Type: "idle"}, Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true, Shutdown: true}}}}, nil
		})
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadShutdown, func(context.Context, appwire.ThreadShutdownParams) (appwire.EmptyResponse, error) {
			shutdownCalls++
			return appwire.EmptyResponse{}, nil
		})
	})
	sessionID = sid
	// cat owns only this test's pipe and exits when the fake controller closes
	// it. Real process liveness drives the roster's unconfirmed/crash states.
	command := exec.CommandContext(t.Context(), "cat")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if command.ProcessState == nil {
			_ = command.Wait()
		}
	})
	pe, _ := cfg.Past.Find(sessionID)
	entry := rendezvous.Entry{PID: command.Process.Pid, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID, StateDir: pe.StateDir, Protocol: appwire.ProtocolVersion, StartedAt: time.Now()}
	writeRendezvous(t, cfg.RunDir, entry)
	cfg.Roster = hubcore.NewRoster(cfg.RunDir, forceStopProberFunc(func(e rendezvous.Entry) hubcore.ProbeResult {
		if e.PID == entry.PID {
			return hubcore.ProbeResult{}
		}
		return hubcore.ProbeResult{OK: true, SessionID: sessionID, Status: "idle"}
	}))
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	opens := 0
	cfg.DaemonProcesses = forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
		opens++
		if target.PID == 106 {
			return &forceStopProcess{events: new([]string)}, nil
		}
		if target.PID == command.Process.Pid && command.ProcessState != nil {
			return nil, daemonprocess.ErrExited
		}
		if target.PID != command.Process.Pid {
			t.Fatalf("unexpected target: %+v", target)
		}
		return &fixtureExitProcess{input: input, command: command}, nil
	})
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID
	if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true}); err == nil {
		t.Fatal("unconfirmed live root unexpectedly hydrated")
	}
	var stopped appwire.EmptyResponse
	if err := client.Request(t.Context(), "evener/thread/forceStop", appwire.ThreadForceStopParams{Ref: ref}, &stopped); err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Status.Type != "notLoaded" || len(read.Thread.Turns) != 2 || read.Thread.Evener.Capabilities.Send || !read.Thread.Evener.ResumeRequired {
		t.Fatalf("stopped read: %+v", read.Thread)
	}
	if *resumes != 0 {
		t.Fatal("stop/read automatically resumed")
	}
	entries, err := rendezvous.ListStrict(cfg.RunDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("retained crash marker=%v err=%v", entries, err)
	}
	client = dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if *resumes != 1 {
		t.Fatalf("explicit resumes=%d", *resumes)
	}
	if err := client.ThreadShutdown(t.Context(), appwire.ThreadShutdownParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if shutdownCalls != 1 || opens != 1 {
		t.Fatalf("normal shutdown route=%d process opens=%d", shutdownCalls, opens)
	}
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil); err != nil {
		t.Fatalf("stop after explicit resume: %v", err)
	}
	if *resumes != 1 {
		t.Fatalf("second stop resumed: %d", *resumes)
	}
}

func TestForceStopSerializesResumeAndDeletionEntryPoints(t *testing.T) {
	for _, operation := range []string{"resume", "delete"} {
		for _, savedAlias := range []string{"stable", "current"} {
			t.Run(operation+"/"+savedAlias, func(t *testing.T) {
				stateDir := filepath.Join(t.TempDir(), "force-stop-0000000000")
				sessionID := buildRPCParentSession(t, stateDir)
				past := hubcore.NewPastIndex(stateDir)
				if _, err := past.Rebuild(); err != nil {
					t.Fatal(err)
				}
				currentID, stableID := sessionID, "02wMz5Txv1C3Hut0M8GCeC"
				if savedAlias == "stable" {
					currentID, stableID = stableID, currentID
				}
				runDir := t.TempDir()
				writeRendezvous(t, runDir, rendezvous.Entry{PID: exitedPID(t), SessionID: currentID, ThreadID: currentID, WorkspaceRef: "local:" + stableID, StateDir: stateDir})
				process := &waitingForceStopProcess{entered: make(chan struct{}), release: make(chan struct{})}
				spawned := make(chan struct{}, 1)
				cfg := hubcore.WebConfig{RunDir: runDir, Past: past, ResumeLocks: hubcore.NewResumeLocks(),
					DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return process, nil }),
					Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
						if !process.confirmed.Load() {
							t.Error("resume spawned before exit confirmation")
						}
						spawned <- struct{}{}
						return rendezvous.Entry{}, errors.New("fixture spawn boundary")
					}},
				}
				web := NewWebServer(cfg)
				stopped := make(chan error, 1)
				go func() {
					stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stableID}, nil)
				}()
				<-process.entered
				for _, alias := range []string{currentID, stableID} {
					lock := cfg.ResumeLocks.For(alias)
					if lock.TryLock() {
						lock.Unlock()
						t.Errorf("%s lock released before exit", alias)
					}
				}
				started, finished := make(chan struct{}), make(chan struct{})
				resumeResult := make(chan error, 1)
				go func() {
					defer close(finished)
					close(started)
					if operation == "resume" {
						_, err := hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: "local:" + sessionID})
						resumeResult <- err
					} else {
						response, err := web.sessionDelete(t.Context(), appwire.SessionDeleteParams{Ref: "local:" + sessionID})
						if err != nil || len(response.Deleted) != 1 {
							t.Errorf("delete response=%+v err=%v", response, err)
						}
						if !process.confirmed.Load() {
							t.Error("deletion completed before exit confirmation")
						}
					}
				}()
				<-started
				if _, err := os.Stat(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-spawned:
					t.Error("spawned while force stop holds ownership")
				default:
				}
				close(process.release)
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
				<-finished
				if operation == "resume" {
					// The overlapping resume can observe active recovery between
					// ownership unlock and completion of the recovery fence.
					wantOverlap := appwire.ErrorActionUnavailable
					select {
					case <-spawned:
						wantOverlap = appwire.ErrorHubLaunch
					default:
					}
					overlapErr := <-resumeResult
					if overlapErr == nil || evenerErrorInfoFromData(appserver.WireError(overlapErr).Data) != string(wantOverlap) {
						t.Fatalf("overlapping resume error=%v want=%s", overlapErr, wantOverlap)
					}
					_, err := hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: "local:" + sessionID})
					if err == nil || evenerErrorInfoFromData(appserver.WireError(err).Data) != string(appwire.ErrorHubLaunch) {
						t.Fatalf("fresh resume did not return fixture spawn error: %v", err)
					}
					select {
					case <-spawned:
					default:
						t.Error("fresh resume never reached spawner after force stop returned")
					}
				} else if _, err := os.Stat(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl")); !os.IsNotExist(err) {
					t.Fatalf("delete did not remove saved data: %v", err)
				}
			})
		}
	}
}

func TestHubForceStopInterruptsStalledRPCOnSameConnection(t *testing.T) {
	for _, method := range []string{appwire.MethodThreadShutdown, appwire.MethodThreadModelSet} {
		t.Run(method, func(t *testing.T) {
			var sessionID string
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			cfg, sid, resumes := parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Shutdown: true, ChangeModel: true}}}}, nil
				})
				daemon.Router().Handle(method, func(ctx context.Context, _ json.RawMessage) (any, error) {
					close(entered)
					select {
					case <-ctx.Done():
					case <-release:
					}
					return appwire.EmptyResponse{}, ctx.Err()
				})
			})
			sessionID = sid
			cfg.ResumeLocks = hubcore.NewResumeLocks()
			var events []string
			cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				return &forceStopProcess{events: &events}, nil
			})
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			ref := "local:" + sessionID
			if _, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref}); err != nil {
				t.Fatal(err)
			}
			shutdown := make(chan error, 1)
			go func() {
				shutdown <- client.Request(t.Context(), method, appwire.ThreadModelSetParams{Ref: ref, ModelProvider: "test", Model: "test"}, nil)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("shutdown never reached daemon")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := client.Request(ctx, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil); err != nil {
				t.Fatalf("force stop behind stalled shutdown: %v", err)
			}
			select {
			case err := <-shutdown:
				if err == nil {
					t.Fatal("interrupted RPC returned success")
				}
			case <-ctx.Done():
				t.Fatal("stalled shutdown did not return")
			}
			if !reflect.DeepEqual(events, []string{"kill", "wait", "close"}) {
				t.Fatalf("process events=%v", events)
			}
			if *resumes != 1 {
				t.Fatalf("recovery automatically resumed session: %d", *resumes)
			}
		})
	}
}

func TestForceStopSkipsVerifiedExitedClaims(t *testing.T) {
	for _, aliasOnly := range []bool{false, true} {
		t.Run(strconv.FormatBool(aliasOnly), func(t *testing.T) {
			runDir := t.TempDir()
			old := rendezvous.Entry{PID: 4242, SessionID: "stable", ThreadID: "current", StateDir: t.TempDir(), StartedAt: time.Now()}
			writeRendezvous(t, runDir, old)
			var events []string
			dead := map[int]bool{}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
				if dead[target.PID] {
					return nil, daemonprocess.ErrExited
				}
				return &forceStopProcess{events: &events, onWait: func() { dead[target.PID] = true }}, nil
			})}
			params := appwire.ThreadForceStopParams{Ref: "local:stable"}
			if err := forceStopThread(t.Context(), cfg, params, nil); err != nil {
				t.Fatal(err)
			}
			resumed := old
			resumed.PID++
			if aliasOnly {
				resumed.SessionID = "resumed"
				params.Ref = "local:resumed"
			}
			writeRendezvous(t, runDir, resumed)
			if err := forceStopThread(t.Context(), cfg, params, nil); err != nil {
				t.Fatalf("second force stop: %v", err)
			}
			if err := forceStopThread(t.Context(), cfg, params, nil); err != nil {
				t.Fatalf("retry with all owners exited: %v", err)
			}
			entries, err := rendezvous.ListStrict(runDir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("crash markers not preserved: %v %v", entries, err)
			}
		})
	}
}

func TestHubForceStopInterruptsStalledSpawnedResumeRead(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	cfg, sessionID, resumes := parityResumeFixture(t, func(daemon *appserver.Server) {
		appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			close(entered)
			select {
			case <-ctx.Done():
			case <-release:
			}
			return appwire.ThreadReadResponse{}, ctx.Err()
		})
	})
	spawner := cfg.Spawner.(*fakeRPCSpawner)
	spawn := spawner.resume
	spawner.resume = func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
		entry, err := spawn(ctx, req)
		entry.StartedAt = time.Now()
		entry.StateDir = req.StateDir
		writeRendezvous(t, cfg.RunDir, entry)
		return entry, err
	}
	cfg.Roster = hubcore.NewRoster(cfg.RunDir, forceStopProberFunc(func(rendezvous.Entry) hubcore.ProbeResult { return hubcore.ProbeResult{} }))
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	var events []string
	cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &events}, nil
	})
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	ref := "local:" + sessionID
	resume := make(chan error, 1)
	go func() {
		_, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref})
		resume <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("spawned read never reached daemon")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := client.Request(ctx, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil); err != nil {
		t.Fatalf("force stop behind spawned read: %v", err)
	}
	select {
	case err := <-resume:
		if err == nil {
			t.Fatal("canceled resume succeeded")
		}
	case <-ctx.Done():
		t.Fatal("resume retained ownership")
	}
	if *resumes != 1 {
		t.Fatalf("spawns=%d", *resumes)
	}
	if !reflect.DeepEqual(events, []string{"kill", "wait", "close"}) {
		t.Fatalf("process events=%v", events)
	}
}

func TestHubForceStopCancelsStartupWithoutReplayingInput(t *testing.T) {
	for _, stalled := range []string{"read", "initial turn"} {
		t.Run(stalled, func(t *testing.T) {
			var turns atomic.Int32
			entered := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			cfg, sessionID, spawns := parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(ctx context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
					turns.Add(1)
					if stalled == "initial turn" {
						close(entered)
						select {
						case <-ctx.Done():
						case <-release:
						}
						return appwire.TurnStartResponse{}, ctx.Err()
					}
					return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "unexpected"}}, nil
				})
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					if stalled == "initial turn" {
						ref, err := appwire.ParseRef(params.Ref)
						if err != nil {
							return appwire.ThreadReadResponse{}, err
						}
						return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: ref.ThreadID, SessionID: ref.ThreadID, Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: ref.ThreadID}}}, nil
					}
					close(entered)
					select {
					case <-ctx.Done():
					case <-release:
					}
					return appwire.ThreadReadResponse{}, ctx.Err()
				})
			})
			spawner := cfg.Spawner.(*fakeRPCSpawner)
			spawn := spawner.resume
			past, _ := cfg.Past.Find(sessionID)
			spawner.spawn = func(ctx context.Context, _ hubcore.SpawnRequest) (rendezvous.Entry, error) {
				req := hubcore.ResumeRequest{SessionID: sessionID, StateDir: past.StateDir, WorkingDir: past.Meta.EnvInfo.WorkingDir}
				entry, err := spawn(ctx, req)
				entry.StartedAt = time.Now()
				entry.StateDir = req.StateDir
				writeRendezvous(t, cfg.RunDir, entry)
				return entry, err
			}
			cfg.Roster = hubcore.NewRoster(cfg.RunDir, forceStopProberFunc(func(rendezvous.Entry) hubcore.ProbeResult { return hubcore.ProbeResult{} }))
			cfg.ResumeLocks = hubcore.NewResumeLocks()
			var events []string
			cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				return &forceStopProcess{events: &events}, nil
			})
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			ref := "local:" + sessionID
			startResult := make(chan error, 1)
			go func() {
				_, err := client.ThreadStart(t.Context(), appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: past.Meta.EnvInfo.WorkingDir, Input: []appwire.InputItem{{Type: "text", Text: "must not reach stopped daemon"}}})
				startResult <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("startup RPC never reached daemon")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := client.Request(ctx, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil); err != nil {
				t.Fatalf("force stop behind startup RPC: %v", err)
			}
			select {
			case err := <-startResult:
				wantTurns := int32(0)
				if stalled == "initial turn" {
					wantTurns = 1
				}
				if err == nil || turns.Load() != wantTurns {
					t.Fatalf("canceled start dispatched initial input: err=%v turns=%d", err, turns.Load())
				}
			case <-ctx.Done():
				t.Fatal("start retained ownership")
			}
			if *spawns != 1 {
				t.Fatalf("spawns=%d", *spawns)
			}
			if !reflect.DeepEqual(events, []string{"kill", "wait", "close"}) {
				t.Fatalf("process events=%v", events)
			}
		})
	}
}

func TestHubForceStopRejectsWaitingMutationUntilExplicitResume(t *testing.T) {
	for _, method := range []string{appwire.MethodThreadModelSet, appwire.MethodThreadCompactStart} {
		t.Run(method, func(t *testing.T) {
			var sessionID string
			var stopped atomic.Bool
			cfg, sid, resumes := parityResumeFixture(t, func(daemon *appserver.Server) {
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					if stopped.Load() {
						return appwire.ThreadReadResponse{}, appwire.SessionUnavailable("daemon exited")
					}
					return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Status: appwire.ThreadStatus{Type: "idle"}, Evener: appwire.EvenerThread{Ref: params.Ref, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true, ChangeModel: true, Compact: true}}}}, nil
				})
				daemon.Router().Handle(method, func(context.Context, json.RawMessage) (any, error) { return appwire.EmptyResponse{}, nil })
			})
			sessionID = sid
			spawner := cfg.Spawner.(*fakeRPCSpawner)
			spawn := spawner.resume
			spawner.resume = func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
				stopped.Store(false)
				entry, err := spawn(ctx, req)
				process := exec.CommandContext(ctx, "true")
				if err := process.Run(); err != nil {
					return rendezvous.Entry{}, err
				}
				if err := os.Remove(filepath.Join(cfg.RunDir, strconv.Itoa(entry.PID)+".json")); err != nil {
					return rendezvous.Entry{}, err
				}
				entry.PID = process.Process.Pid
				entry.StartedAt = time.Now()
				writeRendezvous(t, cfg.RunDir, entry)
				return entry, err
			}
			cfg.Roster = hubcore.NewRoster(cfg.RunDir, forceStopProberFunc(func(rendezvous.Entry) hubcore.ProbeResult {
				if stopped.Load() {
					return hubcore.ProbeResult{}
				}
				return hubcore.ProbeResult{OK: true, SessionID: sessionID, Status: "idle"}
			}))
			cfg.ResumeLocks = hubcore.NewResumeLocks()
			waiting := make(chan struct{})
			exit := make(chan struct{})
			process := &waitingForceStopProcess{entered: waiting, release: exit}
			cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return process, nil })
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			ref := "local:" + sessionID
			if _, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref}); err != nil {
				t.Fatal(err)
			}
			stop := make(chan error, 1)
			go func() {
				stop <- client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil)
			}()
			select {
			case <-waiting:
			case err := <-stop:
				t.Fatalf("force stop returned before confirmation: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("force stop did not reach exit confirmation")
			}
			mutation := make(chan error, 1)
			started := make(chan struct{})
			sources := newHubSourceRegistry(cfg)
			go func() {
				close(started)
				if method == appwire.MethodThreadModelSet {
					mutation <- setThreadModelWithResume(t.Context(), cfg, sources, appwire.ThreadModelSetParams{Ref: ref, Model: "test", ModelProvider: "test"})
				} else {
					mutation <- compactThreadWithResume(t.Context(), cfg, sources, appwire.ThreadCompactStartParams{Ref: ref})
				}
			}()
			<-started
			stopped.Store(true)
			close(exit)
			if err := <-stop; err != nil {
				t.Fatal(err)
			}
			if err := <-mutation; err == nil {
				t.Fatal("waiting mutation resumed after force stop")
			}
			if *resumes != 1 {
				t.Fatalf("automatic resumes after stop=%d", *resumes-1)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if read.Thread.Status.Type != "notLoaded" || !read.Thread.Evener.ResumeRequired || read.Thread.Evener.Capabilities.Send {
				t.Fatalf("stopped snapshot=%+v", read.Thread)
			}
			// An outside controller can restart the process without acknowledging
			// the hub's action fence. Live reads must still advertise that fence.
			req, err := resumeRequestForConfig(cfg, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Spawner.Resume(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Roster.RefreshAndWait(t.Context()); err != nil {
				t.Fatal(err)
			}
			live, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if live.Thread.Status.Type != "idle" || !live.Thread.Evener.ResumeRequired || live.Thread.Evener.Capabilities.Send {
				t.Fatalf("live fenced snapshot=%+v", live.Thread)
			}
			oldClient := client
			client = dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref}); err != nil {
				t.Fatalf("explicit resume: %v", err)
			}
			staleRead, err := oldClient.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil || !staleRead.Thread.Evener.ResumeRequired || staleRead.Thread.Evener.Capabilities.Send {
				t.Fatalf("old connection lost Resume after another client resumed: %+v %v", staleRead.Thread, err)
			}
			fresh, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Thread.Evener.ResumeRequired || !fresh.Thread.Evener.Capabilities.Send {
				t.Fatalf("explicit resume did not clear live fence: %+v", fresh.Thread)
			}

			if err := client.Request(t.Context(), method, appwire.ThreadModelSetParams{Ref: ref, Model: "test", ModelProvider: "test"}, nil); err != nil {
				t.Fatalf("fresh action after explicit resume: %v", err)
			}
		})
	}
}

func TestSessionRecoveryRejectsOldActionsAfterExplicitResume(t *testing.T) {
	cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
	oldEpoch := sessionRecoveryState(cfg, "local:stable", "").Epoch
	finish := cfg.ResumeLocks.BeginForceStop([]string{"stable", "current"})
	if err := cfg.ResumeLocks.PersistForceStop([]string{"stable", "current"}, "current"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	epoch := sessionRecoveryState(cfg, "local:current", "").Epoch
	if err := cfg.ResumeLocks.ExplicitResumeCompleted("current", epoch); err != nil {
		t.Fatal(err)
	}
	if state := sessionRecoveryState(cfg, "local:stable", ""); state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("stable alias still fenced after explicit resume: %+v", state)
	}
	if err := sessionActionRecoveryError(t.Context(), cfg, "local:stable", "", oldEpoch); err == nil {
		t.Fatal("old ownership waiter admitted after explicit resume")
	}
	if err := sessionActionRecoveryError(t.Context(), cfg, "local:current", "", epoch); err != nil {
		t.Fatalf("fresh action refused: %v", err)
	}
	finish = cfg.ResumeLocks.BeginForceStop([]string{"stable", "current"})
	finish.Finish(false)
	if state := sessionRecoveryState(cfg, "local:stable", ""); state.ResumeRequired || state.Stopping != 0 {
		t.Fatalf("failed stop declared session stopped: %+v", state)
	}
}

func TestTurnStartDoesNotRetryRecoveryRejectionAfterExplicitResume(t *testing.T) {
	cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
	const ref = "local:recovery-waiter"
	oldEpoch := sessionRecoveryState(cfg, ref, "").Epoch
	finish := cfg.ResumeLocks.BeginForceStop([]string{"recovery-waiter"})
	if err := cfg.ResumeLocks.PersistForceStop([]string{"recovery-waiter"}, "recovery-waiter"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	if err := cfg.ResumeLocks.ExplicitResumeCompleted("recovery-waiter", cfg.ResumeLocks.RecoveryState("recovery-waiter").Epoch); err != nil {
		t.Fatal(err)
	}
	stale := sessionActionRecoveryError(t.Context(), cfg, ref, "", oldEpoch)
	if stale == nil {
		t.Fatal("fixture did not reject the stale admission")
	}
	oldResolve, oldResume := resolveTurnStartSource, resumeTurnStartThread
	t.Cleanup(func() { resolveTurnStartSource, resumeTurnStartThread = oldResolve, oldResume })
	resolved, resumed, submitted := 0, 0, 0
	source := &scriptedAppSource{id: "local", startTurn: func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		submitted++
		return appwire.TurnStartResponse{}, nil
	}}
	resolveTurnStartSource = func(*appsource.Registry, string, string) (appsource.Source, error) {
		resolved++
		if resolved == 1 {
			return nil, stale
		}
		return source, nil
	}
	resumeTurnStartThread = func(context.Context, hubcore.WebConfig, *appsource.Registry, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
		resumed++
		return appwire.ThreadResumeResponse{}, nil
	}
	server := newHubAppServer(cfg, appsource.NewRegistry())
	_, err := exactDispatch(t.Context(), t, server, appwire.MethodTurnStart, appwire.TurnStartParams{Ref: ref, ClientMutationID: "pending-before-recovery", Input: []appwire.InputItem{{Type: "text", Text: "old queued input"}}})
	if err == nil {
		t.Fatal("stale turn/start was accepted after explicit resume")
	}
	wire := appserver.WireError(err)
	if wire.Code != appwire.CodeUnavailable || evenerErrorInfoFromData(wire.Data) != string(appwire.ErrorActionUnavailable) {
		t.Fatalf("recovery rejection lost wire classification: %+v", wire)
	}
	if wire.Message != stale.Error() {
		t.Fatalf("wire message changed: %q vs %q", wire.Message, stale.Error())
	}
	if resumed != 0 || submitted != 0 || resolved != 1 {
		t.Fatalf("stale input retried: resolutions=%d resumes=%d submissions=%d", resolved, resumed, submitted)
	}
}

func TestHubForceStopRejectsRequestsQueuedBeforeRecovery(t *testing.T) {
	stateDir, runDir := filepath.Join(t.TempDir(), "queued-recovery-0000000000"), t.TempDir()
	sessionID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, rendezvous.Entry{PID: exitedPID(t), SessionID: sessionID, ThreadID: sessionID, StateDir: stateDir, StartedAt: time.Now()})
	var events []string
	var resumes atomic.Int32
	cfg := hubcore.WebConfig{RunDir: runDir, Past: past, ResumeLocks: hubcore.NewResumeLocks(),
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: &events}, nil
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			resumes.Add(1)
			return rendezvous.Entry{}, errors.New("fixture spawn boundary")
		}},
	}
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	appserver.HandleTyped(web.appRPC.Router(), appwire.MethodThreadList, func(ctx context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return appwire.ThreadListResponse{}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	send := func(id int64, method string, params any) {
		t.Helper()
		if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(id), method, params)); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(id int64) appwire.Message {
		t.Helper()
		for {
			message, err := transport.Recv(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if message.Response != nil && message.Response.ID.String() == strconv.FormatInt(id, 10) || message.Error != nil && message.Error.ID.String() == strconv.FormatInt(id, 10) {
				return message
			}
		}
	}
	send(1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if message := receive(1); message.Error != nil {
		t.Fatal(message.Error.Error)
	}
	send(2, appwire.MethodThreadList, appwire.ThreadListParams{})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("serial request did not start")
	}
	ref := "local:" + sessionID
	send(3, appwire.MethodThreadResume, map[string]any{"sessionId": sessionID, "threadId": "ignored-extra-field"})
	send(4, appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: ref, Model: "test", ModelProvider: "test"})
	send(5, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref})
	if message := receive(5); message.Error != nil {
		t.Fatalf("force stop: %+v", message.Error)
	}
	close(release)
	if message := receive(3); message.Error == nil {
		t.Fatal("pre-recovery resume was accepted")
	}
	if message := receive(4); message.Error == nil {
		t.Fatal("pre-recovery model action was accepted")
	}
	if resumes.Load() != 0 {
		t.Fatalf("queued requests restarted daemon %d times after recovery", resumes.Load())
	}
	send(6, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref})
	if message := receive(6); message.Error == nil || resumes.Load() != 0 {
		t.Fatal("old connection acknowledged recovery")
	}
	fresh := dialHubRPC(t, hub)
	defer fresh.Close()
	if _, err := fresh.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	_ = fresh.Request(ctx, appwire.MethodThreadResume, appwire.ThreadResumeParams{Ref: ref}, nil)
	if resumes.Load() != 1 {
		t.Fatalf("fresh explicit resume did not reach spawn boundary: %d", resumes.Load())
	}
}

func TestRecoveryAdmissionUsesNativeTargetAndPreservesRetryEpoch(t *testing.T) {
	for _, tc := range []struct {
		name, method, target string
		params               any
	}{
		{"resume ignores threadId", appwire.MethodThreadResume, "B", map[string]any{"sessionId": "B", "threadId": "A"}},
		{"resume sessionId precedence", appwire.MethodThreadResume, "B", map[string]any{"sessionId": "B", "ref": "local:A", "threadId": "ignored"}},
		{"turn ref precedence", appwire.MethodTurnStart, "B", map[string]any{"ref": "local:B", "threadId": "A"}},
		{"turn threadId", appwire.MethodTurnStart, "B", map[string]any{"threadId": "B", "sessionId": "ignored"}},
		{"sandbox ref precedence", appwire.MethodEvenerSandboxEscalationResolve, "B", map[string]any{"ref": "local:B", "threadId": "A"}},
		{"sandbox threadId", appwire.MethodEvenerSandboxEscalationResolve, "B", map[string]any{"threadId": "B"}},
		{"sandbox ignores mutation ID", appwire.MethodEvenerSandboxEscalationResolve, "B", map[string]any{"ref": "local:B", "clientMutationId": []string{"ignored"}}},
		{"sandbox invalid target", appwire.MethodEvenerSandboxEscalationResolve, "", map[string]any{"threadId": []string{"invalid"}}},
		{"model ignores unknown field types", appwire.MethodThreadModelSet, "B", map[string]any{"ref": "local:B", "threadId": []string{"ignored"}}},
		{"unknown method", "unknown", "", map[string]any{"ref": "local:B"}},
		{"foreign ref", appwire.MethodThreadResume, "", map[string]any{"ref": "remote:B", "sessionId": "A"}},
		{"malformed ref", appwire.MethodThreadResume, "", map[string]any{"ref": "bad", "sessionId": "B"}},
		{"invalid supported field", appwire.MethodThreadResume, "", map[string]any{"sessionId": []string{"bad"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
			ctx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(appwire.NewIntID(1), tc.method, tc.params))
			admission, ok := ctx.Value(sessionRecoveryAdmissionKey{}).(sessionRecoveryAdmission)
			if tc.target == "" {
				if ok {
					t.Fatalf("invalid or unrelated request admitted: %+v", admission)
				}
				return
			}
			if !ok || admission.sessionID != tc.target {
				t.Fatalf("admission=%+v present=%v", admission, ok)
			}
			other := cfg.ResumeLocks.BeginForceStop([]string{"unrelated"})
			if err := cfg.ResumeLocks.PersistForceStop([]string{"unrelated"}, "unrelated"); err != nil {
				t.Fatal(err)
			}
			other.Finish(true)
			if err := sessionActionRecoveryError(t.Context(), cfg, "", tc.target, sessionRequestRecoveryEpoch(ctx, cfg, "", tc.target)); err != nil {
				t.Fatalf("another session invalidated this admission: %v", err)
			}
			finish := cfg.ResumeLocks.BeginForceStop([]string{tc.target})
			if err := cfg.ResumeLocks.PersistForceStop([]string{tc.target}, tc.target); err != nil {
				t.Fatal(err)
			}
			finish.Finish(true)
			if err := cfg.ResumeLocks.ExplicitResumeCompleted(tc.target, cfg.ResumeLocks.RecoveryState(tc.target).Epoch); err != nil {
				t.Fatal(err)
			}
			if err := sessionActionRecoveryError(t.Context(), cfg, "", tc.target, sessionRequestRecoveryEpoch(ctx, cfg, "", tc.target)); err == nil {
				t.Fatal("retry replaced the request's admission epoch")
			}
			if epoch := sessionRequestRecoveryEpoch(t.Context(), cfg, "", tc.target); epoch != cfg.ResumeLocks.RecoveryState(tc.target).Epoch {
				t.Fatal("direct handler did not use execution snapshot")
			}
		})
	}
}

// recoveryReasoningSource records the externally visible setting application.
type recoveryReasoningSource struct {
	relayLifecycleSource
	applied int
}

func (s *recoveryReasoningSource) ID() string { return "local" }
func (s *recoveryReasoningSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	s.applied++
	return nil
}

func (s *recoveryReasoningSource) NotesHumanSet(context.Context, appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	s.applied++
	return appwire.NotesHumanSetResponse{}, nil
}

func (s *recoveryReasoningSource) UrlsRemove(context.Context, appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	s.applied++
	return appwire.UrlsRemoveResponse{}, nil
}

type recoverySandboxSource struct {
	relayLifecycleSource
	approvals []appwire.SandboxEscalationResolveParams
}

func (s *recoverySandboxSource) ID() string { return "local" }
func (s *recoverySandboxSource) ResolveSandboxEscalation(_ context.Context, params appwire.SandboxEscalationResolveParams) error {
	s.approvals = append(s.approvals, params)
	return nil
}

func TestSandboxApprovalCannotCrossSessionRecovery(t *testing.T) {
	for _, unread := range []bool{false, true} {
		for _, target := range []appwire.SandboxEscalationResolveParams{
			{Ref: "local:owner", ThreadID: "ignored", EscalationID: "approval", Approve: true},
			{ThreadID: "owner", EscalationID: "approval", Approve: true},
		} {
			t.Run(fmt.Sprintf("unread=%v/ref=%s", unread, target.Ref), func(t *testing.T) {
				cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
				source := &recoverySandboxSource{}
				sources := appsource.NewRegistry()
				sources.Add(source)
				server := newHubAppServer(cfg, sources)
				// A queued request retains its admission epoch; unread socket input
				// retains the connection generation even when admitted after resume.
				ctx := t.Context()
				message := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodEvenerSandboxEscalationResolve, target)
				if unread {
					ctx = admitSessionConnection(ctx, cfg)
				} else {
					ctx = admitSessionRecovery(ctx, cfg, message)
				}
				finish := cfg.ResumeLocks.BeginForceStop([]string{"owner"})
				if err := cfg.ResumeLocks.PersistForceStop([]string{"owner"}, "owner"); err != nil {
					t.Fatal(err)
				}
				finish.Finish(true)
				if err := cfg.ResumeLocks.ExplicitResumeCompleted("owner", cfg.ResumeLocks.RecoveryState("owner").Epoch); err != nil {
					t.Fatal(err)
				}
				if unread {
					ctx = admitSessionRecovery(ctx, cfg, message)
				}
				_, err := exactDispatch(ctx, t, server, appwire.MethodEvenerSandboxEscalationResolve, target)
				if !isSessionRecoveryAdmissionError(err) || len(source.approvals) != 0 {
					t.Fatalf("old approval reached replacement: err=%v approvals=%v", err, source.approvals)
				}
				wire := appserver.WireError(err)
				raw, err := json.Marshal(wire.Data)
				if err != nil {
					t.Fatal(err)
				}
				var data appwire.ErrorData
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				if data.ClientMutationID != "" || data.MutationOutcome != "" {
					t.Fatalf("non-durable approval acquired mutation metadata: %+v", data)
				}
				fresh := admitSessionRecovery(admitSessionConnection(t.Context(), cfg), cfg, message)
				if _, err := exactDispatch(fresh, t, server, appwire.MethodEvenerSandboxEscalationResolve, target); err != nil {
					t.Fatal(err)
				}
				if len(source.approvals) != 1 || source.approvals[0] != target {
					t.Fatalf("fresh approval not delivered exactly once: %v", source.approvals)
				}
			})
		}
	}
}

func TestCapturedSessionActionsRejectAdmissionBeforeRecovery(t *testing.T) {
	for _, method := range []string{
		appwire.MethodTurnStart, appwire.MethodTurnSteer, appwire.MethodTurnInterrupt,
		appwire.MethodThreadModelSet, appwire.MethodThreadVisionModelSet,
		appwire.MethodThreadReasoningEffortSet, appwire.MethodThreadCompactStart,
		appwire.MethodThreadClear, appwire.MethodThreadShutdown, appwire.MethodGoalSet,
		appwire.MethodTurnQueue, appwire.MethodTurnDrainAsSteer,
		appwire.MethodTurnPromoteQueuedAsSteer, appwire.MethodTurnCancelQueued,
		appwire.MethodNotesHumanSet, appwire.MethodUrlsRemove,
	} {
		t.Run(method, func(t *testing.T) {
			cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
			source := &recoveryReasoningSource{}
			// The notes relays gate on the shared-notes capability after the
			// admission check; advertise it so a stale write that crosses
			// admission is visibly applied rather than masked by that gate.
			source.thread.Evener.Capabilities.SharedNotes = true
			sources := appsource.NewRegistry()
			sources.Add(source)
			server := newHubAppServer(cfg, sources)
			params := map[string]any{
				"ref": "local:admitted-session", "clientMutationId": "old-action",
				"expectedInstanceId": "instance", "expectedEntryId": "entry", "index": 0,
				"input":           []appwire.InputItem{{Type: "text", Text: "queued input"}},
				"reasoningEffort": "high", "model": "test", "modelProvider": "test",
			}
			ctx := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(appwire.NewIntID(1), method, params))
			finish := cfg.ResumeLocks.BeginForceStop([]string{"admitted-session"})
			if err := cfg.ResumeLocks.PersistForceStop([]string{"admitted-session"}, "admitted-session"); err != nil {
				t.Fatal(err)
			}
			finish.Finish(true)
			if err := cfg.ResumeLocks.ExplicitResumeCompleted("admitted-session", cfg.ResumeLocks.RecoveryState("admitted-session").Epoch); err != nil {
				t.Fatal(err)
			}
			_, err := exactDispatch(ctx, t, server, method, params)
			if !isSessionRecoveryAdmissionError(err) {
				t.Fatalf("old action crossed recovery admission: err=%v applied=%d", err, source.applied)
			}
			if source.applied != 0 {
				t.Fatal("old reasoning setting applied to resumed session")
			}
			if method == appwire.MethodThreadReasoningEffortSet {
				fresh := admitSessionRecovery(t.Context(), cfg, appwire.RequestMessage(appwire.NewIntID(2), method, params))
				if _, err := exactDispatch(fresh, t, server, method, params); err != nil || source.applied != 1 {
					t.Fatalf("fresh reasoning setting failed: err=%v applied=%d", err, source.applied)
				}
			}
		})
	}
}

func TestForceStopUnconfirmedSignalRequiresExplicitResume(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			runDir := t.TempDir()
			entry := rendezvous.Entry{PID: 4242, SessionID: "current", ThreadID: "current", WorkspaceRef: "local:stable", StateDir: t.TempDir(), StartedAt: time.Now()}
			writeRendezvous(t, runDir, entry)
			var events []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var process daemonprocess.Process = &forceStopProcess{events: &events, waitErr: failure}
			if errors.Is(failure, context.Canceled) {
				waiting := &waitingForceStopProcess{entered: make(chan struct{}), release: make(chan struct{})}
				process = waiting
				go func() {
					select {
					case <-waiting.entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				return process, nil
			})}
			if err := forceStopThread(ctx, cfg, appwire.ThreadForceStopParams{Ref: "local:stable"}, nil); err == nil {
				t.Fatal("unconfirmed exit reported success")
			}
			for _, alias := range []string{"stable", "current"} {
				state := cfg.ResumeLocks.RecoveryState(alias)
				if !state.ResumeRequired || state.Stopping != 0 {
					t.Fatalf("signaled alias lost explicit resume requirement: %s %+v", alias, state)
				}
				if _, err := hubThreadAutoResume(t.Context(), cfg, appsource.NewRegistry(), appwire.ThreadResumeParams{Session: alias}); err == nil {
					t.Fatal("automatic resume accepted after unconfirmed termination")
				}
				thread := applyThreadResumeRequirement(t.Context(), cfg, "", alias, appwire.Thread{})
				if !thread.Evener.ResumeRequired {
					t.Fatal("fresh client cannot discover explicit resume requirement")
				}
			}
		})
	}
}

func TestConnectionRecoveryFenceIncludesUnreadActionsAndConnectionsBornDuringStop(t *testing.T) {
	cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
	before := admitSessionConnection(t.Context(), cfg)
	finish := cfg.ResumeLocks.BeginForceStop([]string{"stable", "current"})
	during := admitSessionConnection(t.Context(), cfg)
	if err := cfg.ResumeLocks.PersistForceStop([]string{"stable", "current"}, "current"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	if err := cfg.ResumeLocks.ExplicitResumeCompleted("stable", cfg.ResumeLocks.RecoveryState("stable").Epoch); err != nil {
		t.Fatal(err)
	}
	sources := appsource.NewRegistry()
	source := &recoveryReasoningSource{}
	sources.Add(source)
	server := newHubAppServer(cfg, sources)
	for name, ctx := range map[string]context.Context{"before": before, "during": during} {
		t.Run(name, func(t *testing.T) {
			for _, alias := range []string{"stable", "current"} {
				for _, method := range []string{appwire.MethodThreadResume, appwire.MethodThreadReasoningEffortSet, appwire.MethodTurnStart} {
					params := map[string]any{"ref": "local:" + alias, "clientMutationId": "old-action", "input": []appwire.InputItem{{Type: "text", Text: "unread input"}}}
					admitted := admitSessionRecovery(ctx, cfg, appwire.RequestMessage(appwire.NewIntID(1), method, params))
					if _, err := exactDispatch(admitted, t, server, method, params); !isSessionRecoveryAdmissionError(err) {
						t.Fatalf("%s unread request escaped connection fence: %v", method, err)
					}
				}
				thread := applyThreadResumeRequirement(ctx, cfg, "", alias, appwire.Thread{Evener: appwire.EvenerThread{Capabilities: appwire.ThreadCapabilities{Send: true}}})
				if !thread.Evener.ResumeRequired || thread.Evener.Capabilities.Send {
					t.Fatal("stale connection read lost actionable Resume after another client resumed")
				}
			}
			unrelated := appwire.ThreadReasoningEffortSetParams{Ref: "local:unrelated", ReasoningEffort: "high"}
			admitted := admitSessionRecovery(ctx, cfg, appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodThreadReasoningEffortSet, unrelated))
			beforeApplied := source.applied
			if _, err := exactDispatch(admitted, t, server, appwire.MethodThreadReasoningEffortSet, unrelated); err != nil || source.applied != beforeApplied+1 {
				t.Fatalf("unrelated target action invalidated: %v", err)
			}
		})
	}
	fresh := admitSessionConnection(t.Context(), cfg)
	if sessionConnectionRecoveryError(fresh, cfg, "", "current") != nil {
		t.Fatal("fresh connection remained stale")
	}
	finish = cfg.ResumeLocks.BeginForceStop([]string{"current"})
	if err := cfg.ResumeLocks.PersistForceStop([]string{"current"}, "current"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	fresh = admitSessionConnection(t.Context(), cfg)
	if _, err := hubThreadAutoResume(fresh, cfg, appsource.NewRegistry(), appwire.ThreadResumeParams{Session: "current"}); err == nil {
		t.Fatal("fresh connection automatically cleared explicit resume requirement")
	}
}

func TestRecoveryAdmissionNamesBlockedDurableMutation(t *testing.T) {
	for _, recovery := range []string{"active", "completed", "failed"} {
		t.Run(recovery, func(t *testing.T) {
			cfg := hubcore.WebConfig{ResumeLocks: hubcore.NewResumeLocks()}
			ctx := admitSessionConnection(t.Context(), cfg)
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			finish := cfg.ResumeLocks.BeginForceStop([]string{"owner"})
			if recovery == "active" {
				defer finish.Finish(false)
			} else {
				finish.Finish(recovery == "completed")
			}
			server := newHubAppServer(cfg, appsource.NewRegistry())
			params := appwire.TurnStartParams{Ref: "local:owner", ClientMutationID: "preserved-intent", ExpectedInstanceID: "known-instance", Input: []appwire.InputItem{{Type: "text", Text: "keep this input"}}}
			_, err := exactDispatch(ctx, t, server, appwire.MethodTurnStart, params)
			if !isSessionRecoveryAdmissionError(err) {
				t.Fatalf("lost terminal admission classification: %v", err)
			}
			wireErr := client.Request(t.Context(), appwire.MethodTurnStart, params, nil)
			for boundary, rejection := range map[string]error{"handler": err, "WebSocket": wireErr} {
				wire := appserver.WireError(rejection)
				raw, marshalErr := json.Marshal(wire.Data)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var data appwire.ErrorData
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.ClientMutationID != params.ClientMutationID || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
					t.Fatalf("%s recovery rejection cannot settle dispatcher state: %+v", boundary, wire)
				}
			}
		})
	}
}

func TestBlockedAdmissionMetadataPreservesWrappedRecoveryCause(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		original := map[string]any{"evenerErrorInfo": string(appwire.ErrorActionUnavailable), "cause": "sessionRecovery", "detail": "retained"}
		var rejection error = sessionRecoveryAdmissionError{appwire.WireError{Code: appwire.CodeUnavailable, Message: "resume required", Data: original}}
		if wrapped {
			rejection = errors.Join(errors.New("request context"), rejection)
		}
		blocked := blockedAdmissionMutationError(rejection, "mutation-owner")
		if !isSessionRecoveryAdmissionError(blocked) {
			t.Fatalf("wrapped=%v lost terminal marker", wrapped)
		}
		wire := appserver.WireError(blocked)
		data, ok := wire.Data.(map[string]any)
		if !ok || wire.Code != appwire.CodeUnavailable || wire.Message != "resume required" || data["cause"] != "sessionRecovery" || data["detail"] != "retained" || data["clientMutationId"] != "mutation-owner" || data["mutationOutcome"] != string(appwire.MutationOutcomeUnknown) || data["retryDisposition"] != string(appwire.RetryDispositionBlocked) {
			t.Fatalf("wrapped=%v metadata=%+v", wrapped, wire.Data)
		}
		if _, changed := original["clientMutationId"]; changed {
			t.Fatal("annotation mutated original error data")
		}
	}
}

func TestHubForceStopUnconfirmedExitReadAfterOwnershipDisappears(t *testing.T) {
	cfg, sessionID, resumes := parityResumeFixture(t, func(*appserver.Server) {})
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	entry := rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StateDir: t.TempDir(), StartedAt: time.Now()}
	writeRendezvous(t, cfg.RunDir, entry)
	var events []string
	cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &events, waitErr: context.DeadlineExceeded}, nil
	})
	ref := "local:" + sessionID
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: ref}, nil); err == nil {
		t.Fatal("unconfirmed exit reported success")
	}
	if !slices.Contains(events, "kill") {
		t.Fatal("termination was not requested")
	}
	// Exit can finish after the caller's confirmation deadline.
	if err := rendezvous.Remove(cfg.RunDir, entry.PID); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Status.Type != "notLoaded" || !read.Thread.Evener.ResumeRequired || read.Thread.Evener.Capabilities.Send || len(read.Thread.Turns) == 0 {
		t.Fatalf("unconfirmed stopped snapshot = %+v", read.Thread)
	}
	if *resumes != 0 {
		t.Fatal("read automatically resumed a stopped session")
	}
}

func TestForceStopRepeatedSharedAliasPreservesDurableTarget(t *testing.T) {
	for _, retryAlias := range []string{"A", "B"} {
		t.Run(retryAlias, func(t *testing.T) {
			root, runDir := t.TempDir(), t.TempDir()
			locks, err := hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			var events []string
			dead := map[int]bool{}
			cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
				if dead[target.PID] {
					return nil, daemonprocess.ErrExited
				}
				return &forceStopProcess{events: &events, onWait: func() { dead[target.PID] = true }}, nil
			})}
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 101, SessionID: "B", ThreadID: "B", WorkspaceRef: "local:A"})
			if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:A"}, nil); err != nil {
				t.Fatal(err)
			}
			writeRendezvous(t, runDir, rendezvous.Entry{PID: 102, SessionID: "C", ThreadID: "C", WorkspaceRef: "local:B"})
			if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:B"}, nil); err != nil {
				t.Fatal(err)
			}
			cfg.ResumeLocks, err = hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + retryAlias}, nil)
			if retryAlias == "A" && err == nil {
				t.Error("superseded alias stop accepted")
			}
			if retryAlias == "B" && err != nil {
				t.Fatal(err)
			}
			cfg.ResumeLocks, err = hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ResumeLocks.RecoveryState("B").ResumeSessionID; got != "C" {
				t.Fatalf("repeated stop replaced current target with %q", got)
			}
			called := false
			cfg.Spawner = &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
				called = true
				if req.SessionID != "C" {
					t.Errorf("launched historical session %q", req.SessionID)
				}
				return rendezvous.Entry{}, errors.New("launcher observed")
			}}
			_, _ = hubThreadResume(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: "local:B"})
			if !called {
				t.Fatal("current target did not reach launcher")
			}
		})
	}
}

func TestForceStopRejectsExitedConflictingTargetsWithoutAuthority(t *testing.T) {
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return nil, daemonprocess.ErrExited })}
	writeRendezvous(t, cfg.RunDir, rendezvous.Entry{PID: 101, SessionID: "B", ThreadID: "B", WorkspaceRef: "local:A"})
	writeRendezvous(t, cfg.RunDir, rendezvous.Entry{PID: 102, SessionID: "C", ThreadID: "C", WorkspaceRef: "local:B"})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:B"}, nil); err == nil {
		t.Fatal("ambiguous exited transcripts accepted")
	}
	if cfg.ResumeLocks.RecoveryState("B").ResumeRequired {
		t.Fatal("ambiguous lookup established recovery authority")
	}
}

func TestForceStopRejectsRecoveryAuthorityChangedAfterDiscovery(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{"A", "B"})
	if err := locks.PersistForceStop([]string{"A", "B"}, "B"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 101, SessionID: "B", ThreadID: "B", WorkspaceRef: "local:A"})
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		// A competing stop commits new authority after discovery, before this
		// request acquires its alias reservations.
		finish := locks.BeginForceStop([]string{"B", "C"})
		if err := locks.PersistForceStop([]string{"B", "C"}, "C"); err != nil {
			t.Fatal(err)
		}
		finish.Finish(true)
		return &forceStopProcess{events: &events}, nil
	})}
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:B"}, nil); err == nil {
		t.Fatal("stale recovery target accepted")
	}
	if slices.Contains(events, "kill") {
		t.Fatal("stale target signaled")
	}
	if got := locks.RecoveryState("B").ResumeSessionID; got != "C" {
		t.Fatalf("new authority overwritten: %q", got)
	}
}

func TestForceStopRejectsSoleExitedMarkerForSupersededTarget(t *testing.T) {
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{"B", "C"})
	if err := locks.PersistForceStop([]string{"B", "C"}, "C"); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) { return nil, daemonprocess.ErrExited })}
	writeRendezvous(t, cfg.RunDir, rendezvous.Entry{PID: 101, SessionID: "B", ThreadID: "B", WorkspaceRef: "local:A"})
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:B"}, nil); err == nil {
		t.Fatal("superseded marker accepted after current marker removal")
	}
	if got := locks.RecoveryState("B").ResumeSessionID; got != "C" {
		t.Fatalf("durable current target overwritten: %q", got)
	}
}

// TestForceStopExpectedDaemonStaleClearAlias: a resident row rendered before
// the daemon cleared to a new session must not act on the cleared daemon,
// even though PID and start instant are unchanged.
func TestForceStopExpectedDaemonStaleClearAlias(t *testing.T) {
	runDir := t.TempDir()
	preClear := rendezvous.Entry{PID: 4301, SessionID: "pre-clear", ThreadID: "pre-clear", WorkspaceRef: "local:pre-clear", StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, preClear)
	expected := daemonIdentity(preClear)
	postClear := preClear
	postClear.SessionID, postClear.ThreadID, postClear.WorkspaceRef = "post-clear", "post-clear", "local:post-clear"
	if err := rendezvous.Remove(runDir, preClear.PID); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, postClear)
	locks := hubcore.NewResumeLocks()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:post-clear", ExpectedDaemon: &expected}, nil)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("stale clear alias err=%v, want conflict", err)
	}
	if len(events) != 0 {
		t.Fatalf("stale clear alias reached the process: %v", events)
	}
	if state := locks.RecoveryState("post-clear"); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("stale clear alias fenced recovery: %+v", state)
	}
}

// TestForceStopExpectedDaemonRejectsPIDReuse: the same PID with a new start
// instant is a different daemon; a pre-restart identity must not signal it.
func TestForceStopExpectedDaemonRejectsPIDReuse(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4302, SessionID: "reused", ThreadID: "reused", WorkspaceRef: "local:reused", StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, entry)
	expected := daemonIdentity(entry)
	restarted := entry
	restarted.StartedAt = entry.StartedAt.Add(time.Second)
	if err := rendezvous.Remove(runDir, entry.PID); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, restarted)
	locks := hubcore.NewResumeLocks()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:reused", ExpectedDaemon: &expected}, nil)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("PID reuse err=%v, want conflict", err)
	}
	if len(events) != 0 {
		t.Fatalf("PID reuse identity signaled the restarted process: %v", events)
	}
}

// TestForceStopExpectedDaemonRejectsProtocolChange: a ref-only force stop
// against an older-protocol daemon is supported (see
// TestHubForceStopConfirmsExitAndPreservesSavedData), but an identity rendered
// for the current-protocol daemon must not act after the process restarted
// onto a different protocol.
func TestForceStopExpectedDaemonRejectsProtocolChange(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4303, SessionID: "proto", ThreadID: "proto", WorkspaceRef: "local:proto", StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, entry)
	expected := daemonIdentity(entry)
	restarted := entry
	restarted.Protocol = "evener-appwire-v3"
	if err := rendezvous.Remove(runDir, entry.PID); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, restarted)
	locks := hubcore.NewResumeLocks()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:proto", ExpectedDaemon: &expected}, nil)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("protocol change err=%v, want conflict", err)
	}
	if len(events) != 0 {
		t.Fatalf("protocol-change identity signaled the replacement: %v", events)
	}
}

// TestForceStopExpectedDaemonRevalidationUnderLocks swaps ownership while the
// stop waits for an alias lock. The refusal is the composite fence: the
// existing ownership revalidation compares full rendezvous entries (strictly
// stronger than the identity fingerprint), so any swap the expected-identity
// recheck would catch is caught there first; what this pins is that no signal
// is ever delivered across a mid-wait replacement.
func TestForceStopExpectedDaemonRevalidationUnderLocks(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: 4304, SessionID: "current", ThreadID: "current", WorkspaceRef: "local:stable", StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, entry)
	expected := daemonIdentity(entry)
	locks := hubcore.NewResumeLocks()
	locks.For("stable").Lock()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:current", ExpectedDaemon: &expected}, nil)
	}()
	// Wait until the attempt holds the recovery fence (BeginForceStop runs
	// before the alias locks), so the swap lands while it waits.
	deadline := time.Now().Add(10 * time.Second)
	for locks.RecoveryState("current").Stopping == 0 {
		if time.Now().After(deadline) {
			t.Fatal("force stop did not reach the recovery fence")
		}
		time.Sleep(5 * time.Millisecond)
	}
	replacement := entry
	replacement.StartedAt = entry.StartedAt.Add(time.Second)
	if err := rendezvous.Remove(runDir, entry.PID); err != nil {
		t.Fatal(err)
	}
	writeRendezvous(t, runDir, replacement)
	locks.For("stable").Unlock()
	if err := <-errCh; err == nil {
		t.Fatal("force stop signaled across a mid-wait ownership change")
	}
	if slices.Contains(events, "kill") {
		t.Fatalf("replacement was signaled: %v", events)
	}
	if state := locks.RecoveryState("current"); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("refused stop left a recovery fence: %+v", state)
	}
}

// TestForceStopExpectedDaemonMaliciousArbitraryInput: an attacker-supplied
// identity naming an unrelated PID/generation must never reach the victim's
// process — the hub compares against verified discovery before opening any
// handle.
func TestForceStopExpectedDaemonMaliciousArbitraryInput(t *testing.T) {
	runDir := t.TempDir()
	victim := rendezvous.Entry{PID: 4305, SessionID: "victim", ThreadID: "victim", WorkspaceRef: "local:victim", StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	writeRendezvous(t, runDir, victim)
	malicious := appwire.DaemonIdentity{Ref: "local:victim", PID: 1, StartedAt: "1970-01-01T00:00:00Z", Generation: "deadbeef"}
	locks := hubcore.NewResumeLocks()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		events = append(events, "open")
		return &forceStopProcess{events: &events}, nil
	})}
	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:victim", ExpectedDaemon: &malicious}, nil)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("malicious identity err=%v, want conflict", err)
	}
	if len(events) != 0 {
		t.Fatalf("malicious identity opened the victim process: %v", events)
	}
}

// TestForceStopResolvesExactIdentityAmongSameRefResidents is the M5 regression:
// two live residents share the requested ref but are different daemons, so the
// ref alone cannot choose between them. A request carrying the addressed
// daemon's full rendered identity must resolve and stop exactly that daemon
// instead of failing the ref-ambiguity check before the identity is consulted.
func TestForceStopResolvesExactIdentityAmongSameRefResidents(t *testing.T) {
	runDir := t.TempDir()
	const shared = "shared-ref"
	addressed := rendezvous.Entry{PID: 4311, SessionID: shared, ThreadID: shared, WorkspaceRef: "local:" + shared, StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	other := addressed
	other.PID = 4312
	other.StartedAt = addressed.StartedAt.Add(time.Second)
	other.StateDir = t.TempDir()
	writeRendezvous(t, runDir, addressed)
	writeRendezvous(t, runDir, other)

	var opened []int
	var events []string
	controller := forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
		opened = append(opened, target.PID)
		if target.PID != addressed.PID {
			t.Errorf("force stop opened PID %d, want the addressed %d", target.PID, addressed.PID)
		}
		return &forceStopProcess{events: &events}, nil
	})
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: hubcore.NewResumeLocks(), DaemonProcesses: controller}
	expected := daemonIdentity(addressed)
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + shared, ExpectedDaemon: &expected}, nil); err != nil {
		t.Fatalf("addressed daemon was not resolved: %v", err)
	}
	if !reflect.DeepEqual(opened, []int{addressed.PID}) {
		t.Fatalf("opened daemons = %v, want only the addressed %d", opened, addressed.PID)
	}
	if !reflect.DeepEqual(events, []string{"kill", "wait", "close"}) {
		t.Fatalf("stop events = %v", events)
	}
}

// TestForceStopRefusesStaleIdentityAmongSameRefResidents pins M5's safety
// constraint: when several live residents share a ref, a rendered identity that
// matches none of them must never fall back to a same-ref replacement.
func TestForceStopRefusesStaleIdentityAmongSameRefResidents(t *testing.T) {
	runDir := t.TempDir()
	const shared = "shared-ref"
	rotated := rendezvous.Entry{PID: 4321, SessionID: shared, ThreadID: shared, WorkspaceRef: "local:" + shared, StateDir: t.TempDir(), Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now()}
	stale := daemonIdentity(rotated)
	current := rotated
	current.PID = 4322
	current.StartedAt = rotated.StartedAt.Add(time.Second)
	current.StateDir = t.TempDir()
	twin := current
	twin.PID = 4323
	twin.StartedAt = current.StartedAt.Add(time.Second)
	twin.StateDir = t.TempDir()
	writeRendezvous(t, runDir, current)
	writeRendezvous(t, runDir, twin)

	locks := hubcore.NewResumeLocks()
	var events []string
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &events}, nil
	})}
	err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + shared, ExpectedDaemon: &stale}, nil)
	if err == nil {
		t.Fatal("stale identity was accepted against same-ref replacements")
	}
	if _, ok := errors.AsType[appwire.WireError](err); !ok {
		t.Fatalf("stale refusal is not a typed wire error: %v", err)
	}
	if slices.Contains(events, "kill") || slices.Contains(events, "wait") {
		t.Fatalf("stale identity signaled a same-ref replacement: %v", events)
	}
	if state := locks.RecoveryState(shared); state.Stopping != 0 || state.ResumeRequired {
		t.Fatalf("stale identity fenced recovery: %+v", state)
	}
}

// exitedPID is the PID of a fixture process that has already exited: a
// rendezvous file naming it is a crashed daemon's on any host. A fixed number
// is dead on one machine and somebody's live process on another - on a
// GitHub runner it was - and the roster probes the file's PID against the
// real process table.
func exitedPID(t *testing.T) int {
	t.Helper()
	command := exec.CommandContext(t.Context(), "cat")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &fixtureExitProcess{input: input, command: command}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	return command.Process.Pid
}

// TestConfirmedStopAdmissionBarrierDefersRegistrationDuringNoOp pins the
// interleaving RoboRev found: ordinary shutdown's confirmed-stopped no-op holds
// the session's alias reservation across its final HasActiveResume check and
// its success return, but a new explicit Resume could still register inside
// that window (RegisterResume only took the registry mutex), wait on the held
// alias lock, and launch after shutdown had already reported success. The no-op
// is blocked on the reservation here, so a registration admitted while that
// reservation is held is exactly a registration landing in that window.
func TestConfirmedStopAdmissionBarrierDefersRegistrationDuringNoOp(t *testing.T) {
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
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
		held := locks.For(sessionID)
		held.Lock()
		noopDone := make(chan struct{})
		go func() {
			if _, err := confirmedStoppedWithoutClaim(t.Context(), cfg, sessionID, false, nil); err != nil {
				t.Errorf("confirmed-stopped no-op: %v", err)
			}
			close(noopDone)
		}()
		synctest.Wait() // the no-op is now blocked acquiring the alias reservation
		registered := make(chan error, 1)
		go func() {
			_, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
			registered <- err
		}()
		synctest.Wait() // a committed registration would now be admitted
		select {
		case err := <-registered:
			held.Unlock()
			<-noopDone
			t.Fatalf("RegisterResume was admitted while the confirmed-stopped no-op held the alias reservation: %v", err)
		default:
		}
		held.Unlock()
		<-noopDone
		// The no-op published its stopped decision while it held the
		// reservation, so the registration that was waiting on the alias must
		// re-admit on a snapshot taken after the decision instead of launching
		// on one taken before shutdown reported success.
		if err := <-registered; !errors.Is(err, hubcore.ErrResumeInvalidated) {
			t.Fatalf("waiting registration after the no-op = %v, want ErrResumeInvalidated", err)
		}
		fresh, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		fresh.Complete(nil)
	})
}

// TestConfirmedStopNoOpInvalidatesWaitingResumeRegistration pins the admission
// race RoboRev found: ordinary shutdown's confirmed-stopped no-op only
// serialized with RegisterResume, so a Resume registration already waiting for
// the alias when the no-op decided could register with its pre-decision
// snapshot the moment the no-op released — launching after shutdown had
// already reported success. The no-op must invalidate that snapshot as it
// publishes the decision; the waiter then re-admits afterwards.
func TestConfirmedStopNoOpInvalidatesWaitingResumeRegistration(t *testing.T) {
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
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Block the no-op on its first under-reservation deletion check, so it
		// holds the alias reservation while the registration waits for it.
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
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks, DeletionStore: store}
		stopped := make(chan error, 1)
		go func() {
			stopped <- shutdownThreadTolerateExited(t.Context(), cfg, appsource.NewRegistry(), appwire.ThreadShutdownParams{Ref: "local:" + sessionID})
		}()
		<-entered // the no-op holds the alias reservation
		epochs := map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch}
		registered := make(chan error, 1)
		go func() {
			_, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, epochs)
			registered <- err
		}()
		synctest.Wait() // the registration is now waiting for the held alias
		close(release)
		if err := <-stopped; err != nil {
			t.Fatalf("confirmed-stopped shutdown no-op: %v", err)
		}
		if err := <-registered; !errors.Is(err, hubcore.ErrResumeInvalidated) {
			t.Fatalf("registration waiting across the no-op = %v, want ErrResumeInvalidated", err)
		}
		// The decision is published: a fresh admission snapshot registers.
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		active.Complete(nil)
	})
}

// TestShutdownConfirmedStoppedRefreshesRoster pins the confirmed-stopped
// shutdown fast path's parity with the force-stop shortcut: returning success
// while cfg.Roster still advertises the session leaves the frontend's
// live/stopped projection stale until the next watcher pass, so the fast path
// must run the same refresh before returning.
func TestShutdownConfirmedStoppedRefreshesRoster(t *testing.T) {
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
	refreshed := false
	original := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error {
		refreshed = true
		return nil
	}
	defer func() { hubRosterRefresh = original }()
	cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks, Roster: hubcore.NewRoster(t.TempDir(), nil)}
	if err := shutdownThreadTolerateExited(t.Context(), cfg, appsource.NewRegistry(), appwire.ThreadShutdownParams{Ref: "local:" + sessionID}); err != nil {
		t.Fatalf("confirmed-stopped shutdown no-op: %v", err)
	}
	if !refreshed {
		t.Fatal("confirmed-stopped shutdown fast path returned success without refreshing the roster")
	}
}

// TestForceStopResumeCleanupFailureIsUnavailable pins the force-stop boundary
// classification: a retained child-cleanup failure from stop.Wait is a
// retryable "cleanup remains unconfirmed" state and must reach the RPC layer as
// Unavailable, not as a raw resumeCleanupError that maps to Internal.
func TestForceStopResumeCleanupFailureIsUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		sessionID := hubtest.SessionID(t)
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		cleanupErr := &resumeCleanupError{cause: errors.New("fixture child cleanup denied")}
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait() // force stop canceled the registered Resume and is waiting on cleanup
		active.Complete(cleanupErr)
		err = <-stopped
		if err == nil {
			t.Fatal("unconfirmed cleanup reported success")
		}
		if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
			t.Fatalf("force stop cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cleanup classification replaced a context error: %v", err)
		}
	})
}

// TestForceStopConfirmedStoppedCleanupFailureIsUnavailable pins the second
// stop/cleanup boundary. confirmedStoppedWithoutClaim cancels and drains the
// recovery group's in-flight Resume; when that retained child cleanup cannot be
// confirmed, the error must reach the RPC layer as retryable Unavailable, not
// raw. The Resume is registered on a sibling alias so the top-of-function stop
// cannot see it and this path owns the failure.
func TestForceStopConfirmedStoppedCleanupFailureIsUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		sessionID := hubtest.SessionID(t)
		sibling := hubtest.SessionID(t)
		finish := locks.BeginForceStop([]string{sessionID, sibling})
		if err := locks.PersistForceStop([]string{sessionID, sibling}, sessionID); err != nil {
			t.Fatal(err)
		}
		if err := locks.ConfirmForceStop(sessionID); err != nil {
			t.Fatal(err)
		}
		finish.Finish(true)
		active, err := locks.RegisterResume(t.Context(), sibling, []string{sibling}, map[string]uint64{sibling: locks.RecoveryState(sibling).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait() // force stop reached the recovery group's cleanup wait
		active.Complete(&resumeCleanupError{cause: errors.New("fixture child cleanup denied")})
		err = <-stopped
		if err == nil {
			t.Fatal("unconfirmed cleanup reported success")
		}
		if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
			t.Fatalf("confirmed-stopped cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cleanup classification replaced a context error: %v", err)
		}
	})
}

// TestForceStopPostDiscoveryCleanupFailureIsUnavailable pins the post-discovery
// cancelActiveResumes boundary. A Resume that registered during process
// discovery is reached through the verified entry's aliases, not the requested
// ref alias; its retained child cleanup failure must classify as retryable
// Unavailable, not raw.
func TestForceStopPostDiscoveryCleanupFailureIsUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		stable := hubtest.SessionID(t)
		current := hubtest.SessionID(t)
		entry := rendezvous.Entry{
			PID: 4242, SessionID: current, ThreadID: current,
			WorkspaceRef: "local:" + stable, StateDir: t.TempDir(),
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, entry)
		locks := hubcore.NewResumeLocks()
		// Registered on the entry's current alias, so the top-of-function stop
		// on the requested stable alias sees no active Resume.
		active, err := locks.RegisterResume(t.Context(), current, []string{current}, map[string]uint64{current: locks.RecoveryState(current).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		var events []string
		cfg := hubcore.WebConfig{
			RunDir: runDir, ResumeLocks: locks,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				return &forceStopProcess{events: &events}, nil
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stable}, nil)
		}()
		synctest.Wait() // force stop reached the post-discovery cleanup wait
		active.Complete(&resumeCleanupError{cause: errors.New("fixture child cleanup denied")})
		err = <-stopped
		if err == nil {
			t.Fatal("unconfirmed cleanup reported success")
		}
		if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
			t.Fatalf("post-discovery cleanup failure wire code = %d, want %d (Unavailable): %v", code, appwire.CodeUnavailable, err)
		}
	})
}

// TestConfirmedStoppedNoOpToleratesDiscoveryErrorWhenNotStopping pins Low 3:
// for the ordinary shutdown caller (!stopResumes) a transient strict-discovery
// failure must fall through to the tolerant source attempt rather than failing
// thread/shutdown for a session whose recovery state is already
// ResumeRequired && ExitConfirmed. The destructive stopResumes path must keep
// blocking on the same failure.
func TestConfirmedStoppedNoOpToleratesDiscoveryErrorWhenNotStopping(t *testing.T) {
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
	cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks}
	stopped, err := confirmedStoppedWithoutClaim(t.Context(), cfg, sessionID, false, nil)
	if err != nil {
		t.Fatalf("ordinary shutdown no-op failed on a discovery error: %v", err)
	}
	if stopped {
		t.Fatal("a corrupt discovery read must not prove the session stopped")
	}
	if _, err := confirmedStoppedWithoutClaim(t.Context(), cfg, sessionID, true, nil); err == nil {
		t.Fatal("force stop must block on a strict discovery failure")
	} else if code := appserver.WireError(err).Code; code != appwire.CodeUnavailable {
		t.Fatalf("force stop discovery failure wire code = %d, want %d (Unavailable)", code, appwire.CodeUnavailable)
	}
}

// TestForceStopStaleExpectedDaemonDoesNotCancelResume pins the cancellation
// ordering contract: a force stop whose caller-rendered daemon identity no
// longer matches the current owner must be refused before it installs any
// cancellation fence, so the refusal cannot abort the in-flight explicit Resume
// the frontend's stale resident row can no longer address.
func TestForceStopStaleExpectedDaemonDoesNotCancelResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		entry := rendezvous.Entry{
			PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, entry)
		expected := daemonIdentity(entry)
		expected.Generation = "stale-rendered-identity"
		locks := hubcore.NewResumeLocks()
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
		if err != nil {
			t.Fatal(err)
		}
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks}
		completed := make(chan error, 1)
		go func() {
			completed <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &expected}, nil)
		}()
		synctest.Wait()
		if err := active.Context().Err(); err != nil {
			t.Errorf("stale force stop canceled the in-flight Resume: %v", err)
		}
		assertStaleIdentityConflict := func(err error) {
			t.Helper()
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("stale force stop error = %v, want conflict", err)
			}
		}
		select {
		case err := <-completed:
			assertStaleIdentityConflict(err)
		default:
			// The refusal must come before any cancellation: a handler still
			// draining the canceled Resume means the stale request aborted the
			// Resume it could no longer address.
			active.Complete(nil)
			<-completed
			t.Fatal("stale force stop canceled the in-flight Resume before refusing the identity conflict")
		}
		if active.Context().Err() != nil {
			t.Fatal("stale force stop canceled the in-flight Resume before refusing the identity conflict")
		}
		active.Complete(nil)
	})
}

// TestForceStopRevalidatesIdentityUnderFenceBeforeCancelingResume pins the
// atomicity contract RoboRev found: the caller-rendered identity validation at
// the top of forceStopThread is not atomic with the admission fence and
// cancelActiveResumes, so a replacement claim landing between them used to be
// detected only by the post-cancellation reread — after the stale request had
// already aborted the replacement Resume it could no longer address. The
// identity must be revalidated under the admission fence and alias
// reservations, before any in-flight Resume is canceled.
func TestForceStopRevalidatesIdentityUnderFenceBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		entry := rendezvous.Entry{
			PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, entry)
		expected := daemonIdentity(entry)
		replacement := entry
		replacement.PID = 4302
		replacement.StartedAt = entry.StartedAt.Add(time.Second)
		locks := hubcore.NewResumeLocks()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// The replacement claim lands after the pre-fence validation but before
		// the cancellation: the first under-reservation deletion check swaps the
		// addressed marker for the replacement's. The replacement Resume itself
		// registered while the request verified the process — the window the
		// post-fence drain exists to close.
		swapped := false
		original := deletionTargetState
		deletionTargetState = func(_ *hubcore.DeletionStore, ref, _ string) (hubcore.DeletionState, bool) {
			if ref == "" && !swapped {
				swapped = true
				if err := rendezvous.Remove(runDir, entry.PID); err != nil {
					t.Error(err)
				}
				if _, err := rendezvous.Write(runDir, replacement); err != nil {
					t.Error(err)
				}
			}
			return "", false
		}
		defer func() { deletionTargetState = original }()
		var active *hubcore.ActiveResume
		var events []string
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			events = append(events, "open")
			registered, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
			if err != nil {
				return nil, err
			}
			active = registered
			return &forceStopProcess{events: &events}, nil
		})}
		completed := make(chan error, 1)
		go func() {
			completed <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &expected}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-completed:
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("replacement in the validation gap error = %v, want conflict", err)
			}
		default:
			// A handler still draining the canceled replacement means the stale
			// request aborted the Resume it could no longer address.
			if active != nil {
				active.Complete(nil)
			}
			<-completed
			t.Fatal("stale force stop canceled the replacement Resume before refusing the identity conflict")
		}
		if active == nil {
			t.Fatal("replacement Resume was not registered during process verification")
		}
		if active.Context().Err() != nil {
			t.Fatal("stale force stop canceled the replacement Resume before refusing the identity conflict")
		}
		active.Complete(nil)
		if slices.Contains(events, "kill") {
			t.Fatalf("stale force stop killed a process: %v", events)
		}
	})
}

// TestForceStopFenceRefusalLeavesAdmissionEpochsUnchanged pins the other half
// of the same Medium finding on the main force-stop path: BeginForceStop
// advances the recovery admission epochs before the under-fence identity
// recheck, and a stale request refused by a replacement claim used to leave
// them advanced. The replacement Resume the refusal deliberately preserved
// was admitted under the pre-fence epoch and could no longer complete its
// recovery clear. A refusal that canceled nothing must restore the epochs its
// fence advanced.
func TestForceStopFenceRefusalLeavesAdmissionEpochsUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		entry := rendezvous.Entry{
			PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, entry)
		expected := daemonIdentity(entry)
		replacement := entry
		replacement.PID = 4302
		replacement.StartedAt = entry.StartedAt.Add(time.Second)
		locks := hubcore.NewResumeLocks()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// The replacement claim lands after the pre-fence validation but before
		// the cancellation: the first under-reservation deletion check swaps the
		// addressed marker for the replacement's. The replacement Resume itself
		// registered while the request verified the process.
		swapped := false
		original := deletionTargetState
		deletionTargetState = func(_ *hubcore.DeletionStore, ref, _ string) (hubcore.DeletionState, bool) {
			if ref == "" && !swapped {
				swapped = true
				if err := rendezvous.Remove(runDir, entry.PID); err != nil {
					t.Error(err)
				}
				if _, err := rendezvous.Write(runDir, replacement); err != nil {
					t.Error(err)
				}
			}
			return "", false
		}
		defer func() { deletionTargetState = original }()
		var active *hubcore.ActiveResume
		var events []string
		resumeEpoch := uint64(0)
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store, DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			events = append(events, "open")
			registered, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
			if err != nil {
				return nil, err
			}
			active = registered
			resumeEpoch = locks.RecoveryState(sessionID).Epoch
			return &forceStopProcess{events: &events}, nil
		})}
		completed := make(chan error, 1)
		// A connection established before the fence captured this sequence; a
		// refusal that canceled nothing must not leave it stale.
		connection := locks.RecoverySequence()
		go func() {
			completed <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &expected}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-completed:
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("replacement in the validation gap error = %v, want conflict", err)
			}
		default:
			if active != nil {
				active.Complete(nil)
			}
			<-completed
			t.Fatal("stale force stop canceled the replacement Resume before refusing the identity conflict")
		}
		if active == nil {
			t.Fatal("replacement Resume was not registered during process verification")
		}
		if active.Context().Err() != nil {
			t.Fatal("stale force stop canceled the replacement Resume before refusing the identity conflict")
		}
		active.Complete(nil)
		if got := locks.RecoveryState(sessionID).Epoch; got != resumeEpoch {
			t.Fatalf("refused force stop left the recovery admission epoch advanced: got %d, want %d", got, resumeEpoch)
		}
		if got := locks.RecoveryState(sessionID).LastRecoverySequence; got > connection {
			t.Fatalf("refused force stop left the connection-level sequence advanced: got %d, connection captured %d", got, connection)
		}
		if slices.Contains(events, "kill") {
			t.Fatalf("stale force stop killed a process: %v", events)
		}
	})
}
