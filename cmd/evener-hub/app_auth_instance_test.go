package hub

// Instance-keyed auth: credentials and OAuth records belong to an instance
// name, never to the provider it is based on, and only the Codex transport
// has an OAuth flow at all (spec §9.5, §10).

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// writeProvidersToml writes content to dir/providers.toml and returns the path.
func writeProvidersToml(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	return path
}

// newTestRegistry builds a hermetic registry holder over providersToml: no
// network, no catalog cache, and only the env the test hands it.
func newTestRegistry(t *testing.T, stateDir, providersToml string, store *credentials.Store, env map[string]string) *hubcore.ProviderRegistry {
	t.Helper()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		opts := []registry.Option{
			registry.WithOffline(true),
			registry.WithoutCache(),
			registry.WithStateRoot(stateDir),
			registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
			registry.WithEnv(func(name string) (string, bool) {
				v, ok := env[name]
				return v, ok
			}),
		}
		if providersToml != "" {
			opts = append(opts, registry.WithConfigPath(providersToml))
		} else {
			opts = append(opts, registry.WithNoUserLayer())
		}
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	return holder
}

// newTestAuthController creates an isolated hubAuthController with:
//   - credentials store at credsDir/credentials.toml
//   - stateDir = stateDir (for OAuth records)
//   - a registry reading providersToml against the optional env
func newTestAuthController(t *testing.T, credsDir, stateDir, providersToml string, env ...map[string]string) *hubAuthController {
	t.Helper()
	credsPath := filepath.Join(credsDir, "credentials.toml")
	store, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	lookup := map[string]string{}
	if len(env) > 0 {
		lookup = env[0]
	}
	ctrl := newHubAuthControllerWithStore(credsDir, store)
	ctrl.stateDir = stateDir
	ctrl.providersConfigPath = providersToml
	ctrl.reg = newTestRegistry(t, stateDir, providersToml, store, lookup)
	return ctrl
}

// makeOAuthRecord returns a valid unexpired OAuth record for the given instanceName.
func makeOAuthRecord(instanceName, email string) authopenai.AuthRecord {
	return authopenai.AuthRecord{
		Version:      1,
		Provider:     instanceName,
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   time.Now().Add(-time.Hour),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		AccessToken:  "access-" + instanceName,
		RefreshToken: "refresh-" + instanceName,
		Expiry:       time.Now().Add(time.Hour),
		Email:        email,
	}
}

// codexInstanceToml is one instance on the Codex transport under a name that
// is not the registry id, which is the shape every OAuth path must handle.
const codexInstanceToml = `[providers.work]
base = "openai-codex"
`

// bearerInstanceToml is one key-authenticated instance under a custom name.
const bearerInstanceToml = `[providers.work-ant]
base = "anthropic"
api_key_env = ["WORK_ANT_KEY"]
`

// ─────────────────────────────────────────────────────────────────────────────
// 1. ApiKeySet for a named instance writes credentials.toml[name]
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceApiKeySet_WritesNamedKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	got, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work-ant", Value: "sk-work-key"})
	if err != nil {
		t.Fatalf("ApiKeySet(work-ant): %v", err)
	}
	if got.Provider != "work-ant" {
		t.Errorf("Provider = %q, want %q", got.Provider, "work-ant")
	}
	if got.ActiveSource != "store" {
		t.Errorf("ActiveSource = %q, want store", got.ActiveSource)
	}
	if !got.SignedIn || !got.HasStoredFile {
		t.Errorf("status = %+v, want signed in from the store", got)
	}

	// The key is stored under the instance name, not the provider it is based on.
	store2, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	v, src := store2.Get("work-ant")
	if v != "sk-work-key" || src != credentials.SourceFile {
		t.Errorf("credentials.toml[work-ant] = %q/%q, want sk-work-key/file", v, src)
	}
	if v2, _ := store2.Get("anthropic"); v2 != "" {
		t.Errorf("credentials.toml[anthropic] should be empty, got %q", v2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. OAuth ops on a named Codex instance target auth/<name>.json
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceStatus_OAuthTargetsNamedFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	// The record must exist before the registry loads: it is what makes the
	// Codex instance resolvable at all (spec §5.1).
	if err := authopenai.SaveAuth(stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, codexInstanceToml))

	got, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work"})
	if err != nil {
		t.Fatalf("Status(work): %v", err)
	}
	if !got.SignedIn || got.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("status=%+v, want signed-in via OAuth", got)
	}
	if !got.HasStoredOAuth {
		t.Errorf("status=%+v, want HasStoredOAuth=true", got)
	}
	if got.Email != "work@example.com" {
		t.Errorf("Email=%q, want work@example.com", got.Email)
	}

	// auth/openai-codex.json must be absent — we only wrote work.json.
	if _, err := os.Stat(authopenai.AuthFilePath(stateDir, "openai-codex")); !os.IsNotExist(err) {
		t.Errorf("auth/openai-codex.json should not exist; stat err=%v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. OAuth ops on an instance that is not on the Codex transport are rejected
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceOAuthEntry_RejectsNonCodexInstance(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml),
		map[string]string{"WORK_ANT_KEY": "sk-ant"})

	if _, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work-ant"}); err == nil {
		t.Fatal("DeviceStart(work-ant) expected error, got nil")
	}
	if _, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work-ant"}); err == nil {
		t.Fatal("LoginStart(work-ant) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. instanceStatus reports the registry's credential source
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceStatus_ReflectsRegistryCredentialSource(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, bearerInstanceToml))

	// An authored entry is an instance whether or not a credential resolves;
	// it is the source that changes.
	inst, ok := ctrl.reg.Get().Instance("work-ant")
	if !ok {
		t.Fatal("an authored entry is always an instance")
	}
	if inst.CredentialSource != "none" {
		t.Fatalf("credential source = %q with nothing configured, want none", inst.CredentialSource)
	}
	status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work-ant"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Supported || status.SignedIn || status.ActiveSource != "none" {
		t.Fatalf("status = %+v, want supported and signed out", status)
	}

	if err := ctrl.creds.Set("work-ant", "sk-w"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ctrl.reg.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	inst, ok = ctrl.reg.Get().Instance("work-ant")
	if !ok {
		t.Fatal("a stored key makes the instance resolvable")
	}
	got := ctrl.instanceStatus(inst)
	if got.ActiveSource != "store" || !got.HasStoredFile {
		t.Errorf("ActiveSource = %q HasStoredFile = %v, want store/true", got.ActiveSource, got.HasStoredFile)
	}
	if got.HasStoredOAuth {
		t.Error("HasStoredOAuth should be false for a key-authenticated instance")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. An instance whose name is the registry id resolves the same way
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceNamedAfterRegistryID(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, "openai-codex", makeOAuthRecord("openai-codex", "default@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	ctrl := newTestAuthController(t, dir, stateDir, "")

	got, err := ctrl.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("Status(openai-codex): %v", err)
	}
	if !got.SignedIn || got.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("status=%+v, want signed-in oauth for the curated Codex provider", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Logout for a named Codex instance removes auth/<name>.json
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceLogout_RemovesNamedOAuthFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, "work", makeOAuthRecord("work", "w@example.com")); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}
	ctrl := newTestAuthController(t, dir, stateDir, writeProvidersToml(t, dir, codexInstanceToml))

	resp, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: "work"})
	if err != nil {
		t.Fatalf("Logout(work): %v", err)
	}
	if !resp.Removed {
		t.Errorf("Removed = false, want true")
	}
	if _, err := os.Stat(authopenai.AuthFilePath(stateDir, "work")); !os.IsNotExist(err) {
		t.Errorf("auth/work.json still exists after logout; stat err=%v", err)
	}
}

// TestAuth_EmptyProviderMeansCodex pins normalizeAuthProvider's default: the
// pane's OAuth button sends no provider, and that means the Codex instance
// (spec §9.5, §11.3).
func TestAuth_EmptyProviderMeansCodex(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, "openai-codex", makeOAuthRecord("openai-codex", "codex@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	ctrl := newTestAuthController(t, dir, stateDir, "")

	got, err := ctrl.Status(appwire.AuthStatusParams{})
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if got.Provider != "openai-codex" {
		t.Fatalf("Provider = %q, want openai-codex", got.Provider)
	}
	if got.Email != "codex@example.com" {
		t.Fatalf("Email = %q, want the Codex record's", got.Email)
	}
}

// TestAuth_NonCodexLogout_ClearsTheStoredKeyAndReloads is the property the
// deleted behavior-tag file carried: logging out a key-authenticated instance
// clears its stored key, and the registry is reloaded so every surface stops
// reporting a credential that is gone. Nothing else walks this branch with an
// assertion.
//
// The two cases are what make both halves observable. An authored entry is an
// instance whether or not a credential resolves, so it shows the cleared key;
// a curated implicit provider exists only while its credential does, so it is
// the one that shows the reload — its instance-set membership is cached until
// the registry is loaded again (spec §5.1).
func TestAuth_NonCodexLogout_ClearsTheStoredKeyAndReloads(t *testing.T) {
	for _, tt := range []struct {
		name     string
		toml     string
		instance string
		// implicit instances vanish from the set once their credential does.
		staysAnInstance bool
	}{
		{name: "authored instance", toml: bearerInstanceToml, instance: "work-ant", staysAnInstance: true},
		{name: "curated implicit provider", toml: "", instance: "anthropic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			clearProviderKeysFromEnvironment(t)
			dir := t.TempDir()
			path := ""
			if tt.toml != "" {
				path = writeProvidersToml(t, dir, tt.toml)
			}
			ctrl := newTestAuthController(t, dir, t.TempDir(), path)

			if err := ctrl.creds.Set(tt.instance, "sk-ant-stored"); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if err := ctrl.reg.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			before, ok := ctrl.reg.Get().Instance(tt.instance)
			if !ok || before.CredentialSource != "store" {
				t.Fatalf("credential source before logout = %q (present %v), want store", before.CredentialSource, ok)
			}

			resp, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: tt.instance})
			if err != nil {
				t.Fatalf("Logout: %v", err)
			}
			if !resp.Removed {
				t.Error("Removed = false, want true")
			}
			if resp.Status.SignedIn || resp.Status.ActiveSource != "none" {
				t.Errorf("status = %+v, want signed out with source none", resp.Status)
			}
			if !reflect.DeepEqual(resp.Status.AuthModes, []string{"apiKey"}) {
				t.Errorf("AuthModes = %v, want [apiKey]", resp.Status.AuthModes)
			}
			if resp.Status.HasStoredFile {
				t.Error("HasStoredFile = true, want the stored key gone")
			}
			if v, _ := ctrl.creds.Get(tt.instance); v != "" {
				t.Errorf("credentials.toml still holds %q", v)
			}
			// The registry was reloaded, so every other surface agrees the
			// credential is gone.
			after, stillAnInstance := ctrl.reg.Get().Instance(tt.instance)
			switch {
			case tt.staysAnInstance && (!stillAnInstance || after.CredentialSource != "none"):
				t.Fatalf("credential source after logout = %q (present %v), want none", after.CredentialSource, stillAnInstance)
			case !tt.staysAnInstance && stillAnInstance:
				t.Fatalf("%s is still an instance after its only credential was cleared — the registry was not reloaded", tt.instance)
			}
			if err := validateProviderCredentials(tt.instance, ctrl.reg); err == nil {
				t.Error("the spawn gate still accepts an instance whose key was just cleared")
			}
		})
	}
}
