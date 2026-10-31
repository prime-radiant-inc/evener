package hub

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// Manager belongs to the session's start-time metadata. A hub capability pass
// must preserve both the positive and negative projection without a store.
func TestManagerProjectionPreservesSessionProperty(t *testing.T) {
	for _, manager := range []bool{false, true} {
		thread := appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:workspace", InstanceID: "instance", Manager: manager}}
		projected := applyHubCapabilities(hubcore.WebConfig{}, thread)
		if projected.Evener.Manager != manager || projected.Evener.Ref != thread.Evener.Ref || projected.Evener.InstanceID != thread.Evener.InstanceID {
			t.Fatalf("hub changed session-owned projection: %+v", projected.Evener)
		}
	}
}

func TestManagerHasNoToggleRPC(t *testing.T) {
	hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: t.TempDir(), Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		var response appwire.EmptyResponse
		err := client.Request(t.Context(), "evener/thread/manager/set", map[string]any{"ref": "local:workspace", "enabled": enabled}, &response)
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeMethodNotFound {
			t.Fatalf("Manager toggle remains callable: enabled=%t error=%v", enabled, err)
		}
	}
}

func TestManagerLegacySavedSessionRemainsOrdinary(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "legacy-0123456789")
	const id = "02wMz5Txv1C3Hut0M8GCeB"
	writeSession(t, stateDir, id, root)
	past := hubcore.NewPastIndex(stateDir)
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{StateDir: stateDir, Past: past})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	read, err := client.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + id})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.Evener.Manager || read.Thread.Evener.InstanceID != id {
		t.Fatalf("legacy session projection: %+v", read.Thread.Evener)
	}
}
