package cmdutil

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

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

func TestLoadClient_NoFile_FallsBackToEnv(t *testing.T) {
	// Point SERF_PROVIDERS_CONFIG at a path that does not exist.
	t.Setenv("SERF_PROVIDERS_CONFIG", "/nonexistent/path/providers.toml")
	// Unset any API keys so NewFromEnv doesn't fail on missing credentials.
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	client, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (absent file): %v", err)
	}
	if hasConfig {
		t.Fatal("expected hasConfig=false when file is absent")
	}
	if cfg.Default != "" || len(cfg.Instances) != 0 {
		t.Fatalf("expected empty Config when hasConfig=false, got %+v", cfg)
	}
	if client == nil {
		t.Fatal("expected non-nil client from env fallback")
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
	// The default path (~/.serf/providers.toml) almost certainly doesn't
	// exist in CI, so we just verify it doesn't blow up: hasConfig=false
	// and no error.
	t.Setenv("SERF_PROVIDERS_CONFIG", "")
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	// We override the home dir to a temp dir so the default path is clean.
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	_, _, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient (default path, no file): %v", err)
	}
	if hasConfig {
		t.Fatal("expected hasConfig=false when default path doesn't exist")
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

func TestLoadClient_ResolverPicksEnv_WhenNoConfig(t *testing.T) {
	t.Setenv("SERF_PROVIDERS_CONFIG", "/nonexistent/providers.toml")
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-test")

	_, cfg, hasConfig, err := LoadClient()
	if err != nil {
		t.Fatalf("LoadClient: %v", err)
	}

	resolver := BuildResolveProfile(cfg, hasConfig)

	// "openai/gpt-4o" should go through the env path (SelectProfile).
	profile, err := resolver("openai/gpt-4o")
	if err != nil {
		t.Fatalf("resolver(openai/gpt-4o): %v", err)
	}
	if profile.ID() != "openai" {
		t.Fatalf("profile.ID()=%q, want %q", profile.ID(), "openai")
	}
}
