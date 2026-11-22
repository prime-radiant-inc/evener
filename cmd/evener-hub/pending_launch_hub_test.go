package hub

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func pendingLaunchFixture(t *testing.T) (cfg hubcore.WebConfig, sources *appsource.Registry, sessionID, runDir string) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID = buildRPCParentSessionWithWorkingDir(t, stateDir, t.TempDir())
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	runDir = t.TempDir()
	source := &scriptedAppSource{id: "local", thread: appwire.Thread{
		ID: sessionID, SessionID: sessionID, Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID},
	}}
	sources = appsource.NewRegistry()
	sources.Add(source)
	cfg = hubcore.WebConfig{RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil), Past: past}
	return cfg, sources, sessionID, runDir
}

// A caller that walks away mid-restore gets the still-starting classification,
// and a reconnecting caller joins the SAME claimed daemon instead of spawning a
// second one. The claim itself outlives the departing caller.
func TestResumeDisconnectKeepsRestoringDaemonForReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, sources, sessionID, runDir := pendingLaunchFixture(t)
		locks := hubcore.NewResumeLocks()
		defer locks.Close()
		cfg.ResumeLocks = locks
		var calls atomic.Int32
		launched := make(chan *hubcore.PendingLaunch, 1)
		cfg.Spawner = &fakeRPCSpawner{resume: func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			calls.Add(1)
			launched <- req.PendingLaunch
			<-ctx.Done()
			if req.PendingLaunch != nil {
				req.PendingLaunch.HandOff()
			}
			return rendezvous.Entry{}, errResumeStillStarting
		}}
		params := appwire.ThreadResumeParams{Ref: "local:" + sessionID}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		first := make(chan error, 1)
		go func() {
			_, err := hubThreadResume(ctx, cfg, sources, params)
			first <- err
		}()
		synctest.Wait()
		claim := <-launched
		if claim == nil {
			cancel()
			t.Fatal("resume did not carry a pending-launch claim to the spawner")
		}
		cancel()
		synctest.Wait()
		departureErr := <-first
		var wire appwire.WireError
		if !errors.As(departureErr, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("departing caller error = %v, want Unavailable", departureErr)
		}
		if data, _ := appwire.DecodedErrorData(wire.Data); data.EvenerErrorInfo != appwire.ErrorHubLaunch {
			t.Fatalf("departing caller info = %q, want hubLaunch", data.EvenerErrorInfo)
		}
		if _, ok := locks.PendingLaunch(sessionID); !ok {
			t.Fatal("claim was dropped with the departing caller")
		}

		entry := rendezvous.Entry{PID: 4242, Protocol: appwire.ProtocolVersion, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()}
		type result struct {
			response appwire.ThreadResumeResponse
			err      error
		}
		reconnect := make(chan result, 1)
		go func() {
			response, err := hubThreadResume(t.Context(), cfg, sources, params)
			reconnect <- result{response, err}
		}()
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("reconnect spawned a duplicate daemon: resume calls = %d, want 1", got)
		}
		writeRendezvous(t, runDir, entry)
		claim.Settle(entry, nil)
		got := <-reconnect
		if got.err != nil || got.response.Thread.ID != sessionID {
			t.Fatalf("reconnect = %+v, %v", got.response, got.err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("reconnect resume calls = %d, want 1", got)
		}
	})
}

// Two concurrent resumes that arrive while a launch is in flight both join it:
// neither reaches the spawner, and both read the daemon once it registers.
func TestPendingLaunchDeduplicatesConcurrentResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, sources, sessionID, runDir := pendingLaunchFixture(t)
		locks := hubcore.NewResumeLocks()
		defer locks.Close()
		cfg.ResumeLocks = locks
		var calls atomic.Int32
		cfg.Spawner = &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			calls.Add(1)
			return rendezvous.Entry{}, errors.New("duplicate spawn")
		}}
		claim, created := locks.ClaimPendingLaunch(sessionID, time.Minute)
		if !created {
			t.Fatal("claim was not created")
		}
		params := appwire.ThreadResumeParams{Ref: "local:" + sessionID}
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := hubThreadResume(t.Context(), cfg, sources, params)
				results <- err
			}()
		}
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("resumes spawned while a launch was claimed: calls = %d", got)
		}
		entry := rendezvous.Entry{PID: 4242, Protocol: appwire.ProtocolVersion, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()}
		writeRendezvous(t, runDir, entry)
		claim.Settle(entry, nil)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("joined resume = %v", err)
			}
		}
		if got := calls.Load(); got != 0 {
			t.Fatalf("joined resumes reached the spawner: calls = %d", got)
		}
	})
}
