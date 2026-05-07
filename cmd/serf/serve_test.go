package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/serf/rendezvous"
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

func TestServe_WritesAndRemovesRendezvousFile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("requires an LLM API key for serve startup")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	args := []string{
		"--provider", os.Getenv("SERF_TEST_PROVIDER"),
		"--model", os.Getenv("SERF_TEST_MODEL"),
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
	}
	if args[1] == "" || args[3] == "" {
		t.Skip("set SERF_TEST_PROVIDER and SERF_TEST_MODEL to run this test")
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	runDir := filepath.Join(tmpHome, ".serf", "run")
	pid := os.Getpid()
	target := filepath.Join(runDir, strconv.Itoa(pid)+".json")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(target); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("rendezvous file %s was not created: %v", target, err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("rendezvous.List returned err=%v entries=%v", err, entries)
	}
	if entries[0].SpawnedBy != "user" {
		t.Errorf("SpawnedBy: got %q, want %q", entries[0].SpawnedBy, "user")
	}
	if entries[0].Address == "" {
		t.Error("Address should not be empty")
	}

	resp, err := http.Post("http://"+entries[0].Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after /shutdown")
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rendezvous file should be removed, stat err=%v", err)
	}
}
