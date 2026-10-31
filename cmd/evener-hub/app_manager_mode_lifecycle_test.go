package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestManagerModeClearColdRead(t *testing.T) {
	f := &daemonRetirementProcessFixture{t: t, cfg: DefaultConfig(), params: map[string]appwire.TurnStartParams{}, turnMutations: map[string]string{}}
	f.setupRoots(t)
	f.stateDir = filepath.Join(f.root, "manager-0123456789")
	if err := os.MkdirAll(f.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.installSeam()
	f.hub = f.newSpawner()
	t.Cleanup(func() {
		if err := f.cleanup(); err != nil {
			t.Error(err)
		}
	})
	before := f.handleCount()
	launchCtx, cancel := context.WithTimeout(t.Context(), f.watchdogBudget())
	defer cancel()
	request := f.launchRequest()
	request.Manager = true
	entry, err := f.hub.Spawn(launchCtx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.adoptLaunch(before, entry); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitStartup(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(f.runDir, nil)
	roster.Refresh()
	past := hubcore.NewPastIndex(f.stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{RunDir: f.runDir, StateDir: f.stateDir, Roster: roster, Past: past, HubID: "hub-test"}
	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	ref := f.entry.WorkspaceRef
	initial, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if !initial.Thread.Evener.Manager {
		t.Fatalf("start flag absent from live projection: %+v", initial.Thread.Evener)
	}
	turn, err := f.startMutation("manager-fork-seed")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.waitTurnSettled(turn.Turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	turns, err := f.transcriptTurns()
	if err != nil {
		t.Fatal(err)
	}
	divergence := 0
	for i, turn := range turns {
		if turn.Kind == schema.TurnUserInput {
			divergence = i + 1
			break
		}
	}
	if divergence == 0 {
		t.Fatal("actual turn produced no user input")
	}
	cleared, err := client.ThreadClear(t.Context(), appwire.ThreadClearParams{Ref: ref, ClientMutationID: "manager-clear", ExpectedInstanceID: initial.Thread.Evener.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	instance := cleared.Thread.Evener.InstanceID
	if instance == "" || instance == initial.Thread.Evener.InstanceID {
		t.Fatalf("Clear instance = %q, previous = %q", instance, initial.Thread.Evener.InstanceID)
	}
	if cleared.Thread.Evener.Ref != ref {
		t.Fatalf("Clear ref = %q, want %q", cleared.Thread.Evener.Ref, ref)
	}
	if !cleared.Thread.Evener.Manager {
		t.Fatalf("Clear lost Manager: %+v", cleared.Thread.Evener)
	}
	meta, err := schema.LoadSessionMeta(f.stateDir, instance)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != instance || !meta.Manager {
		t.Fatalf("persisted ID/Manager = %q/%t, want %q/true", meta.ID, meta.Manager, instance)
	}
	roster.Refresh()
	live, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if live.Thread.Evener.Ref != ref {
		t.Fatalf("live projection = %+v", live.Thread.Evener)
	}
	if err := client.ThreadShutdown(t.Context(), appwire.ThreadShutdownParams{Ref: ref}); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// A new human transport exercises cold source resolution without a live relay.
	cold := dialHubRPC(t, hub)
	defer cold.Close()
	if _, err := cold.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []struct{ ref, instance string }{{"local:" + instance, instance}, {ref, initial.Thread.Evener.InstanceID}} {
		requested := selected.ref
		read, err := cold.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: requested})
		if err != nil {
			t.Fatal(err)
		}
		if read.Thread.Evener.Ref != requested || read.Thread.Evener.InstanceID != selected.instance || !read.Thread.Evener.Manager {
			t.Errorf("cold read %s = %+v, want selected instance %s with Manager", requested, read.Thread.Evener, selected.instance)
		}
	}
	if _, err := cold.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	aliasClient := dialHubRPC(t, hub)
	defer aliasClient.Close()
	if _, err := aliasClient.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := aliasClient.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + instance, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	child, _, err := agent.ForkSessionAtUserTurn(f.stateDir, initial.Thread.Evener.InstanceID, divergence, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	forked, err := cold.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + child})
	if err != nil {
		t.Fatal(err)
	}
	if forked.Thread.Evener.Manager {
		t.Fatalf("fork inherited Manager: %+v", forked.Thread.Evener)
	}
	const legacyID = "02wMz5Txv1C3Hut0M8GCeB"
	writeSession(t, f.stateDir, legacyID, f.workDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	legacy, err := cold.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + legacyID})
	if err != nil || legacy.Thread.Evener.Manager {
		t.Fatalf("legacy session inherited Manager: %+v %v", legacy, err)
	}

	// Rebuild the real hub spawner and human server over the same persisted roots.
	f.sessionID = instance
	if err := f.restartHub(); err != nil {
		t.Fatal(err)
	}
	if err := f.ensureOwner(); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	restarted := newHubRPCTestServer(t, cfg)
	defer restarted.Close()
	reconnected := dialHubRPC(t, restarted)
	defer reconnected.Close()
	if _, err := reconnected.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	resumed, err := reconnected.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Thread.Evener.Ref != ref || resumed.Thread.Evener.InstanceID != instance || !resumed.Thread.Evener.Manager {
		t.Fatalf("resumed projection = %+v", resumed.Thread.Evener)
	}
	// A second Clear leaves the intermediate instance independently readable
	// while the same workspace is live at a new instance.
	second, err := client.ThreadClear(t.Context(), appwire.ThreadClearParams{Ref: ref, ClientMutationID: "manager-clear-again", ExpectedInstanceID: instance})
	if err != nil {
		t.Fatal(err)
	}
	currentInstance := second.Thread.Evener.InstanceID
	roster.Refresh()
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	liveClient := dialHubRPC(t, hub)
	defer liveClient.Close()
	if _, err := liveClient.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	liveRead, err := liveClient.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if liveRead.Thread.Evener.InstanceID != currentInstance {
		t.Fatalf("live subscription got stale instance: %+v", liveRead.Thread.Evener)
	}
	// Renew the intermediate historical selection after Clear; retain its own
	// public identity rather than replacing it with the live workspace ref.
	selected, err := aliasClient.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + instance, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Thread.Evener.Ref != "local:"+instance || selected.Thread.Evener.InstanceID != instance {
		t.Fatalf("historical sibling was rerouted: %+v", selected.Thread.Evener)
	}
	if !selected.Thread.Evener.Manager || !liveRead.Thread.Evener.Manager || !second.Thread.Evener.Manager {
		t.Fatalf("Manager after second Clear: historical=%t live=%t response=%t", selected.Thread.Evener.Manager, liveRead.Thread.Evener.Manager, second.Thread.Evener.Manager)
	}
	if err := f.launchInitial(); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	newRoot, err := reconnected.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: f.entry.WorkspaceRef})
	if err != nil {
		t.Fatal(err)
	}
	if newRoot.Thread.Evener.Ref == ref || newRoot.Thread.Evener.Manager {
		t.Fatalf("new root inherited Manager: %+v", newRoot.Thread.Evener)
	}
}
