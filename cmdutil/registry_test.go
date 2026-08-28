package cmdutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

// isolateRoots points XDG_CONFIG_HOME, XDG_STATE_HOME, and HOME at fresh
// temp dirs and clears the two EVENER_*_CONFIG env vars so each test starts
// from a clean, predictable tri-state. t.Setenv("", ...) before os.Unsetenv
// registers the pre-test value for cleanup-time restore; os.Unsetenv alone
// (without the preceding t.Setenv) would leave the var unset for the rest of
// the test binary once this test returns.
func isolateRoots(t *testing.T) (configRoot, stateRoot string) {
	t.Helper()
	configHome, stateHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"EVENER_PROVIDERS_CONFIG", "EVENER_CREDENTIALS_CONFIG"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	return filepath.Join(configHome, "evener"), filepath.Join(stateHome, "evener")
}

func TestProvidersConfigPathTriState(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if path, none := ProvidersConfigPath(); none || path != filepath.Join(configRoot, "providers.toml") {
		t.Fatalf("unset: %q %v", path, none)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	if path, none := ProvidersConfigPath(); !none || path != "" {
		t.Fatalf("present and empty means no user layer (spec §10): %q %v", path, none)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "/x/providers.toml")
	if path, none := ProvidersConfigPath(); none || path != "/x/providers.toml" {
		t.Fatalf("set: %q %v", path, none)
	}
}

func TestCredentialsPathPrecedence(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if got := CredentialsPath(); got != filepath.Join(configRoot, "credentials.toml") {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "/x/providers.toml")
	if got := CredentialsPath(); got != "/x/credentials.toml" {
		t.Fatalf("sibling of the providers path: %q", got)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	if got := CredentialsPath(); got != filepath.Join(configRoot, "credentials.toml") {
		t.Fatalf("no user layer falls back to the config root: %q", got)
	}
	t.Setenv("EVENER_CREDENTIALS_CONFIG", "/y/creds.toml")
	if got := CredentialsPath(); got != "/y/creds.toml" {
		t.Fatalf("explicit wins: %q", got)
	}
}

func TestLoadRegistryUsesStoreAndUserLayer(t *testing.T) {
	configRoot, stateRoot := isolateRoots(t)
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "providers.toml"), []byte("default = \"work\"\n[providers.work]\nbase = \"openai\"\nbase_url = \"https://gw.example.com/v1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "credentials.toml"), []byte("schema = 1\n[providers.work]\napi_key = \"from-store\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, store, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if store == nil || r.StateRoot() != stateRoot {
		t.Fatalf("store %v, state root %q", store, r.StateRoot())
	}
	res, err := r.Resolve("work/gpt-5.5")
	if err != nil || res.Credential.Source != "store" || res.Credential.Value != "from-store" {
		t.Fatalf("the store's file layer is looked up by instance name (spec §10): %v %+v", err, res.Credential)
	}
	if !strings.Contains(r.UserLayerNote(), "providers.toml") {
		t.Fatalf("user layer note: %q", r.UserLayerNote())
	}
}

func TestLoadRegistryReportsOldSchema(t *testing.T) {
	configRoot, _ := isolateRoots(t)
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "providers.toml"), []byte("default = \"openai\"\n[instances.openai]\ntype = \"openai\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if !errors.Is(err, registry.ErrOldSchema) {
		t.Fatalf("want ErrOldSchema, got %v", err)
	}
}

func TestLoadRegistryHonorsEmptyProvidersConfig(t *testing.T) {
	isolateRoots(t)
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	r, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.UserLayerNote(), "EVENER_PROVIDERS_CONFIG is empty") {
		t.Fatalf("tri-state note: %q", r.UserLayerNote())
	}
}

func TestNewRegistryClientWiresTheProcessSeams(t *testing.T) {
	_, stateRoot := isolateRoots(t)
	r, _, err := LoadRegistry(registry.WithOffline(true), registry.WithoutCache())
	if err != nil {
		t.Fatal(err)
	}
	c := NewRegistryClient(r, t.TempDir())
	if c == nil || c.Registry() != r {
		t.Fatal("client carries the registry")
	}
	if tokenauth.DefaultCodex.StateDir != stateRoot || tokenauth.ClientVersion != buildinfo.Version() {
		t.Fatalf("seams: %q %q", tokenauth.DefaultCodex.StateDir, tokenauth.ClientVersion)
	}
}
