package cmdutil

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"primeradiant.com/serf/llm"
	_ "primeradiant.com/serf/llm/providers/anthropic"
	_ "primeradiant.com/serf/llm/providers/kimi"
	_ "primeradiant.com/serf/llm/providers/openai"
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

func TestLoadClient_SkipsUninitializedUnusedProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(`
schema = 1
default = "openai"

[instances.anthropic]
type = "anthropic"

[instances.openai]
type = "openai"
api_style = "responses"
api_key = "sk-openai-test"
`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)
	t.Setenv("ANTHROPIC_API_KEY", "")

	client, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true with a valid config file")
	}
	if len(cfg.Instances) != 2 {
		t.Fatalf("cfg.Instances len=%d, want 2", len(cfg.Instances))
	}
	names := client.ProviderNames()
	sort.Strings(names)
	if len(names) != 1 || names[0] != "openai" {
		t.Fatalf("ProviderNames=%v, want [openai]", names)
	}
}

func TestLoadClient_NoFile_SeedsInMemory(t *testing.T) {
	// When providers.toml is absent, LoadClient seeds the config in memory from
	// the environment and returns hasConfig=true (always-config contract) WITHOUT
	// writing a file.
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	t.Setenv("SERF_PROVIDERS_CONFIG", path)
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	client, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (absent file): %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true after in-memory seed")
	}
	if len(cfg.Instances) == 0 {
		t.Fatal("expected non-empty Config after seed")
	}
	foundOpenAI := false
	for _, inst := range cfg.Instances {
		if inst.Name == "openai" {
			foundOpenAI = true
			if inst.Type != "openai" {
				t.Errorf("openai instance Type=%q, want %q", inst.Type, "openai")
			}
		}
	}
	if !foundOpenAI {
		t.Fatalf("expected 'openai' instance in seeded Config, got: %+v", cfg.Instances)
	}
	if client == nil {
		t.Fatal("expected non-nil client after seed")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("LoadClient must not write providers.toml (stat err=%v)", statErr)
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
	// Clear SERF_STATE_DIR so DefaultStateRoot() falls back to the HOME path.
	// Override HOME so the default state root lands in a clean temp dir.
	// The config is seeded in memory; hasConfig is true and no file is written.
	t.Setenv("SERF_PROVIDERS_CONFIG", "")
	t.Setenv("SERF_STATE_DIR", "")
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	dir := t.TempDir()
	t.Setenv("HOME", dir)

	_, _, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (default path, no file): %v", err)
	}
	if !hasConfig {
		t.Fatal("expected hasConfig=true after in-memory seed at default path")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".serf", "providers.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("LoadClient must not write to the default state root (stat err=%v)", statErr)
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

func TestLoadClient_ResolverPicksConfig_WhenSeeded(t *testing.T) {
	// When providers.toml is absent it is seeded in memory; the resolver always
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

// TestBuildResolveProfile_AlwaysUsesConfig verifies that BuildResolveProfile
// always delegates to ResolveProfileFromConfig regardless of the hasConfig
// argument. The env-fallback branch (SelectProfile) was removed; passing
// hasConfig=false must still resolve a materialized instance name, not fall
// back to env-based selection.
func TestBuildResolveProfile_AlwaysUsesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	if err := os.WriteFile(path, []byte(validProvidersToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)

	_, cfg, _, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}

	// Even with hasConfig=false the resolver must use the config path.
	resolver := BuildResolveProfile(cfg, false)
	profile, err := resolver("work/gpt-4o")
	if err != nil {
		t.Fatalf("resolver(work/gpt-4o): %v", err)
	}
	if profile.ID() != "work" {
		t.Fatalf("profile.ID()=%q, want %q", profile.ID(), "work")
	}
}

func TestLoadClientSeedsInMemoryAndInjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	t.Setenv("SERF_PROVIDERS_CONFIG", path)
	t.Setenv("SERF_STATE_DIR", dir)
	t.Setenv("OPENAI_API_KEY", "sk-env")

	client, cfg, hasConfig, err := LoadClient(llm.WithStateDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !hasConfig {
		t.Fatal("hasConfig must be true after in-memory seed")
	}
	// LoadClient must NOT write providers.toml — persisting is the hub's job.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("LoadClient must not write providers.toml (stat err=%v)", statErr)
	}
	// openai instance registered + key injected into the in-memory cfg
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
}

// Credential-store injection must not fill a key into an instance that
// authenticates via a configured Authorization header — the adapter's bearer
// would clobber that header on every request, sending an unrelated secret to
// the gateway.
func TestLoadProviderConfig_SkipsInjectionForAuthorizationHeaderInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	providers := `
schema = 1
default = "hdrgw"

[instances.hdrgw]
type = "glm"
base_url = "https://gw.example.com/v1"
headers = { "Authorization" = "Custom scheme-token" }

[instances.plain]
type = "glm"
`
	if err := os.WriteFile(path, []byte(providers), 0o644); err != nil {
		t.Fatal(err)
	}
	creds := `
schema = 1

[providers.plain]
api_key = "sk-glm-store"

[providers.hdrgw]
api_key = "sk-hdr-store-must-not-inject"
`
	credPath := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(credPath, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERF_PROVIDERS_CONFIG", path)

	cfg, hasConfig, err := LoadProviderConfig()
	if err != nil || !hasConfig {
		t.Fatalf("LoadProviderConfig: %v hasConfig=%v", err, hasConfig)
	}
	byName := map[string]string{}
	for _, inst := range cfg.Instances {
		byName[inst.Name] = inst.APIKey
	}
	if byName["hdrgw"] != "" {
		t.Errorf("header-authenticated instance got an injected key %q, want none", byName["hdrgw"])
	}
	if byName["plain"] != "sk-glm-store" {
		t.Errorf("plain keyless instance APIKey = %q, want store-injected sk-glm-store", byName["plain"])
	}
}
