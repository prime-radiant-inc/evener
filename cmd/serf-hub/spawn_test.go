package main

import (
	"testing"
)

func TestFindTemplate(t *testing.T) {
	cfg := Config{
		SpawnTemplates: []SpawnTemplate{
			{Name: "code, gpt", Provider: "openai", Model: "gpt-5.2", Agent: "default"},
			{Name: "review, claude", Provider: "anthropic", Model: "claude-opus-4-7"},
		},
	}
	got, ok := findTemplate(cfg, "code, gpt")
	if !ok {
		t.Fatal("expected to find template")
	}
	if got.Model != "gpt-5.2" {
		t.Errorf("Model: %q", got.Model)
	}
}

func TestFindTemplate_Missing(t *testing.T) {
	cfg := Config{}
	if _, ok := findTemplate(cfg, "nope"); ok {
		t.Fatal("expected not-found")
	}
}

func TestBuildSpawnArgs(t *testing.T) {
	tmpl := SpawnTemplate{
		Provider:        "openai",
		Model:           "gpt-5.2",
		Agent:           "default",
		ReasoningEffort: "medium",
	}
	args := buildSpawnArgs(tmpl, "/Users/jesse/git/foo")
	want := map[string]string{
		"--provider":         "openai",
		"--model":            "gpt-5.2",
		"--agent":            "default",
		"--reasoning-effort": "medium",
		"--dir":              "/Users/jesse/git/foo",
		"--addr":             "127.0.0.1:0",
	}
	got := pairsToMap(args)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("arg %s: got %q, want %q", k, got[k], v)
		}
	}
}

// pairsToMap collapses ["--k", "v", ...] to {"--k": "v"} for assertions.
func pairsToMap(args []string) map[string]string {
	out := make(map[string]string)
	for i := 0; i+1 < len(args); i += 2 {
		out[args[i]] = args[i+1]
	}
	return out
}
