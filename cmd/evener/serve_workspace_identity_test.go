package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/server"
)

func TestWorkspaceIdentityClearKeepsReferenceAndChangesInstance(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var gotRef, oldWorkspaceID, newWorkspaceID string
	obs := runClearAttempt(t, deps, state, args, func(obs *clearObservation) {
		read := clearThreadRead(t, state.srv)
		gotRef = read.Thread.Evener.Ref
		oldWorkspaceID = state.session(0).WorkspaceID()
		newWorkspaceID = state.session(1).WorkspaceID()
	})
	if obs.clearErr != nil {
		t.Fatalf("clear: %v", obs.clearErr)
	}
	wantRef := "local:" + obs.oldSessionID
	if oldWorkspaceID != obs.oldSessionID {
		t.Fatalf("initial workspace ID = %q, want %q", oldWorkspaceID, obs.oldSessionID)
	}
	if newWorkspaceID != oldWorkspaceID {
		t.Fatalf("cleared workspace ID = %q, want retained %q", newWorkspaceID, oldWorkspaceID)
	}
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
	if meta.WorkspaceID != oldWorkspaceID {
		t.Fatalf("persisted cleared workspace ID = %q, want %q", meta.WorkspaceID, oldWorkspaceID)
	}

	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	var cancel context.CancelFunc
	resume := defaultServeDeps()
	resume.ensureConfigDirs = func() error { return nil }
	resume.seedMarketplaces = func(context.Context) error { return nil }
	resume.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel = context.WithCancel(ctx)
		return ctx, cancel
	}
	preparedRef := ""
	prepare := resume.prepareAppIdentity
	resume.prepareAppIdentity = func(sourceID, threadID, ref, transcriptPath string) (server.PreparedAppIdentity, error) {
		preparedRef = ref
		if threadID != obs.newSessionID {
			t.Errorf("restart instance ID = %q, want %q", threadID, obs.newSessionID)
		}
		return prepare(sourceID, threadID, ref, transcriptPath)
	}
	resume.serveHTTP = func(*http.Server, net.Listener) error {
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
	if preparedRef != wantRef {
		t.Fatalf("restart prepared workspace ref = %q, want %q", preparedRef, wantRef)
	}
}

func TestWorkspaceIdentityLegacyResumePersistsBeforeReferencePreparation(t *testing.T) {
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
		meta, err := schema.LoadSessionMeta(stateDir, sessionID)
		if err != nil {
			t.Fatalf("load metadata before reference publication: %v", err)
		}
		if meta.WorkspaceID != sessionID {
			t.Fatalf("workspace identity at reference preparation = %q, want saved session ID %q", meta.WorkspaceID, sessionID)
		}
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
		t.Fatalf("prepared workspace ref = %q, want %q", preparedRef, want)
	}
}
