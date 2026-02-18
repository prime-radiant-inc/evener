package main

import (
	"os"
	"testing"
)

// TestRunServe_MissingProvider verifies runServe returns an error when no
// --provider flag is set and SERF_PROVIDER is unset.
func TestRunServe_MissingProvider(t *testing.T) {
	old := os.Getenv("SERF_PROVIDER")
	if err := os.Unsetenv("SERF_PROVIDER"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			os.Setenv("SERF_PROVIDER", old)
		}
	}()

	err := runServe([]string{"--model", "gpt-5.2"})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
	if got := err.Error(); got != "no provider: use --provider or set SERF_PROVIDER" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunServe_MissingModel verifies runServe returns an error when no
// --model flag is set and SERF_MODEL is unset.
func TestRunServe_MissingModel(t *testing.T) {
	old := os.Getenv("SERF_MODEL")
	if err := os.Unsetenv("SERF_MODEL"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			os.Setenv("SERF_MODEL", old)
		}
	}()

	err := runServe([]string{"--provider", "openai"})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if got := err.Error(); got != "no model: use --model or set SERF_MODEL" {
		t.Fatalf("unexpected error: %v", err)
	}
}
