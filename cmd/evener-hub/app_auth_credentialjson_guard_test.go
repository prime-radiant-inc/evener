package hub

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// vertexRemovedInstanceToml authors a gcp-adc instance under a name of its own,
// so the probe can remove it without depending on whether any curated provider
// happens to share the name.
const vertexRemovedInstanceToml = `[providers.vertex-removed]
base = "google-vertex"
[providers.vertex-removed.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"
`

// TestAuth_CredentialJsonSet_RequiresGCPADCImpliesConnectable probes the claimed
// hole: that CredentialJsonSet skips the nameIsConnectable guard ApiKeySet
// enforces (app_auth.go:491), so a credential-JSON write with an empty
// fingerprint assertion stores a secret under a name nothing reads after the
// instance was removed.
//
// The probe needs no guard fix to pass: CredentialJsonSet calls requiresGCPADC
// both before (app_auth.go:796) and inside (app_auth.go:809) the credential
// lock, and requiresGCPADC (app_auth.go:828) passes only when
// instanceUsesGCPADC (app_auth.go:780) is true, which requires
// instanceAuthScheme (app_auth.go:707) to find the name - exactly the lookup
// nameIsConnectable (app_auth.go:730) answers for. Both cases below therefore
// expect a refusal and an empty store; a store hit would make the finding real.
func TestAuth_CredentialJsonSet_RequiresGCPADCImpliesConnectable(t *testing.T) {
	t.Run("a name nothing owns", func(t *testing.T) {
		ctrl, dir := newVertexController(t)
		if ctrl.nameIsConnectable("ghost-provider") {
			t.Fatal("probe fixture: ghost-provider is connectable, so it cannot stand for a name nothing reads")
		}
		if ctrl.instanceUsesGCPADC("ghost-provider") {
			t.Fatal("probe fixture: ghost-provider uses gcp-adc, so requiresGCPADC would not refuse it")
		}
		_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{
			Provider:                    "ghost-provider",
			Value:                       authorizedUserJSON,
			ExpectedEndpointFingerprint: "",
		})
		if err == nil {
			t.Fatal("CredentialJsonSet stored a credential JSON for a name nothing owns")
		}
		if !strings.Contains(err.Error(), "application-default") {
			t.Fatalf("err = %v; want the requiresGCPADC refusal", err)
		}
		assertCredentialJSONNotStored(t, dir, "ghost-provider")
	})

	t.Run("a gcp-adc instance removed since it was authored", func(t *testing.T) {
		oaitest.IsolateOpenAIAuth(t)
		dir := t.TempDir()
		tomlPath := writeProvidersToml(t, dir, vertexRemovedInstanceToml)
		ctrl := newTestAuthController(t, dir, t.TempDir(), tomlPath, map[string]string{"HOME": t.TempDir()})

		if !ctrl.instanceUsesGCPADC("vertex-removed") || !ctrl.nameIsConnectable("vertex-removed") {
			t.Fatal("probe fixture: vertex-removed should be a gcp-adc instance before removal")
		}
		// A removal rewrites providers.toml and reloads the registry; do the same.
		writeProvidersToml(t, dir, "")
		if err := ctrl.reloadRegistry(); err != nil {
			t.Fatalf("reload after removal: %v", err)
		}
		if ctrl.nameIsConnectable("vertex-removed") {
			t.Fatal("probe fixture: vertex-removed still connectable after removal")
		}
		if ctrl.instanceUsesGCPADC("vertex-removed") {
			t.Fatal("probe fixture: removal did not take; requiresGCPADC would still pass")
		}
		_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{
			Provider:                    "vertex-removed",
			Value:                       authorizedUserJSON,
			ExpectedEndpointFingerprint: "",
		})
		if err == nil {
			t.Fatal("CredentialJsonSet stored a credential JSON for a removed instance with an empty fingerprint assertion")
		}
		assertCredentialJSONNotStored(t, dir, "vertex-removed")
	})
}

func assertCredentialJSONNotStored(t *testing.T, dir, name string) {
	t.Helper()
	store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if v, ok := store.Get(name); ok {
		t.Fatalf("a refused credential JSON landed under %q: %q", name, v)
	}
}
