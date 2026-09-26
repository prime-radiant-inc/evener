package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestServe_FailedTurnReportsSystemErrorAcrossRestart drives the real daemon:
// a turn the provider fails leaves thread/read reporting systemError, and a
// daemon resumed on that session reports systemError on its very FIRST read
// (serve.go publishes RestingWireState synchronously before the bridge drains
// SessionStart, the #251 race), never idle first.
func TestServe_FailedTurnReportsSystemErrorAcrossRestart(t *testing.T) {
	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
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

	firstArgs := []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
	done1 := make(chan error, 1)
	go func() { done1 <- runServe(firstArgs) }()
	entry1 := waitForServeTestRendezvous(t, runDir)

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry1.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serve-failed-turn-test", Version: "test"}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry1.SessionID}.String()
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "failed-turn",
		ExpectedInstanceID: entry1.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "do the thing"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}
	pollServeAskStatusUntil(t, entry1.Address, appwire.ThreadStatusSystemError, 10*time.Second, 100*time.Millisecond)
	client.Close()

	if err := shutdownServeTestDaemon(context.Background(), entry1.Address, entry1.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done1:
		if err != nil {
			t.Fatalf("first runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first runServe did not exit after thread/shutdown")
	}

	secondArgs := []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--resume", entry1.SessionID, "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir}
	done2 := make(chan error, 1)
	go func() { done2 <- runServe(secondArgs) }()
	entry2 := waitForServeTestRendezvous(t, runDir)
	if first := waitForServeAskStatusUp(t, entry2.Address, 10*time.Second); first.State != appwire.ThreadStatusSystemError {
		t.Fatalf("first thread/read after restore = %q, want %q", first.State, appwire.ThreadStatusSystemError)
	}
	if err := shutdownServeTestDaemon(context.Background(), entry2.Address, entry2.SessionID); err != nil {
		t.Fatalf("thread/shutdown (second daemon): %v", err)
	}
	select {
	case err := <-done2:
		if err != nil {
			t.Fatalf("second runServe returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second runServe did not exit after thread/shutdown")
	}
}
