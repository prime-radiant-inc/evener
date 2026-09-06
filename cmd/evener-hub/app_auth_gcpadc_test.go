package hub

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
)

// vertexInstanceToml is one gcp-adc instance under a custom name with its
// project and location as instance vars, the shape the hub's add dialog writes.
const vertexInstanceToml = `[providers.vertex]
base = "google-vertex"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"
`

// authorizedUserJSON is a syntactically valid Google credential with no real
// secret in it; google.CredentialsFromJSON accepts the shape without any
// network or key parsing.
const authorizedUserJSON = `{"type":"authorized_user","client_id":"cid","client_secret":"csecret","refresh_token":"rtoken"}`

func newVertexController(t *testing.T) (*hubAuthController, string) {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	// HOME points at an empty directory so no ADC file resolves.
	ctrl := newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, vertexInstanceToml), map[string]string{"HOME": t.TempDir()})
	return ctrl, dir
}

func TestAuth_CredentialJsonSet_StoresForGCPADCInstance(t *testing.T) {
	ctrl, dir := newVertexController(t)
	before, err := ctrl.Status(appwire.AuthStatusParams{Provider: "vertex"})
	if err != nil || before.SignedIn || before.ActiveSource != "none" {
		t.Fatalf("before = %+v err=%v; want unconfigured", before, err)
	}
	got, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{Provider: "vertex", Value: authorizedUserJSON})
	if err != nil {
		t.Fatalf("CredentialJsonSet: %v", err)
	}
	if got.Provider != "vertex" || !got.SignedIn || got.ActiveSource != "store" || !got.HasStoredFile {
		t.Fatalf("status = %+v; want signed in from the store", got)
	}
	if strings.Join(got.AuthModes, ",") != "adc,credentialJson" {
		t.Fatalf("authModes = %v", got.AuthModes)
	}
	store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := store.Get("vertex"); !ok || v != authorizedUserJSON {
		t.Fatalf("stored = %q ok=%v", v, ok)
	}
}

func TestAuth_CredentialJsonSet_RejectsMalformedJSON(t *testing.T) {
	ctrl, dir := newVertexController(t)
	_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{Provider: "vertex", Value: `{"type":"authorized_user"`})
	if err == nil || !strings.Contains(err.Error(), "credential JSON") {
		t.Fatalf("err = %v; want an invalid-params error naming the credential JSON", err)
	}
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if _, ok := store.Get("vertex"); ok {
		t.Fatal("a rejected paste must not be stored")
	}
}

func TestAuth_CredentialJsonSet_RejectsCredentialWithoutKeyMaterial(t *testing.T) {
	ctrl, dir := newVertexController(t)
	_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{Provider: "vertex", Value: `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com"}`})
	if err == nil || !strings.Contains(err.Error(), "missing private_key") {
		t.Fatalf("err = %v; want a refusal naming the missing key material", err)
	}
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if _, ok := store.Get("vertex"); ok {
		t.Fatal("a rejected paste must not be stored")
	}
}

func TestAuth_CredentialJsonSet_RejectsUnusableKeyMaterial(t *testing.T) {
	ctrl, dir := newVertexController(t)
	_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{Provider: "vertex", Value: `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`})
	if err == nil || !strings.Contains(err.Error(), "unusable private_key") {
		t.Fatalf("err = %v; want a refusal naming the key material the signer cannot parse", err)
	}
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if _, ok := store.Get("vertex"); ok {
		t.Fatal("a rejected paste must not be stored")
	}
}

func TestAuth_CredentialJsonSet_RefusesNonGCPADCInstance(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, bearerInstanceToml))
	_, err := ctrl.CredentialJsonSet(appwire.AuthCredentialJsonSetParams{Provider: "work-ant", Value: authorizedUserJSON})
	if err == nil || !strings.Contains(err.Error(), "apiKey/set") {
		t.Fatalf("err = %v; want a refusal pointing at apiKey/set", err)
	}
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if _, ok := store.Get("work-ant"); ok {
		t.Fatal("a refused credential JSON must not be stored")
	}
}

func TestAuth_ApiKeySet_RefusesGCPADCInstance(t *testing.T) {
	ctrl, dir := newVertexController(t)
	_, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "vertex", Value: "AQ.not-a-json"})
	if err == nil || !strings.Contains(err.Error(), "credentialJson/set") {
		t.Fatalf("err = %v; want a refusal pointing at credentialJson/set", err)
	}
	store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if _, ok := store.Get("vertex"); ok {
		t.Fatal("a refused key must not be stored")
	}
}
