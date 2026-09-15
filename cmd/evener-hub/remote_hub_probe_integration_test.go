package hub

import (
	"context"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestRemoteHubProbeAgainstRealHub drives HostCapabilities through the hub's
// real AppWire edge, so the launch-layer read runs the exact
// hubLaunchController.GetLayer validation the remote hub serves instead of a
// permissive canned responder. GetLayer canonicalizes params.CWD before it
// dispatches on the layer, and fspaths.CanonicalizeDir rejects the empty
// string, so a probe that sends an empty CWD fails with
// InvalidParams("cwd: path is empty") against every real hub. This test is the
// regression lock for that: it fails unless the probe sends a CWD a real hub
// accepts.
func TestRemoteHubProbeAgainstRealHub(t *testing.T) {
	// ProvidersConfigPath must be set: the hub only registers
	// evener/instance/list when it has one, and the probe reads that surface.
	server, _ := newHubRPCTestServerWithWeb(t, hubcore.WebConfig{
		ProvidersConfigPath: filepath.Join(t.TempDir(), "providers.toml"),
	})
	t.Cleanup(server.Close)

	ctx := t.Context()
	client := dialHubRPC(t, server)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize against the hub edge: %v", err)
	}

	source := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	caps, err := source.HostCapabilities(ctx)
	if err != nil {
		t.Fatalf("HostCapabilities against a real hub: %v", err)
	}
	if caps.HubSourceID != "local" {
		t.Fatalf("HubSourceID = %q, want local", caps.HubSourceID)
	}
}
