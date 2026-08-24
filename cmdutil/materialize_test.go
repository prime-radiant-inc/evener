package cmdutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
)

// TestOllamaBaseURLFromEnv pins the base_url the materializer persists for
// the "ollama" instance against a bare OLLAMA_HOST, the value the
// documented quickstart (docs/ollama.md) tells a new user to set:
// OLLAMA_HOST=localhost. It must come out as a complete URL, not the raw
// host string — a bare "localhost" written into base_url makes the ollama
// provider post to "localhost/chat/completions", which has no scheme.
func TestOllamaBaseURLFromEnv(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "localhost")
	if got, want := ollamaBaseURLFromEnv(), "http://localhost:11434/v1"; got != want {
		t.Fatalf("ollamaBaseURLFromEnv() = %q, want %q", got, want)
	}
}

// TestOllamaBaseURLFromEnvPrefersBaseURL pins that OLLAMA_BASE_URL still
// wins outright over OLLAMA_HOST, matching the ollama adapter's own
// resolution order, and is used as-is (trailing slash stripped) rather than
// normalized as a host.
func TestOllamaBaseURLFromEnvPrefersBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "https://proxy.example/ollama/v1/")
	t.Setenv("OLLAMA_HOST", "some-other-host")
	if got, want := ollamaBaseURLFromEnv(), "https://proxy.example/ollama/v1"; got != want {
		t.Fatalf("ollamaBaseURLFromEnv() = %q, want %q", got, want)
	}
}

// TestOllamaBaseURLFromEnvUnset pins that both unset yields "", so Seed
// leaves the instance's base_url empty and the adapter's own default
// applies at construction time rather than a materializer-supplied value.
func TestOllamaBaseURLFromEnvUnset(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "")
	t.Setenv("OLLAMA_HOST", "")
	if got := ollamaBaseURLFromEnv(); got != "" {
		t.Fatalf("ollamaBaseURLFromEnv() = %q, want \"\"", got)
	}
}

func TestMaterializeProvidersConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	t.Setenv("OPENAI_API_KEY", "k1")
	t.Setenv("ANTHROPIC_API_KEY", "k2")
	// ensure no OAuth/state interferes:
	t.Setenv("EVENER_STATE_DIR", dir)

	cfg, err := MaterializeProvidersConfig(path, llm.WithStateDir(dir))
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("not written: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "api_key") {
		t.Fatalf("secret leaked:\n%s", data)
	}

	got, exists, err := providercfg.LoadFile(path)
	if err != nil || !exists {
		t.Fatalf("reload: exists=%v err=%v", exists, err)
	}
	names := map[string]bool{}
	for _, i := range got.Instances {
		names[i.Name] = true
	}
	if !names["openai"] || !names["anthropic"] {
		t.Errorf("missing instances: %+v", got.Instances)
	}
	// default is determined by registration order: anthropic registers before openai
	// (blank import order in load_client_test.go), so anthropic wins when both keys are set.
	if cfg.Default != "anthropic" {
		t.Errorf("cfg.Default = %q, want \"anthropic\"", cfg.Default)
	}
}

// TestMaterializeDetectsOpenAIOAuth is the safety net for the hub-startup
// materialization: an OAuth-only OpenAI (no OPENAI_API_KEY) must still be
// detected and materialized, so the hub never persists a config missing the
// user's main provider.
func TestMaterializeDetectsOpenAIOAuth(t *testing.T) {
	// Isolate auth: clears OPENAI_API_KEY, points XDG_STATE_HOME at a temp dir,
	// and returns the evener state dir to store the record in.
	stateDir := oaitest.IsolateOpenAIAuth(t)
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"KIMI_API_KEY", "GLM_API_KEY", "MINIMAX_API_KEY", "OPENROUTER_API_KEY",
		"OPENAI_COMPATIBLE_BASE_URL", "OLLAMA_BASE_URL", "OLLAMA_HOST",
	} {
		t.Setenv(k, "")
	}

	// A fresh OAuth record (expiry in the future) so detection needs no refresh
	// and makes no network call.
	rec := authopenai.AuthRecord{
		Version:      1,
		Source:       authopenai.AuthSourceOAuth,
		AccessToken:  "fake-access",
		RefreshToken: "fake-refresh",
		TokenType:    "Bearer",
		ObtainedAt:   time.Now(),
		Expiry:       time.Now().Add(time.Hour),
	}
	if err := authopenai.SaveAuth(stateDir, "openai", rec); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}

	path := filepath.Join(t.TempDir(), "providers.toml")
	cfg, err := MaterializeProvidersConfig(path)
	if err != nil {
		t.Fatalf("MaterializeProvidersConfig: %v", err)
	}

	hasOpenAI := false
	for _, inst := range cfg.Instances {
		if inst.Name == "openai" {
			hasOpenAI = true
		}
	}
	if !hasOpenAI {
		t.Fatalf("openai (OAuth-only) not detected/materialized: %+v", cfg.Instances)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "[instances.openai]") {
		t.Errorf("openai missing from persisted file:\n%s", data)
	}
	if strings.Contains(string(data), "api_key") {
		t.Errorf("secret leaked to file:\n%s", data)
	}
}
