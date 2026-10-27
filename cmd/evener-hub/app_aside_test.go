package hub

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestHubRPCThreadForkAsideCreatesSideThread verifies the aside mode of
// thread/fork: a local session is forked at its tip (no source turn, no
// edited input), and the child inherits the parent session's identity and
// config through its meta with lineage pointing at the parent.
func TestHubRPCThreadForkAsideCreatesSideThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-aside-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadFork(context.Background(), client, appwire.ThreadForkParams{
		Ref:   "local:" + parentID,
		Aside: true,
	})
	if err != nil {
		t.Fatalf("ThreadFork(aside): %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Evener.Ref != "local:"+resp.Thread.ID {
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
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		Aside:         true,
		SourceItemKey: "apptranscript-item-v2:t_1:0:0",
		EditedInput:   "edit",
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork(aside+turn fields) succeeded, want invalid-params rejection")
	}
	requireWireError(t, err, appwire.CodeInvalidParams)
}

// TestHubRPCThreadForkAsideUnavailableForNonLocal pins that aside is a
// local-evener-session feature: remote sources are never invoked with it.
func TestHubRPCThreadForkAsideUnavailableForNonLocal(t *testing.T) {
	source := &forkingRelaySource{
		id: "remote",
		thread: appwire.Thread{
			ID:        "th_aside",
			SessionID: "th_aside",
			Source:    "remote",
			Evener:    appwire.EvenerThread{Ref: "remote:th_aside", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:   "remote:th_aside",
		Aside: true,
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork(aside) succeeded for a non-local source")
	}
	if source.forkCalled {
		t.Fatal("aside reached the non-local source")
	}
	requireWireError(t, err, appwire.CodeUnavailable)
}
