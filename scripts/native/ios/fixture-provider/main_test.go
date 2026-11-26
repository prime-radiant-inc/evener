//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/test/e2e/fakellm"
)

// TestReadyFileWriteIsAtomic pins the readiness file's reader contract: every
// read observes either no file or the complete endpoint JSON.
func TestReadyFileWriteIsAtomic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const readyName = "ready.json"
	ready := filepath.Join(dir, readyName)
	const wantBaseURL = "http://127.0.0.1:9"
	endpoint, err := json.Marshal(readyEndpoint{BaseURL: wantBaseURL})
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(ready)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				readErr <- err
				return
			}
			var parsed readyEndpoint
			if err := json.Unmarshal(data, &parsed); err != nil {
				readErr <- fmt.Errorf("reader saw an incomplete readiness file %q: %w", data, err)
				return
			}
			if parsed.BaseURL == "" {
				readErr <- fmt.Errorf("reader saw readiness JSON without an endpoint: %q", data)
				return
			}
		}
	}()
	for range 5000 {
		if err := writeReadyFile(ready, endpoint); err != nil {
			close(stop)
			<-readErr
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(ready)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("readiness file is not private: %v", perm)
	}
	published, err := os.ReadFile(ready)
	if err != nil {
		t.Fatal(err)
	}
	var got readyEndpoint
	if err := json.Unmarshal(published, &got); err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != wantBaseURL {
		t.Fatalf("readiness file names %q, not the published endpoint", got.BaseURL)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != readyName {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("readiness directory holds leftovers after the write: %v", names)
	}
}

// waitReady polls the readiness file until the provider names its endpoint,
// then returns the base URL. The file is the provider's only interface: the
// UI runner never learns the port any other way.
func waitReady(t *testing.T, ready string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil {
			var endpoint readyEndpoint
			if err := json.Unmarshal(data, &endpoint); err != nil {
				t.Fatalf("readiness file is not endpoint JSON: %v", err)
			}
			if endpoint.BaseURL != "" {
				return endpoint.BaseURL
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("provider never wrote its readiness file")
	return ""
}

// TestFixtureProviderCompletesTurnAndStops drives the provider through its
// actual HTTP boundary: an ordinary tool-bearing session round is answered
// with the fixed qualification fixture through the agent's result tool, the
// readiness file is private, and cancelling the context stops the listener.
// Requests without tools are the namer's auto-answered calls, so the
// tool-bearing body is the session loop's own round.
func TestFixtureProviderCompletesTurnAndStops(t *testing.T) {
	t.Parallel()
	ready := filepath.Join(t.TempDir(), "ready.json")
	// A seeded readiness file makes the publish observable: waitReady rejects
	// this placeholder, and the provider must replace the path rather than
	// rewrite it in place, so no reader can catch a truncated rewrite.
	placeholder, err := json.Marshal(readyEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ready, placeholder, 0o600); err != nil {
		t.Fatal(err)
	}
	seeded, err := os.Stat(ready)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, ready) }()

	baseURL := waitReady(t, ready)
	published, err := os.Stat(ready)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(seeded, published) {
		t.Fatal("provider rewrote the seeded readiness file in place; a concurrent reader can observe a truncated file")
	}
	request := `{"model":"` + fakellm.ModelID + `","messages":[{"role":"user","content":"qualify"}],"tools":[{}]}`
	resp, err := http.Post(baseURL+"/chat/completions", "application/json", strings.NewReader(request))
	if err != nil {
		t.Fatalf("session round failed at the provider boundary: %v", err)
	}
	defer resp.Body.Close()
	var reply struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Choices) != 1 {
		t.Fatalf("expected one completion choice, got %+v", reply.Choices)
	}
	choice := reply.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("fixture answer is not a tool call: finish_reason %q", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected one result-tool call, got %+v", choice.Message.ToolCalls)
	}
	call := choice.Message.ToolCalls[0].Function
	if call.Name != "communicate" {
		t.Fatalf("fixture answered through %q, not the result tool", call.Name)
	}
	var args struct {
		Message string `json:"message"`
		Next    string `json:"next"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		t.Fatalf("result-tool arguments are not JSON: %v", err)
	}
	if args.Next != "send_and_end_turn" {
		t.Fatalf("result-tool next = %q, want send_and_end_turn", args.Next)
	}
	for _, marker := range []string{"QUALIFICATION_prose_a7", "**QUALIFICATION_emphasis_b8**", "QUALIFICATION_code_c9"} {
		if !strings.Contains(args.Message, marker) {
			t.Fatalf("fixture answer lost %q: %q", marker, args.Message)
		}
	}

	info, err := os.Stat(ready)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("readiness file is not private: %v", perm)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown failed: %v", err)
	}
	// The listener must be gone after cancellation. A dying listener's
	// accept queue can hand the kernel one more completed handshake, which
	// the client reads as EOF instead of a refusal, so probe until the port
	// refuses connections outright. An answered request is the survival this
	// check exists to catch.
	refusalDeadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Post(baseURL+"/chat/completions", "application/json", strings.NewReader(request))
		if err == nil {
			resp.Body.Close()
			t.Fatal("listener survived cancellation")
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			break
		}
		if time.Now().After(refusalDeadline) {
			t.Fatalf("the listener never refused connections after cancellation: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
