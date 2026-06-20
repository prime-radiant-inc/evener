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
