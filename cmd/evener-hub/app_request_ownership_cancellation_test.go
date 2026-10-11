package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// Dispatch through the registered RPC route, not Client.Request: a canceled
// client can return while the server handler remains parked behind Resume.
func TestHubRPCCanceledActionDoesNotWaitForExplicitResume(t *testing.T) {
	for _, method := range []string{appwire.MethodGoalSet, appwire.MethodThreadShutdown} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				id := hubtest.SessionID(t)
				locks := hubcore.NewResumeLocks()
				entered := make(chan hubcore.ResumeRequest, 1)
				release := make(chan struct{})
				releaseResume := sync.OnceFunc(func() { close(release) })
				defer releaseResume()
				cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks,
					Spawner: &fakeRPCSpawner{resume: func(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
						entered <- req
						<-release
						return rendezvous.Entry{}, errors.New("fixture restore released")
					}},
				}
				// Keep the real daemon source. Its discovery callback is the first
				// external lookup an admitted action could perform.
				sourceCalls := 0
				sources := appsource.NewRegistry()
				sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry {
					sourceCalls++
					return nil
				}, nil))
				server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
				registerThreadHandlers(server, cfg, sources, hubRelayFunctions{}, nil)
				params, err := json.Marshal(map[string]string{"ref": "local:" + id, "text": "fixture goal"})
				if err != nil {
					t.Fatal(err)
				}
				resumed := make(chan error, 1)
				var lifecycleLog bytes.Buffer
				resumeCtx, _ := withThreadLifecycleLog(t.Context(), "resume", id, &lifecycleLog)
				go func() {
					_, err := server.Router().Dispatch(resumeCtx, appwire.Request{Method: appwire.MethodThreadResume, Params: params})
					resumed <- err
				}()
				req := <-entered
				if !req.CompletionOwned || req.ActiveResume == nil {
					t.Fatal("fixture did not enter a completion-owned explicit Resume")
				}
				if locks.For(id).TryLock() {
					locks.For(id).Unlock()
					t.Fatal("Resume did not retain alias ownership at the spawner")
				}
				before := locks.RecoveryState(id)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				completed := make(chan error, 1)
				go func() {
					_, err := server.Router().Dispatch(ctx, appwire.Request{Method: method, Params: params})
					completed <- err
				}()
				synctest.Wait()
				select {
				case err := <-completed:
					t.Fatalf("action did not wait for Resume ownership before cancellation: %v", err)
				default:
				}
				cancel()
				synctest.Wait()
				select {
				case err := <-completed:
					if err == nil {
						t.Error("canceled action reported success")
					}
				default:
					t.Error("canceled RPC handler still waits for explicit Resume ownership")
				}
				if sourceCalls != 0 {
					t.Errorf("canceled action reached daemon source: calls=%d", sourceCalls)
				}
				if req.ActiveResume.Context().Err() != nil || !locks.HasActiveResume([]string{id}) {
					t.Error("canceling the independent action canceled or released Resume")
				}
				if locks.For(id).TryLock() {
					locks.For(id).Unlock()
					t.Error("canceling the independent action released Resume's alias")
				}
				if after := locks.RecoveryState(id); after != before {
					t.Errorf("ordinary action changed recovery authority: before=%+v after=%+v", before, after)
				}
				releaseResume()
				<-resumed
				synctest.Wait()
				records := assertThreadLifecycleRecords(t, lifecycleLog.String())
				for _, record := range records {
					if record["state"] != "complete" {
						continue
					}
					result, class := "success", "none"
					if record["stage"] == "request" || record["stage"] == "spawner_resume" {
						result, class = "error", "failed"
					}
					assertThreadLifecycleOutcome(t, records, record["stage"], result, class)
				}
			})
		})
	}
}

func TestHubRPCForkCanceledAliasWaitReleasesAcquiredPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		alias, current := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
		stateDir, runDir := t.TempDir(), t.TempDir()
		buildRPCSessionWithWorkingDir(t, stateDir, current, t.TempDir())
		writeRendezvous(t, runDir, rendezvous.Entry{
			PID: os.Getpid(), SourceID: "local", ThreadID: current, SessionID: current,
			WorkspaceRef: "local:" + alias, StateDir: stateDir,
			Protocol: appwire.ProtocolVersion, StartedAt: time.Now(),
		})
		roster := hubcore.NewRoster(runDir, fakeProber{sessionID: current, status: appwire.ThreadStatusIdle})
		roster.Refresh()
		locks := hubcore.NewResumeLocks()
		blocked := locks.For(current)
		blocked.Lock()
		release := sync.OnceFunc(blocked.Unlock)
		defer release()
		spawns := 0
		cfg := hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Roster: roster, ResumeLocks: locks,
			Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				spawns++
				return rendezvous.Entry{}, errors.New("unexpected fork spawn")
			}},
		}
		server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
		registerThreadHandlers(server, cfg, newHubSourceRegistry(cfg), hubRelayFunctions{}, nil)
		params, err := json.Marshal(appwire.ThreadForkParams{Ref: "local:" + alias, SourceItemKey: "apptranscript-item-v2:t_1:0:0", EditedInput: "forked input"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		go func() {
			_, err := server.Router().Dispatch(ctx, appwire.Request{Method: appwire.MethodThreadFork, Params: params})
			completed <- err
		}()
		synctest.Wait()
		if locks.For(alias).TryLock() {
			locks.For(alias).Unlock()
			t.Fatal("fork did not acquire the earlier alias before blocking")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-completed:
			if err == nil {
				t.Error("canceled fork reported success")
			}
		default:
			t.Error("canceled fork still waits for the later alias")
		}
		if !locks.For(alias).TryLock() {
			t.Error("canceled fork retained its acquired prefix")
		} else {
			locks.For(alias).Unlock()
		}
		if blocked.TryLock() {
			blocked.Unlock()
			t.Error("canceled fork released another owner's alias")
		}
		if spawns != 0 {
			t.Errorf("canceled fork launched a child: %d", spawns)
		}
		release()
		synctest.Wait()
		metas, err := schema.ListSessionMetas(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(metas) != 1 || metas[0].ID != current {
			t.Errorf("canceled fork wrote a new session: %+v", metas)
		}
	})
}

func TestHubRPCProjectDeleteCanceledAliasWaitReleasesAcquiredPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, workDir := t.TempDir(), t.TempDir()
		project, err := identifier.ResolveProject(workDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		first, second := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
		buildRPCSessionWithWorkingDir(t, stateDir, first, workDir)
		buildRPCSessionWithWorkingDir(t, stateDir, second, workDir)
		locks := hubcore.NewResumeLocks()
		web := NewWebServer(hubcore.WebConfig{
			StateDir: root, HubStateRoot: t.TempDir(), LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(),
			Past: hubcore.NewPastIndex(filepath.Join(root, "projects", "*")), ResumeLocks: locks,
			CredsStore: newTestCredentialsStore(t),
		})
		// Begin only after construction: this is request-driven retry, not
		// startup's intentionally background cleanup.
		if _, err := web.cfg.DeletionStore.Begin(project.ID, []hubcore.DeletionTarget{
			{Ref: "local:" + first, ThreadID: first}, {Ref: "local:" + second, ThreadID: second},
		}); err != nil {
			t.Fatal(err)
		}
		blocked := locks.For(second)
		blocked.Lock()
		release := sync.OnceFunc(blocked.Unlock)
		defer release()
		params, err := json.Marshal(appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: workDir})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			response any
			err      error
		}
		completed := make(chan result, 1)
		go func() {
			response, err := web.appRPC.Router().Dispatch(ctx, appwire.Request{Method: appwire.MethodEvenerProjectDelete, Params: params})
			completed <- result{response, err}
		}()
		synctest.Wait()
		if locks.For(first).TryLock() {
			locks.For(first).Unlock()
			t.Fatal("project deletion did not acquire the earlier target")
		}
		cancel()
		synctest.Wait()
		select {
		case got := <-completed:
			if got.err == nil {
				response, ok := got.response.(appwire.ProjectDeleteResponse)
				if !ok || len(response.Deleted) != 0 || len(response.Skipped) == 0 {
					t.Errorf("canceled deletion did not report its refusal: %+v", got.response)
				}
			}
		default:
			t.Error("canceled project deletion still waits for the later target")
		}
		if !locks.For(first).TryLock() {
			t.Error("canceled project deletion retained its acquired prefix")
		} else {
			locks.For(first).Unlock()
		}
		if blocked.TryLock() {
			blocked.Unlock()
			t.Error("canceled deletion released another owner's alias")
		}
		owner, err := llm.NewSessionAPILogger(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if err := owner.ReserveSession(first); err != nil {
			t.Errorf("canceled deletion retained its earlier API-log reservation: %v", err)
		}
		_ = owner.Close()
		metas, err := schema.ListSessionMetas(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(metas) != 2 {
			t.Errorf("canceled deletion removed session artifacts: %+v", metas)
		}
		for _, id := range []string{first, second} {
			if state, ok := web.cfg.DeletionStore.TargetState("local:"+id, id); !ok || state != hubcore.DeletionStateDeleting {
				t.Errorf("canceled deletion advanced target %s: %s, %v", id, state, ok)
			}
		}
		release()
		synctest.Wait()
	})
}

// TestHubRPCProjectDeleteRetryCanceledPropagatesCancellation pins the retry
// boundary of Medium 1: a request canceled while acquiring a later target on
// the existing-deletion resume path must propagate the cancellation unchanged.
// A successful response that merely lists a skipped target reports the resume
// as done when it never ran.
func TestHubRPCProjectDeleteRetryCanceledPropagatesCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, workDir := t.TempDir(), t.TempDir()
		project, err := identifier.ResolveProject(workDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		first, second := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
		buildRPCSessionWithWorkingDir(t, stateDir, first, workDir)
		buildRPCSessionWithWorkingDir(t, stateDir, second, workDir)
		locks := hubcore.NewResumeLocks()
		web := NewWebServer(hubcore.WebConfig{
			StateDir: root, HubStateRoot: t.TempDir(), LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(),
			Past: hubcore.NewPastIndex(filepath.Join(root, "projects", "*")), ResumeLocks: locks,
			CredsStore: newTestCredentialsStore(t),
		})
		// A committed record selects the existing-deletion resume path.
		if _, err := web.cfg.DeletionStore.Begin(project.ID, []hubcore.DeletionTarget{
			{Ref: "local:" + first, ThreadID: first}, {Ref: "local:" + second, ThreadID: second},
		}); err != nil {
			t.Fatal(err)
		}
		if _, ok := web.cfg.DeletionStore.DeletingProject(project.ID); !ok {
			t.Fatal("fixture did not commit a deletion record for the retry path")
		}
		blocked := locks.For(second)
		blocked.Lock()
		release := sync.OnceFunc(blocked.Unlock)
		defer release()
		params, err := json.Marshal(appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: workDir})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		type result struct {
			response any
			err      error
		}
		completed := make(chan result, 1)
		go func() {
			response, err := web.appRPC.Router().Dispatch(ctx, appwire.Request{Method: appwire.MethodEvenerProjectDelete, Params: params})
			completed <- result{response, err}
		}()
		synctest.Wait()
		if locks.For(first).TryLock() {
			locks.For(first).Unlock()
			t.Fatal("retry deletion did not acquire the earlier target before waiting for the later one")
		}
		cancel()
		synctest.Wait()
		select {
		case got := <-completed:
			if !errors.Is(got.err, context.Canceled) {
				t.Fatalf("canceled retry deletion error = %v, want context.Canceled (response %+v)", got.err, got.response)
			}
		default:
			t.Error("canceled retry deletion still waits for the later target")
		}
		release()
		synctest.Wait()
	})
}

// TestProjectDeleteRetryCanceledAfterOwnershipReportsError pins the retry
// path's post-acquire cancellation gap: once every ownership reservation is
// held, a request abandoned before it could decide must fail before
// cleanupProjectDeletion removes session artifacts, exactly as the fresh path
// and sessionDelete already do.
func TestProjectDeleteRetryCanceledAfterOwnershipReportsError(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	project, err := identifier.ResolveProject(workDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	id := projectDeleteCanonicalSessionIDs[0]
	buildRPCSessionWithWorkingDir(t, stateDir, id, workDir)
	locks := hubcore.NewResumeLocks()
	web := NewWebServer(hubcore.WebConfig{
		StateDir: root, HubStateRoot: t.TempDir(), LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(),
		Past: hubcore.NewPastIndex(filepath.Join(root, "projects", "*")), ResumeLocks: locks,
		CredsStore: newTestCredentialsStore(t),
	})
	// A committed record selects the existing-deletion resume path.
	if _, err := web.cfg.DeletionStore.Begin(project.ID, []hubcore.DeletionTarget{
		{Ref: "local:" + id, ThreadID: id},
	}); err != nil {
		t.Fatal(err)
	}
	// The acquisition's own post-lock check sees an uncanceled request; the next
	// check sees it canceled, modeling a request abandoned while it acquired.
	ctx := &cancelAfterAcquireContext{Context: t.Context(), done: make(chan struct{})}
	if _, err := web.projectDelete(ctx, appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: workDir}); err == nil {
		t.Fatal("canceled post-acquisition retry deletion reported success")
	}
	if _, statErr := os.Stat(filepath.Join(stateDir, "sessions", id+".meta.json")); statErr != nil {
		t.Fatalf("canceled retry deletion removed saved data: %v", statErr)
	}
}
