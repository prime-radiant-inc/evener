package cmdutil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm"
)

// validProvidersToml is a minimal providers.toml with two openai instances
// and a kimi instance for use in LoadClient tests.
const validProvidersToml = `
schema = 1
default = "work"

[instances.work]
type = "openai"
api_style = "responses"
api_key = "sk-work"

[instances.work2]
type = "openai"
api_style = "chat-completions"
api_key = "sk-work2"

[instances.kimi]
type = "kimi"
api_key = "km-key"
`

const corruptToml = `this is not valid toml \x00\x01`

func TestLoadClient_WithValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(validProvidersToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)

	client, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true with a valid config file")
	}
	if len(cfg.Instances) != 3 {
		t.Fatalf("cfg.Instances len=%d, want 3", len(cfg.Instances))
	}
	names := client.ProviderNames()
	sort.Strings(names)
	wantNames := []string{"kimi", "work", "work2"}
	if len(names) != len(wantNames) {
		t.Fatalf("ProviderNames=%v, want %v", names, wantNames)
	}
	for i, n := range wantNames {
		if names[i] != n {
			t.Errorf("ProviderNames[%d]=%q, want %q", i, names[i], n)
		}
	}
}

func TestLoadClient_NoFile_Materializes(t *testing.T) {
	// When providers.toml is absent, LoadClient materializes it and returns
	// hasConfig=true (always-config contract).
	dir := t.TempDir()
	t.Setenv("SERF_PROVIDERS_CONFIG", filepath.Join(dir, "providers.toml"))
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	client, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (absent file): %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true after materialization")
	}
	if len(cfg.Instances) == 0 {
		t.Fatal("expected non-empty Config after materialization")
	}
	if client == nil {
		t.Fatal("expected non-nil client after materialization")
	}
}

func TestLoadClient_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(corruptToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)

	_, _, _, err := LoadClient()
	if err == nil {
		t.Fatal("expected error for corrupt providers.toml, got nil")
	}
}

func TestLoadClient_DefaultPath_UsedWhenEnvNotSet(t *testing.T) {
	// Clear SERF_PROVIDERS_CONFIG so LoadClient uses the default path.
	// Override HOME so the default state root lands in a clean temp dir.
	// After materialization, hasConfig must be true (always-config contract).
	t.Setenv("SERF_PROVIDERS_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	_, _, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (default path, no file): %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true after materialization at default path")
	}
}

func TestLoadClient_ResolverPicksConfig_WhenHasConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(validProvidersToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)

	_, cfg, hasConfig, err := LoadClient()
	if err != nil || !hasConfig {
		t.Fatalf("LoadClient: err=%v hasConfig=%v", err, hasConfig)
	}

	// Build the resolver closure the same way serve.go/run.go will.
	resolver := BuildResolveProfile(cfg, hasConfig)

	// "work/gpt-4o" should resolve via config (instance "work").
	profile, err := resolver("work/gpt-4o")
	if err != nil {
		t.Fatalf("resolver(work/gpt-4o): %v", err)
	}
	if profile.ID() != "work" {
		t.Fatalf("profile.ID()=%q, want %q", profile.ID(), "work")
	}
}

func TestLoadClient_ResolverPicksConfig_WhenMaterialized(t *testing.T) {
	// When providers.toml is absent it is materialized; the resolver always
	// goes through ResolveProfileFromConfig (always-config contract).
	dir := t.TempDir()
	t.Setenv("SERF_PROVIDERS_CONFIG", filepath.Join(dir, "providers.toml"))
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	_, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true after materialization")
	}

	resolver := BuildResolveProfile(cfg, hasConfig)

	// "openai/gpt-4o" resolves via the materialized config.
	profile, err := resolver("openai/gpt-4o")
	if err != nil {
		t.Fatalf("resolver(openai/gpt-4o): %v", err)
	}
	if profile.ID() != "openai" {
		t.Fatalf("profile.ID()=%q, want %q", profile.ID(), "openai")
	}
}

func TestLoadClientMaterializesAndInjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERF_PROVIDERS_CONFIG", filepath.Join(dir, "providers.toml"))
	t.Setenv("SERF_STATE_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "sk-env")

	client, cfg, hasConfig, err := LoadClient(llm.WithStateDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !hasConfig {
		t.Fatal("hasConfig must be true once materialized")
	}
	if _, err := os.Stat(filepath.Join(dir, "providers.toml")); err != nil {
		t.Fatal("providers.toml not materialized")
	}
	// openai instance registered + key injected into in-memory cfg
	found := false
	for _, n := range client.ProviderNames() {
		if n == "openai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("openai not registered: %v", client.ProviderNames())
	}
	injected := false
	for _, inst := range cfg.Instances {
		if inst.Name == "openai" && inst.APIKey == "sk-env" {
			injected = true
		}
	}
	if !injected {
		t.Errorf("key not injected into in-memory cfg: %+v", cfg.Instances)
	}
	// the secret must NOT be on disk
	data, _ := os.ReadFile(filepath.Join(dir, "providers.toml"))
	if strings.Contains(string(data), "sk-env") {
		t.Fatal("secret leaked to providers.toml")
	}
}
