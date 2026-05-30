package main

// Tests for Phase 2: re-keying credential RPCs by instance name.
//
// Each test uses a temp dir, a temp providers.toml, and SERF_STATE_DIR /
// XDG_STATE_HOME so that auth files and credentials land in isolated dirs.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/internal/credentials"
)

// writeProvidersToml writes a minimal providers.toml to path.
func writeProvidersToml(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
	return path
}

// newTestAuthController creates an isolated hubAuthController with:
//   - credentials store at credsDir/credentials.toml
//   - stateDir = stateDir (for OAuth files)
//   - providersConfigPath = providersToml
func newTestAuthController(t *testing.T, credsDir, stateDir, providersToml string) *hubAuthController {
	t.Helper()
	credsPath := filepath.Join(credsDir, "credentials.toml")
	store, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	ctrl := newHubAuthControllerWithStore(credsDir, store)
	ctrl.stateDir = stateDir
	ctrl.providersConfigPath = providersToml
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

// ─────────────────────────────────────────────────────────────────────────────
// 1. ApiKeySet for a named openai-type instance writes credentials.toml[name]
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceApiKeySet_WritesNamedKey(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work]
type = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	got, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work", Value: "sk-work-key"})
	if err != nil {
		t.Fatalf("ApiKeySet(work): %v", err)
	}
	if got.Provider != "work" {
		t.Errorf("Provider = %q, want %q", got.Provider, "work")
	}
	if got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("ActiveSource = %q, want file", got.ActiveSource)
	}

	// Verify the key is stored under the instance name "work", not "openai".
	credsPath := filepath.Join(dir, "credentials.toml")
	store2, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	v, src := store2.Get("work")
	if v != "sk-work-key" || src != credentials.SourceFile {
		t.Errorf("credentials.toml[work] = %q/%q, want sk-work-key/file", v, src)
	}
	// "openai" slot should be untouched.
	v2, _ := store2.Get("openai")
	if v2 != "" {
		t.Errorf("credentials.toml[openai] should be empty, got %q", v2)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. OAuth ops on a named openai-type instance target auth/<name>.json
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceStatus_OpenAIOAuthTargetsNamedFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work]
type = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	// Write an OAuth record for "work", not "openai".
	if err := authopenai.SaveAuth(stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}

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

	// auth/openai.json must be absent — we only wrote work.json.
	openaiPath := authopenai.AuthFilePath(stateDir, "openai")
	if _, err := os.Stat(openaiPath); !os.IsNotExist(err) {
		t.Errorf("auth/openai.json should not exist; stat err=%v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. OAuth ops on a non-openai instance (anthropic-type) are rejected
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceDeviceStart_RejectsNonOpenAI(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work-ant]
type = "anthropic"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	_, err := ctrl.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "work-ant"})
	if err == nil {
		t.Fatal("DeviceStart(work-ant) expected error, got nil")
	}
}

func TestAuth_InstanceLoginStart_RejectsNonOpenAI(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work-ant]
type = "anthropic"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	_, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "work-ant"})
	if err == nil {
		t.Fatal("LoginStart(work-ant) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. instanceStatus helper reflects correct activeSource for named instance
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceStatus_ReflectsActiveSourceForNamedInstance(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work]
type = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	// No credentials yet: openai-type reports "signed-out" when no OAuth/file/env.
	got := ctrl.instanceStatus("work", "openai")
	if got.ActiveSource != authopenai.AuthSourceSignedOut {
		t.Errorf("ActiveSource = %q, want signed-out (no creds)", got.ActiveSource)
	}

	// Set a file key for "work".
	if err := ctrl.creds.Set("work", "sk-w"); err != nil {
		t.Fatalf("Set(work): %v", err)
	}
	got = ctrl.instanceStatus("work", "openai")
	if got.ActiveSource != string(credentials.SourceFile) || !got.HasStoredFile {
		t.Errorf("ActiveSource = %q HasStoredFile = %v, want file/true", got.ActiveSource, got.HasStoredFile)
	}
}

func TestAuth_InstanceStatus_AnthropicTypeReportsKeyOnly(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work-ant]
type = "anthropic"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)
	if err := ctrl.creds.Set("work-ant", "sk-ant"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := ctrl.instanceStatus("work-ant", "anthropic")
	if got.ActiveSource != string(credentials.SourceFile) {
		t.Errorf("ActiveSource = %q, want file", got.ActiveSource)
	}
	if got.HasStoredOAuth {
		t.Errorf("HasStoredOAuth should be false for anthropic type")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Backward compat: default instance (name == type) still works
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_DefaultInstance_NameEqualsTypeStillWorks(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.openai]
type = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	// Write OAuth for "openai" (default instance).
	if err := authopenai.SaveAuth(stateDir, "openai", makeOAuthRecord("openai", "default@example.com")); err != nil {
		t.Fatalf("SaveAuth(openai): %v", err)
	}

	got, err := ctrl.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err != nil {
		t.Fatalf("Status(openai): %v", err)
	}
	if !got.SignedIn || got.ActiveSource != authopenai.AuthSourceOAuth {
		t.Fatalf("status=%+v, want signed-in oauth for default instance", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Logout for named openai-type instance removes auth/<name>.json
// ─────────────────────────────────────────────────────────────────────────────

func TestAuth_InstanceLogout_RemovesNamedOAuthFile(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	stateDir := t.TempDir()
	providersToml := writeProvidersToml(t, dir, `
schema = 1

[instances.work]
type = "openai"
`)
	ctrl := newTestAuthController(t, dir, stateDir, providersToml)

	if err := authopenai.SaveAuth(stateDir, "work", makeOAuthRecord("work", "w@example.com")); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}

	resp, err := ctrl.Logout(appwire.AuthLogoutParams{Provider: "work"})
	if err != nil {
		t.Fatalf("Logout(work): %v", err)
	}
	if !resp.Removed {
		t.Errorf("Removed = false, want true")
	}

	// auth/work.json should no longer exist.
	workPath := authopenai.AuthFilePath(stateDir, "work")
	if _, err := os.Stat(workPath); !os.IsNotExist(err) {
		t.Errorf("auth/work.json still exists after logout; stat err=%v", err)
	}
}
