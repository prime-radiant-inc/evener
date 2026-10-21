package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func managerModeAwaitResync(t *testing.T, client *appwire.Client, ref, instance string, setting appwire.ManagerSetting) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case n := <-client.Notifications():
			if n.Method != appwire.NotifyEvenerThreadResync {
				continue
			}
			var hint appwire.ThreadResyncParams
			if err := json.Unmarshal(n.Params, &hint); err != nil {
				t.Fatal(err)
			}
			if hint.Ref != ref || hint.ThreadID != instance {
				t.Fatalf("resync = %+v, want selected %s / %s", hint, ref, instance)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: hint.Ref})
			if err != nil {
				t.Fatal(err)
			}
			if read.Thread.Evener.ManagerSetting == nil || *read.Thread.Evener.ManagerSetting != setting {
				t.Fatalf("resync reread = %+v, want %+v", read.Thread.Evener, setting)
			}
			return
		case <-timer.C:
			t.Fatalf("no Manager resync for %s", ref)
		}
	}
}

// Hypotheses: the cold projection uses the transcript ID instead of explicit
// WorkspaceID; canonical lookup then selects the retained pre-Clear transcript.
// A missing persisted identity would instead implicate the Clear writer.
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
	if err := f.launchInitial(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitStartup(); err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewSessionPreferencesStore(filepath.Join(f.root, "index.db"))
	roster := hubcore.NewRoster(f.runDir, nil)
	roster.Refresh()
	past := hubcore.NewPastIndex(f.stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{RunDir: f.runDir, StateDir: f.stateDir, Roster: roster, Past: past, SessionPreferences: store}
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
	var enabled appwire.ThreadManagerSetResponse
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}, &enabled); err != nil {
		t.Fatal(err)
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
	meta, err := schema.LoadSessionMeta(f.stateDir, instance)
	if err != nil {
		t.Fatal(err)
	}
	if "local:"+meta.WorkspaceID != ref {
		t.Fatalf("persisted identity = %q, want %q", meta.WorkspaceID, ref)
	}
	roster.Refresh()
	live, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if live.Thread.Evener.Ref != ref || !live.Thread.Evener.Manager {
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
		if read.Thread.Evener.Ref != requested || read.Thread.Evener.InstanceID != selected.instance || !read.Thread.Evener.Manager || read.Thread.Evener.ManagerSetting == nil || *read.Thread.Evener.ManagerSetting != enabled.View.Setting {
			t.Errorf("cold read %s = %+v, want selected instance %s and shared setting %+v", requested, read.Thread.Evener, selected.instance, enabled.View.Setting)
		}
		var response appwire.ThreadManagerSetResponse
		if err := cold.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: requested, Enabled: true}, &response); err != nil {
			t.Fatal(err)
		}
		if response.View.Ref != requested || response.View.InstanceID != selected.instance || response.View.Setting != enabled.View.Setting {
			t.Errorf("cold idempotent set = %+v", response.View)
		}
	}
	aliasSetting, err := store.Manager("local:" + instance)
	if err != nil || aliasSetting != (appwire.ManagerSetting{}) {
		t.Fatalf("cold alias created a separate row: %+v, %v", aliasSetting, err)
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
	for _, state := range []bool{false, true} {
		var response appwire.ThreadManagerSetResponse
		if err := aliasClient.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:" + instance, Enabled: state}, &response); err != nil {
			t.Fatal(err)
		}
		managerModeAwaitResync(t, cold, ref, initial.Thread.Evener.InstanceID, response.View.Setting)
		managerModeAwaitResync(t, aliasClient, "local:"+instance, instance, response.View.Setting)
		old, err := cold.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		if old.Thread.Evener.InstanceID != initial.Thread.Evener.InstanceID || old.Thread.Evener.Manager != state || old.Thread.Evener.ManagerSetting == nil || *old.Thread.Evener.ManagerSetting != response.View.Setting {
			t.Fatalf("historical view failed to share preference without rerouting: %+v", old.Thread.Evener)
		}
		enabled = response
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
	if forked.Thread.Evener.Manager || forked.Thread.Evener.ManagerSetting == nil || forked.Thread.Evener.ManagerSetting.Revision != 0 {
		t.Fatalf("fork inherited Manager: %+v", forked.Thread.Evener)
	}
	const legacyID = "02wMz5Txv1C3Hut0M8GCeB"
	writeSession(t, f.stateDir, legacyID, f.workDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	var unrelated []*appwire.Client
	for _, id := range []string{child, legacyID} {
		peer := dialHubRPC(t, hub)
		defer peer.Close()
		if _, err := peer.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		if _, err := peer.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + id, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
		unrelated = append(unrelated, peer)
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
	cfg.SessionPreferences = hubcore.NewSessionPreferencesStore(filepath.Join(f.root, "index.db"))
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
	if resumed.Thread.Evener.Ref != ref || resumed.Thread.Evener.InstanceID != instance || !resumed.Thread.Evener.Manager || resumed.Thread.Evener.ManagerSetting == nil || *resumed.Thread.Evener.ManagerSetting != enabled.View.Setting {
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
	var changed appwire.ThreadManagerSetResponse
	if err := aliasClient.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:" + instance, Enabled: false}, &changed); err != nil {
		t.Fatal(err)
	}
	managerModeAwaitResync(t, liveClient, ref, currentInstance, changed.View.Setting)
	managerModeAwaitResync(t, aliasClient, "local:"+instance, instance, changed.View.Setting)
	if err := liveClient.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}, &changed); err != nil {
		t.Fatal(err)
	}
	managerModeAwaitResync(t, aliasClient, "local:"+instance, instance, changed.View.Setting)
	managerModeAwaitResync(t, liveClient, ref, currentInstance, changed.View.Setting)
	for _, peer := range unrelated {
		timer := time.NewTimer(100 * time.Millisecond)
		for done := false; !done; {
			select {
			case n := <-peer.Notifications():
				if n.Method == appwire.NotifyEvenerThreadResync {
					t.Fatalf("fork or legacy workspace received unrelated setting hint: %s", n.Params)
				}
			case <-timer.C:
				done = true
			}
		}
	}
	if err := f.launchInitial(); err != nil {
		t.Fatal(err)
	}
	roster.Refresh()
	newRoot, err := reconnected.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: f.entry.WorkspaceRef})
	if err != nil {
		t.Fatal(err)
	}
	if newRoot.Thread.Evener.Ref == ref || newRoot.Thread.Evener.Manager || newRoot.Thread.Evener.ManagerSetting == nil || newRoot.Thread.Evener.ManagerSetting.Revision != 0 {
		t.Fatalf("new root inherited Manager: %+v", newRoot.Thread.Evener)
	}
}
