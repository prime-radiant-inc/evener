package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/rendezvous"
)

func TestRunServe_BareModelRejected(t *testing.T) {
	err := runServe([]string{"--model", "gpt-5.2"})
	if err == nil {
		t.Fatal("expected error for bare model")
	}
	if got := err.Error(); got != `model "gpt-5.2" must use provider/model` {
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

	err := runServe(nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if got := err.Error(); got != "no model: use --model provider/model or set SERF_MODEL=provider/model" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestServeRendezvousRegistrationUpdatesSessionIdentity(t *testing.T) {
	runDir := t.TempDir()
	reg := &serveRendezvousRegistration{}
	if err := reg.Register(runDir, rendezvous.Entry{
		PID:       4242,
		Protocol:  "serf-appwire-v1",
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "01OLD",
		SessionID: "01OLD",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.UpdateSessionID("01NEW"); err != nil {
		t.Fatalf("UpdateSessionID: %v", err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%+v", entries)
	}
	if entries[0].ThreadID != "01NEW" || entries[0].SessionID != "01NEW" {
		t.Fatalf("entry identity=%+v", entries[0])
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
		"--model", os.Getenv("SERF_TEST_PROVIDER") + "/" + os.Getenv("SERF_TEST_MODEL"),
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
	}
	if os.Getenv("SERF_TEST_PROVIDER") == "" || os.Getenv("SERF_TEST_MODEL") == "" {
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

func TestServeClient_APILogWritesJSONL(t *testing.T) {
	stateDir := t.TempDir()
	called := make(chan struct{}, 1)

	oldNewClient := serveNewLLMClientFromEnv
	serveNewLLMClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{called: called})
		return client, nil
	}
	t.Cleanup(func() {
		serveNewLLMClientFromEnv = oldNewClient
	})

	client, closeAPILog, err := newServeLLMClient(stateDir, nil)
	if err != nil {
		t.Fatalf("newServeLLMClient: %v", err)
	}
	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "serve-session", 4), llm.Request{
		Provider: "openai",
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeAPILog(); err != nil {
		t.Fatalf("closeAPILog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	for _, want := range []string{`"provider":"openai"`, `"model":"gpt-test"`, `"session_id":"serve-session"`, `"round":4`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api.jsonl missing %s:\n%s", want, string(data))
		}
	}
	select {
	case <-called:
	default:
		t.Fatal("fake adapter was not called")
	}
}

type serveLoggingAdapter struct {
	called chan<- struct{}
}

func (a serveLoggingAdapter) Name() string { return "openai" }

func (a serveLoggingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	select {
	case a.called <- struct{}{}:
	default:
	}
	return llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}

func (a serveLoggingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, io.EOF
}
