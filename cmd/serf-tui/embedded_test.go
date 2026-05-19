package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
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
