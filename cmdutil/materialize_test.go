package cmdutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/internal/providerconfig"
	"primeradiant.com/serf/llm"
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

	got, exists, err := providerconfig.LoadFile(path)
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
