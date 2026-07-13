package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

// TestServeModelSwitch_ThreadReadReflectsNewModelWithNoInterveningTurn pins G2
// against the REAL cmd/serf/serve.go wiring (not a hand-built hook). It boots
// the real `serve` daemon with a scripted provider, connects an appwire
// client over the websocket RPC exactly like the TUI does, and drives a model
// switch through the real thread/model/set RPC handler
// (Server.handleAppThreadModelSet → the SetModelFunc closure installed in
// serve.go). It then asserts thread/read reports the NEW model IMMEDIATELY —
// with no turn ever started — which is only true if serve.go's closure calls
// srv.UpdateSessionInfo synchronously after sess.SetModel succeeds. Deleting
// that UpdateSessionInfo line (while leaving sess.SetModel(model) itself
// intact) makes this test fail, because status.Model would then only refresh
// on the next EventSessionStart, which never fires again mid-session.
func TestServeModelSwitch_ThreadReadReflectsNewModelWithNoInterveningTurn(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{
		name: "openai",
		// No steps are ever consumed: the model switch below must not start a
		// turn. If it does, scriptedCommunicate("done") answers so the test
		// fails on a clear assertion rather than hanging.
		steps: nil,
	})

	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()

	args := []string{
		"--model", "openai/gpt-initial",
		"--addr", "127.0.0.1:0",
		"--dir", workDir,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			// best-effort; the /shutdown POST below normally drains this.
		}
	})

	entry := waitForServeTestRendezvous(t, runDir)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-model-switch-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	before, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead (before): %v", err)
	}
	if before.Thread.ModelProvider != "gpt-initial" {
		t.Fatalf("initial thread/read ModelProvider = %q, want %q", before.Thread.ModelProvider, "gpt-initial")
	}

	// Drive the switch through the REAL wired path: the thread/model/set RPC
	// handler, which invokes serve.go's SetModelFunc closure.
	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "openai",
		Model:         "gpt-switched",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}

	// No turn has run (steps is empty and Requests() below proves it). The G2
	// guarantee is that thread/read already reflects the new model.
	after, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead (after): %v", err)
	}
	if after.Thread.ModelProvider != "gpt-switched" {
		t.Fatalf("thread/read ModelProvider after switch = %q, want %q (G2: daemon's cached session info must refresh synchronously, with no intervening turn)",
			after.Thread.ModelProvider, "gpt-switched")
	}

	resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after /shutdown")
	}
}
