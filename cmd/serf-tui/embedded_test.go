package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
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
		model: "openai/gpt-4o-mini",
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

func TestEmbeddedServer_RoundTrip(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	embedded, err := startEmbedded(ctx, embeddedConfig{
		model: "openai/gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("startEmbedded: %v", err)
	}
	defer embedded.Close()

	baseURL := fmt.Sprintf("http://%s", embedded.addr)

	// Connect to SSE before sending input.
	sseResp, err := http.Get(baseURL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer sseResp.Body.Close()

	if ct := sseResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type: %q", ct)
	}

	// Send a trivial input.
	body := strings.NewReader(`{"text":"Reply with exactly the word: pong"}`)
	inputResp, err := http.Post(baseURL+"/input", "application/json", body)
	if err != nil {
		t.Fatalf("POST /input: %v", err)
	}
	inputResp.Body.Close()
	if inputResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /input: %d", inputResp.StatusCode)
	}

	// Read SSE events until we see ASSISTANT_TEXT_END or timeout.
	// The agent may make tool calls before responding with text, so allow enough time.
	events, err := readSSEUntil(sseResp.Body, "ASSISTANT_TEXT_END", 45*time.Second)
	if err != nil {
		t.Fatalf("readSSE: %v", err)
	}

	var gotStart, gotActivity bool
	for i, ev := range events {
		t.Logf("event[%d]: type=%q data=%.100s", i, ev.Event, ev.Data)
		switch ev.Event {
		case "SESSION_START":
			gotStart = true
			if !strings.Contains(ev.Data, "session_id") {
				t.Errorf("SESSION_START missing session_id: %s", ev.Data)
			}
		case "ASSISTANT_TEXT_START", "ASSISTANT_TEXT_DELTA", "TOOL_CALL_START":
			gotActivity = true
		}
	}
	if !gotStart {
		t.Error("never received SESSION_START")
	}
	if !gotActivity {
		t.Error("never received any assistant activity (text delta or tool call)")
	}
	t.Logf("received %d SSE events", len(events))
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

// readSSEUntil reads SSE events from r until an event with the given type is seen.
func readSSEUntil(r interface{ Read([]byte) (int, error) }, eventType string, timeout time.Duration) ([]SSEEvent, error) {
	deadline := time.Now().Add(timeout)
	var events []SSEEvent
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 4096)

	for time.Now().Before(deadline) {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)

			// Parse complete events from buffer.
			for {
				idx := strings.Index(string(buf), "\n\n")
				if idx < 0 {
					break
				}
				block := string(buf[:idx])
				buf = buf[idx+2:]

				var ev SSEEvent
				for _, line := range strings.Split(block, "\n") {
					ev.parseLine(line)
				}
				if ev.hasContent() {
					events = append(events, ev)
					if ev.Event == eventType {
						return events, nil
					}
				}
			}
		}
		if err != nil {
			return events, err
		}
	}
	return events, fmt.Errorf("timeout waiting for %s", eventType)
}
