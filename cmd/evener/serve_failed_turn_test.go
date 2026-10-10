package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// failTurnOnServeDaemon drives a real daemon through a turn its provider
// fails, waits for thread/read to report systemError, shuts the daemon down,
// and returns the failed session's id for a resume.
func failTurnOnServeDaemon(t *testing.T, workDir, stateDir, runDir string) string {
	t.Helper()
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)
			},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
	done := make(chan error, 1)
	go func() { done <- runServe(args) }()
	entry := waitForServeTestRendezvous(t, runDir)

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serve-failed-turn-test", Version: "test"}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "failed-turn",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	pollServeAskStatusUntil(t, entry.Address, appwire.ThreadStatusSystemError, 10*time.Second, 100*time.Millisecond)
	client.Close()

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("failing runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failing runServe did not exit after thread/shutdown")
	}
	return entry.SessionID
}

func resumeServeArgs(sessionID, workDir, stateDir, runDir string) []string {
	return []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--resume", sessionID, "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
}

// TestServe_FailedTurnReportsSystemErrorAcrossRestart drives the real daemon:
// a turn the provider fails leaves thread/read reporting systemError, and a
// daemon resumed on that session reports systemError on its very first read,
// never idle first.
func TestServe_FailedTurnReportsSystemErrorAcrossRestart(t *testing.T) {
	workDir, stateDir, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	sessionID := failTurnOnServeDaemon(t, workDir, stateDir, runDir)

	done := make(chan error, 1)
	go func() { done <- runServe(resumeServeArgs(sessionID, workDir, stateDir, runDir)) }()
	entry := waitForServeTestRendezvous(t, runDir)
	if first := waitForServeAskStatusUp(t, entry.Address, 10*time.Second); first.State != appwire.ThreadStatusSystemError {
		t.Fatalf("first thread/read after restore = %q, want %q", first.State, appwire.ThreadStatusSystemError)
	}
	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown (resumed daemon): %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("resumed runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resumed runServe did not exit after thread/shutdown")
	}
}

// TestServe_StartupWritePublishesTheRestoredFailure pins serve's synchronous
// startup SetState on its own. The bridge's restored SessionStart publishes
// the same WireState later and would mask a wrong startup write, so the bridge
// never drains here, and the read happens in
// observeCallbacks, which runs after the startup write and before the input
// loop starts: the startup write is the only status there is to read (#251).
// It must be the restored session's WireState, systemError, rather than its
// internal idle.
func TestServe_StartupWritePublishesTheRestoredFailure(t *testing.T) {
	workDir, stateDir, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	sessionID := failTurnOnServeDaemon(t, workDir, stateDir, runDir)

	deps, state, _ := newClearServeDeps(t)
	deps.bridge = func(_ serveServer, _ *agent.Session, _ func(events.SessionEvent), onDrained func()) {
		onDrained()
	}
	var first appwire.Message
	deps.observeCallbacks = func(serveCallbackObserver) {
		conn := state.srv.AppServer().NewConnection("startup-write")
		conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(1), appwire.MethodInitialize,
			appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}))
		first = conn.HandleMessage(context.Background(), appwire.RequestMessage(
			appwire.NewIntID(2), appwire.MethodThreadRead,
			appwire.ThreadReadParams{Ref: "local:" + sessionID}))
	}
	deps.serveHTTP = func(_ *http.Server, listener net.Listener) error {
		// Nothing serves on the listener, so nothing else closes it.
		_ = listener.Close()
		state.srv.shutdown()
		return http.ErrServerClosed
	}
	if err := runServeWithDeps(resumeServeArgs(sessionID, workDir, stateDir, runDir), deps); err != nil {
		t.Fatalf("resumed serve: %v", err)
	}
	if first.Response == nil {
		t.Fatalf("first thread/read failed: %+v", first.Error)
	}
	read, ok := first.Response.Result.(appwire.ThreadReadResponse)
	if !ok {
		t.Fatalf("first thread/read result is %T, want appwire.ThreadReadResponse", first.Response.Result)
	}
	if got := read.Thread.Status.Type; got != appwire.ThreadStatusSystemError {
		t.Fatalf("first thread/read with the bridge held = %q, want %q: the startup write must publish the restored WireState", got, appwire.ThreadStatusSystemError)
	}
}
