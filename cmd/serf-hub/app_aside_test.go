package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// TestHubRPCThreadForkAsideCreatesSideThread verifies the aside mode of
// thread/fork: a local session is forked at its tip (no source turn, no
// edited input), and the child inherits the parent session's identity and
// config through its meta with lineage pointing at the parent.
func TestHubRPCThreadForkAsideCreatesSideThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-aside-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := client.ThreadFork(context.Background(), appwire.ThreadForkParams{
		Ref:   "local:" + parentID,
		Aside: true,
	})
	if err != nil {
		t.Fatalf("ThreadFork(aside): %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Serf.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}

	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Fatalf("child meta=%+v", childMeta)
	}
	// buildRPCParentSession writes 3 entries (U, A, U); the aside child copies
	// all of them and diverges one past the tip.
	if childMeta.DivergenceTurn != 4 {
		t.Fatalf("child DivergenceTurn=%d, want 4", childMeta.DivergenceTurn)
	}
	if childMeta.ProfileID != "openai" || childMeta.Model != "gpt-5" {
		t.Fatalf("child profile/model=%q/%q, want openai/gpt-5", childMeta.ProfileID, childMeta.Model)
	}
}

// TestHubRPCThreadForkAsideRejectsTurnFields pins the contract that aside is
// mutually exclusive with divergent-fork fields.
func TestHubRPCThreadForkAsideRejectsTurnFields(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-aside-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:          "local:" + parentID,
		Aside:        true,
		SourceTurnID: "1",
		EditedInput:  "edit",
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork(aside+turn fields) succeeded, want invalid-params rejection")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error=%v, want InvalidParams wire error", err)
	}
}

// TestHubRPCThreadForkAsideUnavailableForNonLocal pins that aside is a
// local-serf-session feature: remote sources are never invoked with it.
func TestHubRPCThreadForkAsideUnavailableForNonLocal(t *testing.T) {
	source := &forkingRelaySource{
		relayBroadcastSource: relayBroadcastSource{
			id: "codex",
			thread: appwire.Thread{
				ID:        "th_aside",
				SessionID: "th_aside",
				Source:    "codex",
				Serf:      appwire.SerfThread{Ref: "codex:th_aside", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
			},
			notifications: make(chan appwire.Notification, 1),
			canceled:      make(chan struct{}, 1),
		},
	}
	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:   "codex:th_aside",
		Aside: true,
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork(aside) succeeded for a non-local source")
	}
	if source.forkCalled {
		t.Fatal("aside reached the non-local source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("error=%v, want Unavailable wire error", err)
	}
}
