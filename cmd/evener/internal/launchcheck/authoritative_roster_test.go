package launchcheck

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/auth/githubcopilot"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// rosterServer starts a /models endpoint answering with status and body and
// returns its base URL.
func rosterServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// loadRosterClient loads a client from a one-instance providers.toml.
func loadRosterClient(t *testing.T, cfg string) *llm.Client {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(cfgPath), registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

// authoritativeRosterClient builds a client for an account-owned Copilot
// instance whose roster endpoint is the test's server. The stored future-dated
// session is the credential the process-global Copilot authenticator resolves
// through XDG_STATE_HOME, which the test isolates.
func authoritativeRosterClient(t *testing.T, status int, body string) *llm.Client {
	t.Helper()
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	stateRoot := filepath.Join(stateHome, "evener")
	if err := githubcopilot.SaveAuth(stateRoot, "copilot-plan", githubcopilot.AuthRecord{
		Instance: "copilot-plan", Scheme: githubcopilot.AuthScheme, Version: 1,
		AccessToken: "access", CopilotToken: "copilot", AccountID: "account-1",
		ObtainedAt: time.Now().Add(-time.Hour), CopilotExpiry: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	baseURL := rosterServer(t, status, body)
	return loadRosterClient(t, "[providers.copilot-plan]\nbase = \"github-copilot\"\nbase_url = \""+baseURL+"\"\n")
}

// TestLaunchCheckRejectsModelWhenAuthoritativeRosterIsEmpty pins the account
// roster rule at the launch check: a successful answer with no usable row is
// proof the account offers nothing, so the requested model cannot launch.
func TestLaunchCheckRejectsModelWhenAuthoritativeRosterIsEmpty(t *testing.T) {
	client := authoritativeRosterClient(t, http.StatusOK, `{"data":[]}`)
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	ref := cmdutil.ModelRef{Provider: "copilot-plan", Model: "gpt-5.6-sol"}
	if !client.Registry().LiveListingAuthoritative(ref.Provider) {
		t.Fatal("fixture precondition: the Copilot instance's roster must be authoritative")
	}
	if !client.CanServe(ref.Provider, ref.Model) {
		t.Fatal("fixture precondition: the saved model must resolve on the bundled catalog")
	}
	err := validateLaunchCheckModel(ref)
	if err == nil {
		t.Fatal("launch check accepted a model although the account roster answered with no usable row")
	}
}

// TestLaunchCheckAcceptsModelWhenAuthoritativeRosterFetchFails pins the other
// half of the rule: a roster the instance could not fetch proves nothing
// about availability, so the launch check still passes.
func TestLaunchCheckAcceptsModelWhenAuthoritativeRosterFetchFails(t *testing.T) {
	client := authoritativeRosterClient(t, http.StatusInternalServerError, `{"message":"boom"}`)
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	ref := cmdutil.ModelRef{Provider: "copilot-plan", Model: "gpt-5.6-sol"}
	if err := validateLaunchCheckModel(ref); err != nil {
		t.Fatalf("a failed roster fetch is not evidence of absence: %v", err)
	}
}

// TestLaunchCheckAcceptsModelWhenRosterIsNotAuthoritative pins the
// non-authoritative control: an instance whose answer is not the account's
// catalog falls back to its static rows, so an empty answer proves nothing.
func TestLaunchCheckAcceptsModelWhenRosterIsNotAuthoritative(t *testing.T) {
	baseURL := rosterServer(t, http.StatusOK, `{"data":[]}`)
	client := loadRosterClient(t, "[providers.gateway]\nbase = \"openai-compatible\"\nbase_url = \""+baseURL+"\"\napi_key = \"test-key\"\n")
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	ref := cmdutil.ModelRef{Provider: "gateway", Model: "gpt-5.6-sol"}
	if err := validateLaunchCheckModel(ref); err != nil {
		t.Fatalf("a non-authoritative listing is not evidence of absence: %v", err)
	}
}
