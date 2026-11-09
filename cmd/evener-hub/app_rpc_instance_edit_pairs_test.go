package hub

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// An evener/instance/edit request that carries a setter and its clear flag for
// the same field used to drop the setter silently and let the clear win. The
// wire contract says the pair is never both meaningful, so the request must be
// refused before it writes anything (#1025).
func TestHubRPCInstanceEditRefusesASetAndClearPair(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	before, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	credsStore := newTestCredentialsStore(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            newTestRegistry(t, t.TempDir(), tomlPath, credsStore, nil),
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	cases := []struct {
		field  string
		params appwire.InstanceEditParams
	}{
		{"baseUrl", appwire.InstanceEditParams{Name: "base", BaseURL: "https://example.test/v1", ClearBaseURL: true}},
		{"protocol", appwire.InstanceEditParams{Name: "base", Protocol: "openai-responses", ClearProtocol: true}},
		{"surface", appwire.InstanceEditParams{Name: "base", Surface: "generic", ClearSurface: true}},
		{"apiKeyEnv", appwire.InstanceEditParams{Name: "base", APIKeyEnv: "PORTKEY_KEY", ClearAPIKeyEnv: true}},
		{"credentialHeader", appwire.InstanceEditParams{Name: "base", CredentialHeader: "Authorization=Bearer $PORTKEY_KEY", ClearCredentialHeader: true}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			var resp appwire.InstanceListResponse
			err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, tc.params, &resp)
			requireWireError(t, err, appwire.CodeInvalidParams)
		})
	}

	after, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("providers.toml changed after refused edit pairs:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The refusal is per field: a setter on one field beside a clear on another is
// the normal sheet diff and must still land, and a lone clear still applies.
func TestHubRPCInstanceEditAcceptsSetAndClearOnDifferentFields(t *testing.T) {
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
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	var resp appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{
		Name:         "base",
		Protocol:     "openai-responses",
		ClearSurface: true,
	}, &resp); err != nil {
		t.Fatalf("set protocol beside clear surface: %v", err)
	}
	if got := instanceProtocol(t, resp, "base"); got != "openai-responses" {
		t.Fatalf("protocol = %q, want the set value", got)
	}

	resp = appwire.InstanceListResponse{}
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceEdit, appwire.InstanceEditParams{
		Name:          "base",
		ClearProtocol: true,
	}, &resp); err != nil {
		t.Fatalf("lone clear: %v", err)
	}
	// A cleared authorship falls back to the base provider's protocol, so the
	// wire entry still reports one; what must be gone is the authored value.
	if got := authoredEntry(t, tomlPath, "base").Protocol; got != "" {
		t.Fatalf("authored protocol after lone clear = %q, want empty", got)
	}
}

func instanceProtocol(t *testing.T, resp appwire.InstanceListResponse, name string) string {
	t.Helper()
	for _, inst := range resp.Instances {
		if inst.Name == name {
			return inst.Protocol
		}
	}
	t.Fatalf("instance %q missing from %+v", name, resp.Instances)
	return ""
}
