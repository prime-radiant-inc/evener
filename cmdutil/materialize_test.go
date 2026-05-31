package cmdutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/auth/openai/oaitest"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func TestMaterializeProvidersConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.toml")
	t.Setenv("OPENAI_API_KEY", "k1")
	t.Setenv("ANTHROPIC_API_KEY", "k2")
	// ensure no OAuth/state interferes:
	t.Setenv("SERF_STATE_DIR", dir)

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
	// default is consistent with what the env client picked
	if cfg.Default == "" {
		t.Error("empty default")
	}
}

// TestMaterializeDetectsOpenAIOAuth is the safety net for the hub-startup
// materialization: an OAuth-only OpenAI (no OPENAI_API_KEY) must still be
// detected and materialized, so the hub never persists a config missing the
// user's main provider.
func TestMaterializeDetectsOpenAIOAuth(t *testing.T) {
	// Isolate auth: clears OPENAI_API_KEY, points XDG_STATE_HOME at a temp dir,
	// and returns the serf state dir to store the record in.
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
