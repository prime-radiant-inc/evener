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
