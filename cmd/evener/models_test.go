package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func modelsTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("EVENER_OFFLINE", "1")
	for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GROQ_API_KEY", "OPENAI_BASE_URL", "OLLAMA_HOST", "OLLAMA_BASE_URL"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func TestModelsInspect(t *testing.T) {
	modelsTestEnv(t)
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"inspect", "openai/gpt-5.5"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("inspect: %v (%s)", err, stderr.String())
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("inspect must print JSON: %v\n%s", err, stdout.String())
	}
	if out["protocol"] != "openai-responses" || out["wire_id"] != "gpt-5.5" || out["credential_source"] != "none" {
		t.Fatalf("inspect output: %v", out)
	}
	req := out["request"].(map[string]any)
	if req["url"] != "https://api.openai.com/v1/responses" || req["auth"] != "bearer" {
		t.Fatalf("request skeleton: %v", req)
	}
	if _, ok := out["provenance"]; !ok {
		t.Fatal("inspect must include provenance")
	}
	if !strings.Contains(strings.Join(anyStrings(out["warnings"]), ";"), "no credential") {
		t.Fatalf("credential-less inspect must warn: %v", out["warnings"])
	}
}

func anyStrings(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			out = append(out, x.(string))
		}
	}
	return out
}

func TestModelsList(t *testing.T) {
	modelsTestEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk")
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"list"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout.String(), "anthropic/claude-opus-5") || strings.Contains(stdout.String(), "openai/gpt-5.5") {
		t.Fatalf("list shows instances only by default:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := runModels([]string{"list", "--provider", "groq", "--all"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("list --provider: %v", err)
	}
	if !strings.Contains(stdout.String(), "groq/openai/gpt-oss-120b") || !strings.Contains(stdout.String(), "openai-chat") {
		t.Fatalf("list --provider groq:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := runModels([]string{"list", "--provider", "cohere", "--all"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "needs base_url") {
		t.Fatalf("--all must flag a hidden provider:\n%s", stdout.String())
	}
}

func TestModelsOldSchemaIgnoredWithNote(t *testing.T) {
	modelsTestEnv(t)
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte("[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", path)
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"inspect", "anthropic/claude-opus-5"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("old schema must be ignored during step 1: %v", err)
	}
	if !strings.Contains(stderr.String(), "ignored until the cut-over") {
		t.Fatalf("stderr must carry the note: %q", stderr.String())
	}
}

func TestModelsRefreshUsesInjectedFetcher(t *testing.T) {
	modelsTestEnv(t)
	raw, _, err := registry.EmbeddedSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	old := modelsRefreshFetcher
	t.Cleanup(func() { modelsRefreshFetcher = old })
	modelsRefreshFetcher = func(_ context.Context, _ string) ([]byte, string, bool, error) {
		calls++
		return raw, `W/"test"`, false, nil
	}
	var stdout, stderr bytes.Buffer
	if err := runModels([]string{"refresh", "--force"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if calls != 1 || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("refresh output: %q calls=%d", stdout.String(), calls)
	}
	stdout.Reset()
	if err := runModels([]string{"refresh"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(stdout.String(), "fresh") {
		t.Fatalf("a fresh cache is skipped without --force: %q", stdout.String())
	}
}

func TestModelsUsageAndUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runModels(nil, strings.NewReader(""), &stdout, &stderr); err != nil || !strings.Contains(stderr.String(), "Usage: evener models") {
		t.Fatalf("usage: %v %q", err, stderr.String())
	}
	if err := runModels([]string{"bogus"}, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("unknown subcommand must error")
	}
}
