package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

func TestWorkspaceIdentityClearKeepsReferenceAndChangesInstance(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	args = append(args, "--manager")
	var gotRef string
	obs := runClearAttempt(t, deps, state, args, func(obs *clearObservation) {
		read := clearThreadRead(t, state.srv)
		gotRef = read.Thread.Evener.Ref
		if !state.session(0).Meta().Manager || !state.session(1).Meta().Manager {
			t.Errorf("Manager = initial:%t cleared:%t, want true for both", state.session(0).Meta().Manager, state.session(1).Meta().Manager)
		}
	})
	if obs.clearErr != nil {
		t.Fatalf("clear: %v", obs.clearErr)
	}
	wantRef := "local:" + obs.oldSessionID
	if obs.newSessionID == obs.oldSessionID {
		t.Fatalf("clear instance ID did not change: %q", obs.newSessionID)
	}
	if gotRef != wantRef {
		t.Fatalf("thread ref after Clear = %q, want stable %q", gotRef, wantRef)
	}
	meta, err := schema.LoadSessionMeta(args[7], obs.newSessionID)
	if err != nil {
		t.Fatalf("load cleared metadata: %v", err)
	}
	if !meta.Manager {
		t.Fatal("cleared session metadata lost Manager=true")
	}

	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	var cancel context.CancelFunc
	resume := defaultServeDeps()
	resume.ensureConfigDirs = func() error { return nil }
	resume.seedMarketplaces = func(context.Context) error { return nil }
	resume.bridge = deps.bridge
	resume.newServer = func(cfg server.ServerConfig) serveServer {
		state.srv = &clearIdentityServer{Server: server.NewServer(cfg), state: state}
		return state.srv
	}
	resume.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel = context.WithCancel(ctx)
		return ctx, cancel
	}
	preparedRef := ""
	var restartedPublicRef string
	prepare := resume.prepareAppIdentity
	resume.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		preparedRef = ref
		if threadID != obs.newSessionID {
			t.Errorf("restart instance ID = %q, want %q", threadID, obs.newSessionID)
		}
		return prepare(sourceID, threadID, ref, transcriptPath)
	}
	resume.serveHTTP = func(*http.Server, net.Listener) error {
		read := clearThreadRead(t, state.srv)
		restartedPublicRef = read.Thread.Evener.Ref
		if read.Thread.ID != obs.newSessionID {
			t.Errorf("restarted public instance ID = %q, want %q", read.Thread.ID, obs.newSessionID)
		}
		if !read.Thread.Evener.Manager {
			t.Error("restarted public projection lost Manager=true")
		}
		cancel()
		return http.ErrServerClosed
	}
	resumeArgs := []string{
		"--model", "openai/gpt-test", "--addr", "127.0.0.1:0",
		"--resume", obs.newSessionID, "--dir", args[5],
		"--state-dir", args[7], "--run-dir", args[9],
	}
	if err := runServeWithDeps(resumeArgs, resume); err != nil {
		t.Fatalf("restart cleared session: %v", err)
	}
	newRef := "local:" + obs.newSessionID
	if preparedRef != newRef {
		t.Fatalf("restart prepared boot ref = %q, want current session ref %q", preparedRef, newRef)
	}
	if restartedPublicRef != newRef {
		t.Fatalf("restarted public ref = %q, want current session ref %q", restartedPublicRef, newRef)
	}
	meta, err = schema.LoadSessionMeta(args[7], obs.newSessionID)
	if err != nil {
		t.Fatalf("load restarted metadata: %v", err)
	}
	if !meta.Manager {
		t.Fatal("restarted session metadata lost Manager=true")
	}
}

func TestLegacyMetadataResumePreparesCurrentSessionReference(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedResumableSession(t, stateDir, sessionID, sessionID)

	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	var cancel context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel = context.WithCancel(ctx)
		return ctx, cancel
	}
	preparedRef := ""
	prepare := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		preparedRef = ref
		return prepare(sourceID, threadID, ref, transcriptPath)
	}
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		cancel()
		return http.ErrServerClosed
	}
	args := []string{
		"--model", "openai/gpt-test", "--addr", "127.0.0.1:0",
		"--resume", sessionID, "--dir", t.TempDir(),
		"--state-dir", stateDir, "--run-dir", t.TempDir(),
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("resume old-format session: %v", err)
	}
	if want := "local:" + sessionID; preparedRef != want {
		t.Fatalf("prepared session ref = %q, want %q", preparedRef, want)
	}
}

func TestLegacyClearKeepsLiveAliasAndRestartUsesCurrentSessionRef(t *testing.T) {
	stateDir := t.TempDir()
	const originalSessionID = "02wMz5Txv1C3Hut0M8GCeB"
	seedResumableSession(t, stateDir, originalSessionID, originalSessionID)
	deps, state, baseArgs := newClearServeDeps(t)
	baseArgs[7] = stateDir
	args := append(append([]string(nil), baseArgs...), "--resume", originalSessionID)
	var initialPreparedRef string
	prepare := deps.prepareAppIdentity
	deps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		if threadID == originalSessionID {
			initialPreparedRef = ref
		}
		return prepare(sourceID, threadID, ref, transcriptPath)
	}

	var initialPublicRef, clearedPublicRef string
	var oldInstanceID, replacementID string
	var clearErr error
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		oldInstanceID = originalSessionID
		initialRead := clearThreadRead(t, state.srv)
		initialPublicRef = initialRead.Thread.Evener.Ref
		if initialRead.Thread.ID != originalSessionID {
			t.Errorf("initial public instance ID = %q, want restored ID %q", initialRead.Thread.ID, originalSessionID)
		}
		if initialRead.Thread.Evener.Manager {
			t.Error("legacy non-Manager session projected Manager=true")
		}
		clearErr = state.srv.clear(context.Background(), appwire.ThreadClearParams{
			Ref:                initialPublicRef,
			ClientMutationID:   "legacy-clear",
			ExpectedInstanceID: oldInstanceID,
		})
		if clearErr == nil {
			replacement := state.session(0)
			replacementID = replacement.ID()
			cleared := clearThreadRead(t, state.srv)
			clearedPublicRef = cleared.Thread.Evener.Ref
			if cleared.Thread.Evener.Manager {
				t.Error("legacy Clear projection changed non-Manager session to Manager")
			}
		}
		state.srv.shutdown()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("serve legacy session and Clear: %v", err)
	}
	if clearErr != nil {
		t.Fatalf("Clear legacy session: %v", clearErr)
	}
	wantRef := "local:" + originalSessionID
	if initialPreparedRef != wantRef || initialPublicRef != wantRef {
		t.Fatalf("initial prepared/public refs = %q/%q, want %q", initialPreparedRef, initialPublicRef, wantRef)
	}
	if replacementID == "" || replacementID == oldInstanceID {
		t.Fatalf("Clear instance IDs = %q then %q, want a distinct replacement", oldInstanceID, replacementID)
	}
	if clearedPublicRef != wantRef {
		t.Fatalf("public ref after Clear = %q, want %q", clearedPublicRef, wantRef)
	}
	replacementMeta, err := schema.LoadSessionMeta(stateDir, replacementID)
	if err != nil {
		t.Fatalf("load replacement metadata: %v", err)
	}
	if replacementMeta.ID != replacementID {
		t.Fatalf("replacement metadata ID = %q, want %q", replacementMeta.ID, replacementID)
	}

	restartDeps, restartState, _ := newClearServeDeps(t)
	restartArgs := []string{
		"--model", "openai/gpt-test", "--addr", "127.0.0.1:0",
		"--resume", replacementID, "--dir", baseArgs[5],
		"--state-dir", stateDir, "--run-dir", baseArgs[9],
	}
	var restartPreparedRef, restartPublicRef, restartPublicInstanceID string
	restartPrepare := restartDeps.prepareAppIdentity
	restartDeps.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		restartPreparedRef = ref
		return restartPrepare(sourceID, threadID, ref, transcriptPath)
	}
	restartDeps.serveHTTP = func(*http.Server, net.Listener) error {
		read := clearThreadRead(t, restartState.srv)
		restartPublicRef = read.Thread.Evener.Ref
		restartPublicInstanceID = read.Thread.ID
		if read.Thread.Evener.Manager {
			t.Error("restarted legacy projection changed non-Manager session to Manager")
		}
		restartState.srv.shutdown()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(restartArgs, restartDeps); err != nil {
		t.Fatalf("restart cleared legacy session: %v", err)
	}
	newRef := "local:" + replacementID
	if restartPreparedRef != newRef || restartPublicRef != newRef {
		t.Fatalf("restarted prepared/public refs = %q/%q, want current session ref %q", restartPreparedRef, restartPublicRef, newRef)
	}
	if restartPublicInstanceID != replacementID {
		t.Fatalf("restarted public instance ID = %q, want replacement %q", restartPublicInstanceID, replacementID)
	}
}
