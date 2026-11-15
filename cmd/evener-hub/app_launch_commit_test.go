package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/testfs"
)

// TestHubRPCEvenerLaunchSetLayerBroadcastsExactlyOnce pins the launch
// controller's OnCommitted port as the handler's only broadcast path: the layer
// write reaches every subscriber once, and the handler no longer pairs its own
// notify with the controller's.
func TestHubRPCEvenerLaunchSetLayerBroadcastsExactlyOnce(t *testing.T) {
	root := t.TempDir()
	hub := newHubRPCTestServer(t, hubcore.WebConfig{LaunchConfigRoot: root})
	defer hub.Close()
	clientA := dialHubRPC(t, hub)
	defer clientA.Close()
	clientB := dialHubRPC(t, hub)
	defer clientB.Close()
	for _, client := range []*appwire.Client{clientA, clientB} {
		mustInitializeHubClient(t, client)
	}

	cwd := testfs.CanonicalTempDir(t)
	var result appwire.LaunchConfigResolved
	if err := clientA.Request(context.Background(), appwire.MethodEvenerLaunchSetLayer, appwire.LaunchConfigSetLayerParams{
		CWD: cwd, Layer: "global", Config: appwire.LaunchConfigLayer{Model: "openai/gpt-5"},
	}, &result); err != nil {
		t.Fatalf("evener/launch/setLayer: %v", err)
	}
	for _, client := range []*appwire.Client{clientA, clientB} {
		got := receiveLaunchUpdated(t, client)
		if got.CWD != cwd || got.Layer != "global" {
			t.Fatalf("launch/updated = %+v, want cwd %q layer global", got, cwd)
		}
		assertNoLaunchNotification(t, client)
	}
}

func receiveLaunchUpdated(t *testing.T, client *appwire.Client) appwire.EvenerLaunchUpdatedParams {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		if notification.Method != appwire.NotifyEvenerLaunchUpdated {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerLaunchUpdated)
		}
		var params appwire.EvenerLaunchUpdatedParams
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode launch/updated: %v", err)
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the launch/updated notification")
		return appwire.EvenerLaunchUpdatedParams{}
	}
}

func assertNoLaunchNotification(t *testing.T, client *appwire.Client) {
	t.Helper()
	select {
	case notification := <-client.Notifications():
		t.Fatalf("unexpected second notification %q", notification.Method)
	default:
	}
}
