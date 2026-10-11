package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A served root routes the direct subagent stop to its own session's delegate
// tree (S6): an id the tree does not hold is not found.
func TestServeDelegateStopReachesTheSessionsTree(t *testing.T) {
	workDir, stateDir, runDir := t.TempDir(), t.TempDir(), t.TempDir()
	installServeScriptedProvider(t, &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{}})
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir})
	}()
	entry := waitForServeTestRendezvous(t, runDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "serve-delegate-stop-test", Version: "test"}}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()

	var resp appwire.DelegateStopResponse
	err = client.Request(ctx, appwire.MethodEvenerDelegateStop, appwire.DelegateStopParams{Ref: ref, DelegateID: "dlg_not_in_this_tree"}, &resp)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("stop of an unknown delegate = %+v, %v; want a wire error", resp, err)
	}
	// A client decodes the error's data as plain JSON; read it back as ErrorData.
	raw, _ := json.Marshal(wire.Data)
	var data appwire.ErrorData
	if json.Unmarshal(raw, &data) != nil || data.EvenerErrorInfo != appwire.ErrorResourceNotFound {
		t.Fatalf("stop of an unknown delegate = %v (%+v), want resourceNotFound", err, wire.Data)
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: an idle daemon exits at once after shutdown.
		t.Fatal("runServe did not exit after shutdown")
	}
}
