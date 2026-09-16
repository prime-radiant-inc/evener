package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelsListDisabled(t *testing.T) {
	modelsTestEnv(t)
	modelsFixture(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk")
	path := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.anthropic]\napi_key = \"sk\"\n[providers.anthropic.models.\"claude-opus-4-6\"]\ndisabled = true\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"list", "--provider", "anthropic"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("list: %v (%s)", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "claude-opus-4-6") {
		t.Fatalf("a disabled model must not list by default:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := runModels([]string{"list", "--provider", "anthropic", "--all"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("list --all: %v (%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude-opus-4-6") || !strings.Contains(stdout.String(), "disabled") {
		t.Fatalf("--all must show the disabled row flagged:\n%s", stdout.String())
	}
}
