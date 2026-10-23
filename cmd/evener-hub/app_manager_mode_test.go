package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

func managerModeClient(t *testing.T, store *hubcore.SessionPreferencesStore) *appwire.Client {
	t.Helper()
	const instance = "manager-instance"
	const ref = "local:manager-workspace"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "manager-test-token"})
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity, nil)
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemonHTTP.URL[len("http"):], SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "manager-test-token"})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, StateDir: runDir, Past: hubcore.NewPastIndex(""), SessionPreferences: store})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestManagerModeHumanRPCCanonicalAliasAndProjection(t *testing.T) {
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	client := managerModeClient(t, store)
	initial, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:manager-instance", Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Thread.Evener.Manager || initial.Thread.Evener.ManagerSetting == nil || !initial.Thread.Evener.ManagerEditable {
		t.Fatalf("default projection = %+v", initial.Thread.Evener)
	}
	var response appwire.ThreadManagerSetResponse
	// An unrecognized human JSON field must never select authority.
	err = client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, map[string]any{"ref": "local:manager-instance", "enabled": true, "hubId": "forged"}, &response)
	if err != nil {
		t.Fatal(err)
	}
	hubID, err := store.HubID()
	if err != nil {
		t.Fatal(err)
	}
	if response.View.Ref != "local:manager-workspace" || response.View.InstanceID != "manager-instance" || response.View.Setting.HubID != hubID || !response.View.Setting.Enabled {
		t.Fatalf("canonical response = %+v", response)
	}
	alias, err := store.Manager("local:manager-instance")
	if err != nil || alias.Enabled {
		t.Fatalf("alias persisted separately: %+v, %v", alias, err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case notification := <-client.Notifications():
			if notification.Method != appwire.NotifyEvenerThreadResync {
				continue
			}
			var hint appwire.ThreadResyncParams
			if err := json.Unmarshal(notification.Params, &hint); err != nil {
				t.Fatal(err)
			}
			if hint.Ref != response.View.Ref {
				t.Fatalf("resync = %+v", hint)
			}
			read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: hint.Ref})
			if err != nil {
				t.Fatal(err)
			}
			// The Manager bit is the session's own start-time property; the
			// toggle RPC's projection contract is the setting it writes.
			if read.Thread.Evener.ManagerSetting == nil || *read.Thread.Evener.ManagerSetting != response.View.Setting || !read.Thread.Evener.ManagerEditable {
				t.Fatalf("updated projection = %+v", read.Thread.Evener)
			}
			return
		case <-timer.C:
			t.Fatal("no Manager change resync")
		}
	}
}

func TestManagerModeHumanRPCConflictCarriesCurrentView(t *testing.T) {
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	selected, err := store.SetManager("local:manager-workspace", "other-hub", true)
	if err != nil {
		t.Fatal(err)
	}
	client := managerModeClient(t, store)
	for _, enabled := range []bool{true, false} {
		var response appwire.ThreadManagerSetResponse
		err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:manager-instance", Enabled: enabled}, &response)
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("conflict = %v", err)
		}
		raw, err := json.Marshal(wire.Data)
		if err != nil {
			t.Fatal(err)
		}
		var data appwire.ThreadManagerSetErrorData
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.View == nil || data.View.Setting != selected || data.View.Ref != "local:manager-workspace" || data.View.InstanceID != "manager-instance" {
			t.Fatalf("wire error lost current view: %s", raw)
		}
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:manager-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	// The Manager bit is the session's own start-time property; the selected
	// authority's projection contract is the setting row it owns.
	if read.Thread.Evener.ManagerSetting == nil || *read.Thread.Evener.ManagerSetting != selected || read.Thread.Evener.ManagerEditable {
		t.Fatalf("selected authority projection = %+v", read.Thread.Evener)
	}
}

func TestManagerModeHumanRPCStorageFailureCarriesCurrentView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	store := hubcore.NewSessionPreferencesStore(path)
	client := managerModeClient(t, store)
	var enabled appwire.ThreadManagerSetResponse
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:manager-workspace", Enabled: true}, &enabled); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_manager_write BEFORE UPDATE ON session_manager BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	var response appwire.ThreadManagerSetResponse
	err = client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:manager-instance", Enabled: false}, &response)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("write failure = %v", err)
	}
	raw, err := json.Marshal(wire.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data appwire.ThreadManagerSetErrorData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.View == nil || *data.View != enabled.View {
		t.Fatalf("write failure lost current view: %s", raw)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: enabled.View.Ref})
	if err != nil {
		t.Fatal(err)
	}
	// The Manager bit is the session's own start-time property; the failed
	// write's projection contract is the unchanged setting row.
	if read.Thread.Evener.ManagerSetting == nil || *read.Thread.Evener.ManagerSetting != enabled.View.Setting {
		t.Fatalf("failed write changed projection: %+v", read.Thread.Evener)
	}
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "remote:manager-workspace", Enabled: true}, &response); err == nil {
		t.Fatal("remote Manager mutation succeeded")
	}
}

func TestManagerModeResyncScopedToWorkspace(t *testing.T) {
	sources := appsource.NewRegistry()
	var entries []appsource.LocalDaemonEntry
	for _, name := range []string{"manager", "unrelated", "remote"} {
		instance, ref := name+"-instance", "local:"+name+"-workspace"
		if name == "remote" {
			instance, ref = "manager-instance", "local:manager-workspace"
		}
		daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
		identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
		if err != nil {
			t.Fatal(err)
		}
		daemon.ReplaceAppIdentity(identity, nil)
		upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
		t.Cleanup(upstream.Close)
		if name == "remote" {
			peer := dialHubRPC(t, upstream)
			t.Cleanup(func() { _ = peer.Close() })
			if _, err := peer.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			sources.Add(appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) { return peer, nil }))
		} else {
			entries = append(entries, appsource.LocalDaemonEntry{Entry: rendezvous.Entry{SourceID: "local", ThreadID: instance, SessionID: instance, InstanceID: instance, WorkspaceRef: ref, Endpoint: upstream.URL, Protocol: appwire.ProtocolVersion}})
		}
	}
	sources.Add(appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry { return entries }, nil))
	store := hubcore.NewSessionPreferencesStore(filepath.Join(t.TempDir(), "index.db"))
	server := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), StateDir: t.TempDir(), Past: hubcore.NewPastIndex(""), SessionPreferences: store}, sources)
	wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(wire.Close)
	var clients []*appwire.Client
	for _, ref := range []string{"local:manager-workspace", "local:manager-instance", "local:unrelated-workspace", "remote:manager-workspace"} {
		client := dialHubRPC(t, wire)
		t.Cleanup(func() { _ = client.Close() })
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		if _, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: ref, Subscribe: true}); err != nil {
			t.Fatalf("subscribe %s: %v", ref, err)
		}
		clients = append(clients, client)
	}
	// Remote subscription publishes its own initial snapshot resync. Consume
	// that protocol baseline before testing the separate local setting change.
	baseline := time.NewTimer(5 * time.Second)
	defer baseline.Stop()
	for ready := false; !ready; {
		select {
		case n := <-clients[3].Notifications():
			ready = n.Method == appwire.NotifyEvenerThreadResync
		case <-baseline.C:
			t.Fatal("remote subscription did not publish its initial snapshot hint")
		}
	}
	var response appwire.ThreadManagerSetResponse
	if err := clients[0].Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:manager-workspace", Enabled: true}, &response); err != nil {
		t.Fatal(err)
	}
	for _, client := range clients[:2] {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for received := false; !received; {
			select {
			case n := <-client.Notifications():
				if n.Method != appwire.NotifyEvenerThreadResync {
					continue
				}
				var hint appwire.ThreadResyncParams
				if err := json.Unmarshal(n.Params, &hint); err != nil {
					t.Fatal(err)
				}
				if hint.Ref != response.View.Ref || hint.ThreadID != "manager-instance" {
					t.Fatalf("wrong hint: %+v", hint)
				}
				received = true
			case <-timer.C:
				t.Fatal("workspace subscriber missed resync")
			}
		}
	}
	for _, client := range clients[2:] {
		timer := time.NewTimer(100 * time.Millisecond)
		for done := false; !done; {
			select {
			case n := <-client.Notifications():
				if n.Method == appwire.NotifyEvenerThreadResync {
					t.Fatalf("unrelated subscriber received Manager resync: %s", n.Params)
				}
			case <-timer.C:
				done = true
			}
		}
	}
}

func TestManagerModeLegacySavedIdentityAndUnavailable(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "legacy-0123456789")
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeSession(t, stateDir, id, root)
	past := hubcore.NewPastIndex(stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewSessionPreferencesStore(filepath.Join(root, "index.db"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: stateDir, Past: past, SessionPreferences: store})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var response appwire.ThreadManagerSetResponse
	ref := "local:" + id
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: ref, Enabled: true}, &response); err != nil {
		t.Fatal(err)
	}
	if response.View.Ref != ref || response.View.InstanceID != id || !response.View.Setting.Enabled {
		t.Fatalf("legacy view = %+v", response.View)
	}
	stored, err := store.Manager(ref)
	if err != nil || stored != response.View.Setting {
		t.Fatalf("legacy private key = %+v, %v", stored, err)
	}
	if _, err := managerPreferenceRef(hubcore.WebConfig{}, appwire.Thread{Status: appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded}, Evener: appwire.EvenerThread{Ref: ref, InstanceID: id}}); err == nil {
		t.Fatal("unresolved saved identity fabricated a key")
	}
	if err := client.Request(t.Context(), appwire.MethodEvenerThreadManagerSet, appwire.ThreadManagerSetParams{Ref: "local:missing", Enabled: true}, &response); err == nil {
		t.Fatal("missing session enabled Manager")
	}
	missing, err := store.Manager("local:missing")
	if err != nil || missing != (appwire.ManagerSetting{}) {
		t.Fatalf("missing identity created setting: %+v, %v", missing, err)
	}
}
