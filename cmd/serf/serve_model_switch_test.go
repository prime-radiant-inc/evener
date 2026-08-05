package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func waitServeMilestone(ctx context.Context, milestones <-chan string, want string) error {
	select {
	case got := <-milestones:
		if got != want {
			return fmt.Errorf("structured milestone = %q, want %q", got, want)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for structured milestone %q: %w", want, ctx.Err())
	}
}

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

func TestServeModelSwitch_ProviderFailureRestoresCapability(t *testing.T) {
	kimi := &scriptedProvider{
		name: "kimi-anthropic",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus(
					"kimi-anthropic", http.StatusForbidden,
					"billing-cycle quota exhausted", nil, nil,
				)
			},
		},
	}
	openai := &scriptedProvider{
		name: "openai",
		errorSteps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				return scriptedCommunicate("switched provider recovered"), nil
			},
		},
	}
	installServeScriptedProviders(t, kimi, openai)

	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{
			"--model", "kimi-anthropic/k3",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
			"--no-project-prompts",
		})
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
			if err == nil {
				resp.Body.Close()
			}
		}
	})

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
		ClientInfo: appwire.ClientInfo{Name: "serve-provider-failure-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead subscribe: %v", err)
	}

	milestones := make(chan string, 8)
	var onceMu sync.Mutex
	seen := make(map[string]bool)
	record := func(name string) {
		onceMu.Lock()
		defer onceMu.Unlock()
		if !seen[name] {
			seen[name] = true
			milestones <- name
		}
	}
	go func() {
		for notification := range client.Notifications() {
			switch notification.Method {
			case appwire.NotifyTurnCompleted:
				var params appwire.TurnCompletedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				switch params.Turn.Status {
				case appwire.TurnStatusFailed:
					record("failed turn")
				case appwire.TurnStatusCompleted:
					record("successful turn")
				}
			case appwire.NotifyThreadStatusChanged:
				var params appwire.ThreadStatusChangedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				if params.Status.Type == appwire.ThreadStatusIdle &&
					params.Capabilities != nil && params.Capabilities.ChangeModel {
					record("idle with model capability")
				}
			}
		}
	}()

	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID: "provider-failure-turn",
		Ref:              ref,
		Input:            []appwire.InputItem{{Type: "text", Text: "make the first request"}},
	}); err != nil {
		t.Fatalf("TurnStart (failed provider): %v", err)
	}

	if err := waitServeMilestone(ctx, milestones, "failed turn"); err != nil {
		t.Fatalf("wait failed turn: %v", err)
	}
	if err := waitServeMilestone(ctx, milestones, "idle with model capability"); err != nil {
		t.Fatalf("wait idle capability: %v", err)
	}

	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead after provider failure: %v", err)
	}
	if read.Thread.Status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("thread/read status = %q, want idle", read.Thread.Status.Type)
	}
	if !read.Thread.Serf.Capabilities.ChangeModel {
		t.Fatal("thread/read ChangeModel = false, want true after provider failure")
	}

	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
		Ref: ref, ModelProvider: "openai", Model: "gpt-5.6-sol",
	}); err != nil {
		t.Fatalf("ThreadModelSet after provider failure: %v", err)
	}
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID: "provider-recovery-turn",
		Ref:              ref,
		Input:            []appwire.InputItem{{Type: "text", Text: "recover on the selected provider"}},
	}); err != nil {
		t.Fatalf("TurnStart (recovered provider): %v", err)
	}
	if err := waitServeMilestone(ctx, milestones, "successful turn"); err != nil {
		t.Fatalf("wait successful turn: %v", err)
	}

	kimiRequests := kimi.Requests()
	openaiRequests := openai.Requests()
	if len(kimiRequests) != 1 || len(openaiRequests) != 1 {
		t.Fatalf("scripted provider requests = kimi %d, openai %d; want one each", len(kimiRequests), len(openaiRequests))
	}
	if kimiRequests[0].Provider != "kimi-anthropic" || kimiRequests[0].Model != "k3" {
		t.Fatalf("initial request = provider %q model %q, want provider %q model %q",
			kimiRequests[0].Provider, kimiRequests[0].Model, "kimi-anthropic", "k3")
	}
	if openaiRequests[0].Provider != "openai" || openaiRequests[0].Model != "gpt-5.6-sol" {
		t.Fatalf("recovery request = provider %q model %q, want provider %q model %q",
			openaiRequests[0].Provider, openaiRequests[0].Model, "openai", "gpt-5.6-sol")
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
