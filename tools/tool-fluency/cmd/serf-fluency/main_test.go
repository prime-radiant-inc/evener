package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunSuiteRejectsUnknownHarness(t *testing.T) {
	err := runSuite([]string{"--harness", "bogus"})
	if err == nil {
		t.Fatal("runSuite returned nil, want harness validation error")
	}
	if !strings.Contains(err.Error(), "--harness must be cli or live") {
		t.Fatalf("error = %q, want harness guidance", err.Error())
	}
}

func TestCLIProbeArgsIncludesSystemPromptAppendFiles(t *testing.T) {
	cfg := runConfig{
		model:              "openai/test-model",
		fastCheapModel:     "openai/cheap-model",
		systemPromptAppend: []string{"/tmp/append-a.md", " ", "/tmp/append-b.md"},
		reasoningEffort:    "high",
	}
	probe := probeFile{Prompt: "inspect the fixture"}
	res := probeResult{WorkDir: "/work", StateDir: "/state"}

	args := cliProbeArgs(cfg, probe, res)

	want := []string{
		"--system-prompt-append", "/tmp/append-a.md",
		"--system-prompt-append", "/tmp/append-b.md",
	}
	assertSubsequence(t, args, want)
}

func TestMaybeClearOpenAIAPIKeyRestoresExistingValue(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-existing")

	restore := maybeClearOpenAIAPIKey(true)
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after clear = %q, %v; want unset", got, ok)
	}

	restore()
	if got := os.Getenv("OPENAI_API_KEY"); got != "sk-existing" {
		t.Fatalf("OPENAI_API_KEY after restore = %q, want original", got)
	}
}

func assertSubsequence(t *testing.T, haystack, needle []string) {
	t.Helper()
	next := 0
	for _, value := range haystack {
		if value == needle[next] {
			next++
			if next == len(needle) {
				return
			}
		}
	}
	t.Fatalf("args = %#v, want subsequence %#v", haystack, needle)
}

func TestMaybeClearOpenAIAPIKeyRestoresUnsetState(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "temporary")
	if err := os.Unsetenv("OPENAI_API_KEY"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}

	restore := maybeClearOpenAIAPIKey(true)
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after clear = %q, %v; want unset", got, ok)
	}

	restore()
	if got, ok := os.LookupEnv("OPENAI_API_KEY"); ok || got != "" {
		t.Fatalf("OPENAI_API_KEY after restore = %q, %v; want unset", got, ok)
	}
}
