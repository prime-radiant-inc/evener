package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// waitServeMilestonesInAnyOrder waits for every milestone in wants, in
// whatever order they arrive.
func waitServeMilestonesInAnyOrder(ctx context.Context, milestones <-chan string, wants ...string) error {
	pending := map[string]bool{}
	for _, want := range wants {
		pending[want] = true
	}
	for len(pending) > 0 {
		select {
		case got := <-milestones:
			if !pending[got] {
				return fmt.Errorf("unexpected structured milestone %q while waiting for %v", got, wants)
			}
			delete(pending, got)
		case <-ctx.Done():
			return fmt.Errorf("waiting for structured milestones %v: %w", wants, ctx.Err())
		}
	}
	return nil
}

func waitServeTurn(ctx context.Context, turns <-chan string, want string) error {
	for {
		select {
		case got := <-turns:
			if got == want {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("waiting for turn %q: %w", want, ctx.Err())
		}
	}
}

// TestServeModelSwitch_ThreadReadReflectsNewModelWithNoInterveningTurn pins G2
// against the REAL cmd/evener/serve.go wiring (not a hand-built hook). It boots
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
			// best-effort; the thread/shutdown below normally drains this.
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

	// This premise read is deliberately a single, unpolled call: it is exactly
	// what a real client's first thread/read after discovering the rendezvous
	// entry looks like. serve.go seeds status.Model/Profile synchronously right
	// after bridgeSession(sess), so this must already observe "gpt-initial" with
	// no waiting -- polling here would hide a regression of that synchronous
	// seed behind the bridge's asynchronous EventSessionStart drain (#251).
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

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
	}
}

// collectServeModelSwitchFrames drains the client's notifications and reports
// two things a mid-turn switch test needs: every thread/model/changed frame's
// params, and the ids of turns that reached TurnStatusCompleted (first
// completion only, skipping the system prelude and re-publications).
type serveModelSwitchFrames struct {
	modelChanged chan appwire.ThreadModelChangedParams
	turnDone     chan string
}

func collectServeModelSwitchFrames(client *appwire.Client, ref string) *serveModelSwitchFrames {
	frames := &serveModelSwitchFrames{
		modelChanged: make(chan appwire.ThreadModelChangedParams, 4),
		turnDone:     make(chan string, 8),
	}
	completed := map[string]bool{}
	go func() {
		for notification := range client.Notifications() {
			switch notification.Method {
			case appwire.NotifyThreadModelChanged:
				var params appwire.ThreadModelChangedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				frames.modelChanged <- params
			case appwire.NotifyHistoryUpdated:
				var params appwire.HistoryUpdatedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				for _, turn := range params.Turns {
					if turn.Status == appwire.TurnStatusCompleted && turn.ID != appwire.SystemPreludeTurnID && !completed[turn.ID] {
						completed[turn.ID] = true
						frames.turnDone <- turn.ID
					}
				}
			}
		}
	}()
	return frames
}

func waitForServeModelChanged(ctx context.Context, frames *serveModelSwitchFrames, wantModel string) (appwire.ThreadModelChangedParams, error) {
	for {
		select {
		case params := <-frames.modelChanged:
			if params.Model == wantModel {
				return params, nil
			}
		case <-ctx.Done():
			return appwire.ThreadModelChangedParams{}, fmt.Errorf("waiting for thread/model/changed with model %q: %w", wantModel, ctx.Err())
		}
	}
}

// TestServeModelSwitch_SubscribedClientLearnsSwitchAtRest pins the push half of
// an at-rest switch: a client subscribed to the thread must receive
// thread/model/changed carrying the new provider/model, without any turn
// running. The read half (thread/read reflecting the switch) is pinned by
// TestServeModelSwitch_ThreadReadReflectsNewModelWithNoInterveningTurn; without
// this push half, every subscribed surface (the hub web client included) keeps
// showing the old model until some later re-read — which reads as the switch
// silently flipping back.
func TestServeModelSwitch_SubscribedClientLearnsSwitchAtRest(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai", steps: nil})

	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()

	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{
			"--model", "openai/gpt-initial",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
		})
	}()
	entry := waitForServeTestRendezvous(t, runDir)
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID)
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
		ClientInfo: appwire.ClientInfo{Name: "serve-model-switch-push-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead subscribe: %v", err)
	}
	frames := collectServeModelSwitchFrames(client, ref)

	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "openai",
		Model:         "gpt-switched",
	}); err != nil {
		t.Fatalf("ThreadModelSet: %v", err)
	}

	params, err := waitForServeModelChanged(ctx, frames, "gpt-switched")
	if err != nil {
		t.Fatalf("thread/model/changed after an at-rest switch: %v", err)
	}
	if params.ModelProvider != "openai" {
		t.Fatalf("thread/model/changed ModelProvider = %q, want %q", params.ModelProvider, "openai")
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
	}
}

// TestServeModelSwitch_MidTurnSetQueuesUntilNextTurn pins the composer's
// contract: a model picked while a turn is still running is accepted (no
// Conflict), the running turn finishes on the model it started with, and the
// switch applies at the turn boundary so the NEXT turn — typically the message
// the user was typing when they picked — runs on the new model. The queued
// switch must also reach subscribed clients as thread/model/changed when it
// applies.
func TestServeModelSwitch_MidTurnSetQueuesUntilNextTurn(t *testing.T) {
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})
	provider := &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				select {
				case firstArrived <- struct{}{}:
				default:
				}
				<-releaseFirst
				return scriptedCommunicate("first turn done")
			},
		},
	}
	installServeScriptedProvider(t, provider)

	workDir := t.TempDir()
	stateDir := t.TempDir()
	runDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{
			"--model", "openai/gpt-initial",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
		})
	}()
	entry := waitForServeTestRendezvous(t, runDir)
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-mid-turn-switch-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatalf("ThreadRead subscribe: %v", err)
	}
	frames := collectServeModelSwitchFrames(client, ref)

	first, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "mid-turn-switch-first",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "start the first turn"}},
	})
	if err != nil {
		t.Fatalf("TurnStart (first): %v", err)
	}

	select {
	case <-firstArrived:
	case <-ctx.Done():
		t.Fatalf("first turn never reached the provider: %v", ctx.Err())
	}

	// The pick mid-turn: accepted now, applied at the boundary. This is the
	// call that used to answer Conflict "session is processing".
	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
		Ref:           ref,
		ModelProvider: "openai",
		Model:         "gpt-queued",
	}); err != nil {
		t.Fatalf("ThreadModelSet during an active turn: %v", err)
	}
	close(releaseFirst)

	if err := waitServeTurn(ctx, frames.turnDone, first.Turn.ID); err != nil {
		t.Fatalf("first turn completion: %v", err)
	}
	if params, err := waitForServeModelChanged(ctx, frames, "gpt-queued"); err != nil {
		t.Fatalf("thread/model/changed after the boundary: %v", err)
	} else if params.ModelProvider != "openai" {
		t.Fatalf("thread/model/changed ModelProvider = %q, want %q", params.ModelProvider, "openai")
	}

	second, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "mid-turn-switch-second",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "run on the switched model"}},
	})
	if err != nil {
		t.Fatalf("TurnStart (second): %v", err)
	}
	if err := waitServeTurn(ctx, frames.turnDone, second.Turn.ID); err != nil {
		t.Fatalf("second turn completion: %v", err)
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("scripted provider requests = %d, want 2", len(requests))
	}
	if requests[0].Model != "gpt-initial" {
		t.Fatalf("first turn ran on model %q, want the model the turn started with (%q)", requests[0].Model, "gpt-initial")
	}
	if requests[1].Model != "gpt-queued" {
		t.Fatalf("second turn ran on model %q, want the queued switch (%q)", requests[1].Model, "gpt-queued")
	}

	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead after the queued switch applied: %v", err)
	}
	if read.Thread.ModelProvider != "gpt-queued" {
		t.Fatalf("thread/read ModelProvider = %q, want %q", read.Thread.ModelProvider, "gpt-queued")
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
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
		})
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
				return
			}
		}
	})

	// One context spans the whole flow: dial, two full turns, and every
	// milestone wait. Each wait is event-driven, so the budget only has to be
	// impossible for a healthy run to exhaust; 150s matches what
	// serve_goal_test.go gives a comparable full-daemon multi-turn flow, and a
	// loaded 2-core CI runner under -race has blown through 30s here.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
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
	completedTurns := make(chan string, 8)
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
		completed := map[string]bool{}
		ranTurn := false
		for notification := range client.Notifications() {
			switch notification.Method {
			case appwire.NotifyHistoryUpdated:
				var params appwire.HistoryUpdatedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				for _, turn := range params.Turns {
					switch turn.Status {
					case appwire.TurnStatusFailed:
						record("failed turn")
					case appwire.TurnStatusCompleted:
						// The prelude and a turn published again at a higher
						// version are not new completions; only each real
						// turn's first completion belongs on completedTurns,
						// which nothing drains until the recovery phase.
						if turn.ID != appwire.SystemPreludeTurnID && !completed[turn.ID] {
							completed[turn.ID] = true
							completedTurns <- turn.ID
						}
					}
				}
			case appwire.NotifyThreadStatusChanged:
				var params appwire.ThreadStatusChangedParams
				if json.Unmarshal(notification.Params, &params) != nil || params.Ref != ref {
					continue
				}
				if params.Status.Type == appwire.ThreadStatusActive {
					ranTurn = true
				}
				// A failed turn rests the session on systemError (agent
				// RestingWireState) until the next turn starts.
				if params.Status.Type == appwire.ThreadStatusSystemError &&
					params.Capabilities != nil && params.Capabilities.ChangeModel && ranTurn {
					// Boot parks the thread with ChangeModel too, and on a
					// starved runner that status notification is still draining
					// when the subscribe cut lands. A resting status published
					// before the failing turn ran proves nothing about
					// restoration, so only one after the turn went active
					// counts. Its history (the failed turn) is published from
					// the recorded entries on its own schedule, so the two
					// milestones are awaited in either order.
					record("resting with model capability")
				}
			}
		}
	}()

	_, err = client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "provider-failure-turn",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "make the first request"}},
	})
	if err != nil {
		t.Fatalf("TurnStart (failed provider): %v", err)
	}

	if err := waitServeMilestonesInAnyOrder(ctx, milestones, "failed turn", "resting with model capability"); err != nil {
		t.Fatalf("wait failed turn and resting capability: %v", err)
	}

	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatalf("ThreadRead after provider failure: %v", err)
	}
	if read.Thread.Status.Type != appwire.ThreadStatusSystemError {
		t.Fatalf("thread/read status = %q, want systemError", read.Thread.Status.Type)
	}
	if !read.Thread.Evener.Capabilities.ChangeModel {
		t.Fatal("thread/read ChangeModel = false, want true after provider failure")
	}

	if err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
		Ref: ref, ModelProvider: "openai", Model: "gpt-5.6-sol",
	}); err != nil {
		t.Fatalf("ThreadModelSet after provider failure: %v", err)
	}
	recovery, err := client.TurnStart(ctx, appwire.TurnStartParams{
		ClientMutationID:   "provider-recovery-turn",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "recover on the selected provider"}},
	})
	if err != nil {
		t.Fatalf("TurnStart (recovered provider): %v", err)
	}
	if err := waitServeTurn(ctx, completedTurns, recovery.Turn.ID); err != nil {
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

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	// Comfortably past serve's own 30s shutdown drain budget, so this only
	// fires on a daemon that is genuinely stuck, never on a starved runner
	// still inside its teardown contract.
	case <-time.After(60 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
	}
}
