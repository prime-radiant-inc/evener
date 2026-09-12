package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

type durableRecoveryProcess struct {
	beforeKill   func() error
	waitErr      error
	waitObserved *atomic.Bool
}

func (p *durableRecoveryProcess) Kill() error { return p.beforeKill() }
func (p *durableRecoveryProcess) Wait(context.Context) error {
	p.waitObserved.Store(true)
	return p.waitErr
}
func (*durableRecoveryProcess) Close() error { return nil }

func TestHubRecoveryRequirementSurvivesRecreation(t *testing.T) {
	for _, outcome := range []string{"exited", "wait failed", "already exited", "signal denied", "clear write failed"} {
		t.Run(outcome, func(t *testing.T) {
			var sessionID string
			cfg, id, resumes := parityResumeFixture(t, func(daemon *appserver.Server) {
				thread := func(ref string) appwire.Thread {
					return appwire.Thread{ID: sessionID, SessionID: sessionID, Source: "local", Status: appwire.ThreadStatus{Type: "idle"}, Evener: appwire.EvenerThread{Ref: ref, InstanceID: sessionID, MutationStateAuthoritative: true, Capabilities: appwire.ThreadCapabilities{Send: true}}}
				}
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
					return appwire.ThreadReadResponse{Thread: thread(params.Ref)}, nil
				})
				appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
					return appwire.ThreadListResponse{Data: []appwire.Thread{thread(localAppRef(sessionID))}}, nil
				})
			})
			sessionID = id
			cfg.HubStateRoot = t.TempDir()
			if cfg.ResumeLocks != nil {
				t.Fatal("fixture injected in-memory recovery state")
			}
			ref := "local:" + sessionID
			entry := rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StateDir: t.TempDir(), StartedAt: time.Now()}
			writeRendezvous(t, cfg.RunDir, entry)
			var killObserved, waitObserved atomic.Bool
			cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				if outcome == "already exited" {
					return nil, daemonprocess.ErrExited
				}
				process := &durableRecoveryProcess{waitObserved: &waitObserved, beforeKill: func() error {
					// A separately constructed registry must already fence recovery before
					// the external process signal can take effect.
					recreated := NewWebServer(cfg)
					if state := recreated.cfg.ResumeLocks.RecoveryState(sessionID); !state.ResumeRequired || state.ResumeSessionID != sessionID {
						return errors.New("recreated hub lost recovery requirement before Kill")
					}
					killObserved.Store(true)
					if outcome == "signal denied" {
						return errors.New("signal denied")
					}
					return nil
				}}
				if outcome == "wait failed" {
					process.waitErr = context.DeadlineExceeded
				}
				return process, nil
			})
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			err := client.Request(t.Context(), appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: ref}, nil)
			client.Close()
			hub.Close()
			if (err != nil) != (outcome == "wait failed" || outcome == "signal denied") {
				t.Fatalf("force stop outcome=%s error=%v", outcome, err)
			}
			if killObserved.Load() != (outcome != "already exited") || waitObserved.Load() != (outcome != "already exited" && outcome != "signal denied") {
				t.Fatalf("termination was not durably fenced: %v", err)
			}
			if err := rendezvous.Remove(cfg.RunDir, entry.PID); err != nil {
				t.Fatal(err)
			}

			// Recreate from the same disk root, without sharing the WebServer's locks.
			cfg.Roster = hubcore.NewRoster(cfg.RunDir, nil)
			hub = newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client = dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
			if err != nil {
				t.Fatal(err)
			}
			if !read.Thread.Evener.ResumeRequired || read.Thread.Status.Type != "notLoaded" || read.Thread.Evener.Capabilities.Send || len(read.Thread.Turns) == 0 {
				t.Fatalf("recreated recovery snapshot=%+v", read.Thread)
			}
			if err := client.Request(t.Context(), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: ref, Model: "test"}, nil); err == nil {
				t.Fatal("automatic action escaped durable recovery requirement")
			}
			if *resumes != 0 {
				t.Fatalf("automatic resume calls=%d", *resumes)
			}
			if outcome == "clear write failed" {
				restore := obstructRecoveryDirectory(t, cfg.HubStateRoot)
				if _, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref}); err == nil {
					t.Fatal("resume hid durable-clear failure")
				}
				restore()
				client.Close()
				hub.Close()
				cfg.Roster = hubcore.NewRoster(cfg.RunDir, nil)
				hub = newHubRPCTestServer(t, cfg)
				defer hub.Close()
				client = dialHubRPC(t, hub)
				defer client.Close()
				if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
					t.Fatal(err)
				}
				read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
				if err != nil {
					t.Fatal(err)
				}
				if !read.Thread.Evener.ResumeRequired || read.Thread.Evener.Capabilities.Send {
					t.Fatalf("failed clear lost durable authority: %+v", read.Thread)
				}
				if err := client.Request(t.Context(), appwire.MethodThreadModelSet, appwire.ThreadModelSetParams{Ref: ref, Model: "test"}, nil); err == nil {
					t.Fatal("automatic action escaped failed durable clear")
				}
				if *resumes != 1 {
					t.Fatalf("failed-clear automatic launches=%d", *resumes)
				}
			}
			resumed, err := client.ThreadResume(t.Context(), appwire.ThreadResumeParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Thread.Evener.ResumeRequired || *resumes != 1 {
				t.Fatalf("explicit resume=%+v launches=%d", resumed.Thread, *resumes)
			}
			client.Close()
			hub.Close()

			cfg.Roster = hubcore.NewRoster(cfg.RunDir, nil)
			hub = newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client = dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{}); err != nil {
				t.Fatal(err)
			}
			read, err = client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			if read.Thread.Evener.ResumeRequired || !read.Thread.Evener.Capabilities.Send || *resumes != 1 {
				t.Fatalf("completed resume was not durable: thread=%+v launches=%d", read.Thread, *resumes)
			}
		})
	}
}

// Keep the committed snapshot intact while making the next atomic write fail.
func obstructRecoveryDirectory(t *testing.T, root string) func() {
	t.Helper()
	dir := filepath.Join(root, "recovery")
	held := filepath.Join(root, "held-recovery")
	if err := os.Rename(dir, held); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked directory"), 0600); err != nil {
		_ = os.Rename(held, dir)
		t.Fatal(err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if err := os.Remove(dir); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(held, dir); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Cleanup(restore)
	return restore
}
