package hub

// ApiKeyClear is the instance sheet's narrow counterpart to Logout (issue
// #713): a stray stored key can sit shadowed behind an active oauth/adc
// credential - a Codex instance signed in via OAuth, or an ADC-resolved
// instance, that also carries a leftover credentials.toml entry from before
// that login/ADC took over (TestAuth_OpenAI_Status_OAuthShadowsStoredFileKey
// renders exactly this state). Logout's Codex branch removes whichever
// layer IS active, which for a signed-in row means the OAuth record, not
// the stray key (TestAuth_Codex_Logout_OAuthRemovalLeavesNoCredential).
// ApiKeyClear always clears the store layer only, regardless of what is
// active, so it can never take down a live sign-in.

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// TestAuth_ApiKeyClear_LeavesActiveOAuthRecordIntact is the exact shape #713
// reported: a Codex instance signed in via OAuth with a stray stored key
// shadowed behind it. Clearing the stored key must drop the file entry
// without touching the live OAuth record.
func TestAuth_ApiKeyClear_LeavesActiveOAuthRecordIntact(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := authopenai.SaveAuth(c.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
		AccessToken: "acc", RefreshToken: "ref",
		Expiry: time.Now().Add(time.Hour), Email: "o@example.com",
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	got, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	if got.ActiveSource != authopenai.AuthSourceOAuth || !got.SignedIn {
		t.Fatalf("status=%+v, want the OAuth login to stay active", got)
	}
	if !got.HasStoredOAuth {
		t.Errorf("status=%+v, want the OAuth record still present", got)
	}
	if got.HasStoredFile {
		t.Errorf("status=%+v, want the stored key gone", got)
	}
	if v, ok := store.Get("openai-codex"); ok {
		t.Errorf("store still has a key: %q", v)
	}
}

// TestAuth_ApiKeyClear_NonCodexMatchesLogout: on a plain apiKey-mode row
// (no OAuth record ever exists to protect), clearing the stored key is the
// same operation Logout already performs - ApiKeyClear must agree.
func TestAuth_ApiKeyClear_NonCodexMatchesLogout(t *testing.T) {
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err := store.Set("anthropic", "sk-ant-test"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)

	got, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	if got.SignedIn || got.ActiveSource != "none" {
		t.Fatalf("status=%+v, want signed out after clearing the only credential", got)
	}
	if got.HasStoredFile {
		t.Errorf("status=%+v, want the stored key gone", got)
	}
	if v, ok := store.Get("anthropic"); ok {
		t.Errorf("store still has a key: %q", v)
	}
}

// TestAuth_ApiKeyClear_PreservesADC is #713's other reported shape: an
// instance resolved via Application Default Credentials with a stray stored
// key shadowed behind it. adcCredentialsFile and the gcp-adc providers.toml
// shape are shared with the status/adc wire fixture scenario
// (app_auth_wire_fixture_test.go), which exercises the same registry path.
func TestAuth_ApiKeyClear_PreservesADC(t *testing.T) {
	credsDir := t.TempDir()
	preStore, err := credentials.LoadStore(filepath.Join(credsDir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := preStore.Set("vertexish", "sk-stray"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	providersPath := writeProvidersToml(t, credsDir,
		"[providers.vertexish]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\nauth = \"gcp-adc\"\n")
	c := newTestAuthController(t, credsDir, t.TempDir(), providersPath,
		map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": adcCredentialsFile(t)})

	got, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "vertexish"})
	if err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	if got.ActiveSource != "adc" || !got.SignedIn {
		t.Fatalf("status=%+v, want ADC to stay active", got)
	}
	if got.HasStoredFile {
		t.Errorf("status=%+v, want the stray key gone", got)
	}
	if v, ok := c.creds.Get("vertexish"); ok {
		t.Errorf("store still has a key: %q", v)
	}
}

// TestAuth_ApiKeyClear_NoStoredKeyIsNoop: clearing an instance with nothing
// stored is a no-op, not an error - Store.Clear is documented to accept an
// absent entry, and the sheet's action must be safe to offer even when the
// wire momentarily disagrees about hasStoredFile.
func TestAuth_ApiKeyClear_NoStoredKeyIsNoop(t *testing.T) {
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	attachTestRegistry(t, c)

	got, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	if got.HasStoredFile {
		t.Errorf("status=%+v, want no stored file", got)
	}
}

// TestAuth_ApiKeyClear_EmptyProviderMeansCodex mirrors
// TestAuth_EmptyProviderMeansCodex: every evener/auth/* method treats a
// blank provider as the default Codex instance name.
func TestAuth_ApiKeyClear_EmptyProviderMeansCodex(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	attachTestRegistry(t, c)
	if err := store.Set("openai-codex", "sk-test-123"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	if _, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: ""}); err != nil {
		t.Fatalf("ApiKeyClear: %v", err)
	}
	if v, ok := store.Get("openai-codex"); ok {
		t.Errorf("store still has a key: %q", v)
	}
}
