package hub

// An auth mutation from a client that names itself must come back to every
// connected client carrying that same id, so the originator can recognise its
// own echo by id instead of by provider plus timing. These two tests pin that
// round trip at the level where it is a wire fact: a real hub, a real RPC
// client, evener/auth/apiKey/set through the registered handler, and the
// evener/auth/updated notification the client receives off the socket.
//
// The second test is the control for the first: the same mutation with no
// OriginClientId must produce a broadcast with no id at all, which is what
// proves the id in the first test is the caller's own value echoed back rather
// than something the hub invents or leaves behind.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// newAuthOriginTestClient stands up a hub of its own - registry and credential
// store both rooted in this test's temp dirs, the shape
// TestHubRPCInstanceCreateBroadcastsAuthUpdated uses - and returns a connected,
// initialised RPC client for it. One client per hub means the single client
// connection is both the originator of the mutation and the recipient of the
// broadcast, so the notification read below can only have come from this
// client's own call.
func newAuthOriginTestClient(t *testing.T) *appwire.Client {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { client.Close() })

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client
}

// waitForAuthUpdated reads notifications until evener/auth/updated arrives and
// decodes its params. The hub is free to emit other notifications, so anything
// else is skipped and named in the failure message rather than mistaken for the
// broadcast under test; a mutation that broadcasts nothing at all still fails
// here on the timeout.
func waitForAuthUpdated(t *testing.T, client *appwire.Client) appwire.EvenerAuthUpdatedParams {
	t.Helper()
	var skipped []string
	timeout := time.After(2 * time.Second)
	for {
		select {
		case got, ok := <-client.Notifications():
			if !ok {
				t.Fatalf("notification channel closed after seeing %v; want an %s broadcast", skipped, appwire.NotifyEvenerAuthUpdated)
			}
			if got.Method != appwire.NotifyEvenerAuthUpdated {
				skipped = append(skipped, got.Method)
				continue
			}
			var params appwire.EvenerAuthUpdatedParams
			if err := json.Unmarshal(got.Params, &params); err != nil {
				t.Fatalf("decode %s params %s: %v", got.Method, got.Params, err)
			}
			if len(skipped) > 0 {
				t.Logf("skipped %v while waiting for %s", skipped, appwire.NotifyEvenerAuthUpdated)
			}
			return params
		case <-timeout:
			t.Fatalf("timed out waiting for an %s broadcast after the auth mutation (saw: %v)", appwire.NotifyEvenerAuthUpdated, skipped)
		}
	}
}

// TestAuthApiKeySetBroadcastEchoesOriginClientId drives evener/auth/apiKey/set
// with OriginClientId "tab-a" through the registered RPC handler and requires
// the evener/auth/updated broadcast it triggers to carry that same id.
func TestAuthApiKeySetBroadcastEchoesOriginClientId(t *testing.T) {
	client := newAuthOriginTestClient(t)

	var resp appwire.AuthStatusResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthApiKeySet,
		appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-tab-a", OriginClientId: "tab-a"}, &resp); err != nil {
		t.Fatalf("evener/auth/apiKey/set: %v", err)
	}
	// A write that did not land still answers 200-ish, and only a landed write
	// proves the broadcast below belongs to this mutation.
	if resp.ActiveSource != "store" {
		t.Fatalf("evener/auth/apiKey/set answered activeSource=%q, want %q: the key never resolved from the store the handler wrote", resp.ActiveSource, "store")
	}

	params := waitForAuthUpdated(t, client)
	if params.OriginClientId != "tab-a" {
		t.Errorf("%s params=%+v: originClientId=%q, want %q (the caller's own id echoed back)",
			appwire.NotifyEvenerAuthUpdated, params, params.OriginClientId, "tab-a")
	}
}

// TestAuthApiKeySetBroadcastWithoutOriginClientIdHasNone is the control for
// TestAuthApiKeySetBroadcastEchoesOriginClientId: the identical mutation with no
// OriginClientId must broadcast an id that is empty, so the id the first test
// observes is the caller's value and not one the hub supplies on its own. The
// broadcast itself is still required, so an empty id cannot come from a missing
// notification.
func TestAuthApiKeySetBroadcastWithoutOriginClientIdHasNone(t *testing.T) {
	client := newAuthOriginTestClient(t)

	var resp appwire.AuthStatusResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthApiKeySet,
		appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "sk-no-origin"}, &resp); err != nil {
		t.Fatalf("evener/auth/apiKey/set: %v", err)
	}
	if resp.ActiveSource != "store" {
		t.Fatalf("evener/auth/apiKey/set answered activeSource=%q, want %q: the key never resolved from the store the handler wrote", resp.ActiveSource, "store")
	}

	params := waitForAuthUpdated(t, client)
	if params.OriginClientId != "" {
		t.Errorf("%s params=%+v: originClientId=%q, want empty: this caller sent none, so the broadcast must not name one",
			appwire.NotifyEvenerAuthUpdated, params, params.OriginClientId)
	}
}
