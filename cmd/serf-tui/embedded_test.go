package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestEmbeddedServer_StartsAndResponds(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	embedded, err := startEmbedded(ctx, embeddedConfig{
		model:   "openai/gpt-4o-mini",
		workDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("startEmbedded: %v", err)
	}
	defer embedded.Close()

	if embedded.addr == "" {
		t.Fatal("embedded.addr is empty")
	}
	t.Logf("embedded server at %s", embedded.addr)

	// GET /status should return valid JSON.
	statusURL := fmt.Sprintf("http://%s/status", embedded.addr)
	resp, err := http.Get(statusURL)
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /status: %d", resp.StatusCode)
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	t.Logf("status: %v", status)
}

func TestWaitForEmbeddedReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/status" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	}()
	defer func() {
		_ = listener.Close()
		<-done
	}()

	if err := waitForEmbeddedReady(addr, time.Second); err != nil {
		t.Fatalf("waitForEmbeddedReady() error = %v", err)
	}
}

func TestEmbeddedServer_APILogFlushesOnClose(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()

	oldNewClient := embeddedNewLLMClientFromEnv
	embeddedNewLLMClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(embeddedLoggingAdapter{})
		return client, nil
	}
	t.Cleanup(func() {
		embeddedNewLLMClientFromEnv = oldNewClient
	})

	embedded, err := startEmbedded(context.Background(), embeddedConfig{
		model:    "openai/gpt-test",
		workDir:  workDir,
		stateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("startEmbedded: %v", err)
	}

	_, err = embedded.client.Complete(llm.WithAPILogContext(context.Background(), "embedded-session", 3), llm.Request{
		Provider: "openai",
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	embedded.Close()

	data, err := os.ReadFile(filepath.Join(stateDir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	for _, want := range []string{`"session_id":"embedded-session"`, `"round":3`, `"model":"gpt-test"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api.jsonl missing %s:\n%s", want, string(data))
		}
	}
}

type embeddedLoggingAdapter struct{}

func (embeddedLoggingAdapter) Name() string { return "openai" }

func (embeddedLoggingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}

func (embeddedLoggingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, io.EOF
}
