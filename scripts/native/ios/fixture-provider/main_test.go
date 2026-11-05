//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/test/e2e/fakellm"
)

// waitReady polls the readiness file until the provider names its endpoint,
// then returns the base URL. The file is the provider's only interface: the
// UI runner never learns the port any other way.
func waitReady(t *testing.T, ready string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil {
			var endpoint struct {
				BaseURL string `json:"base_url"`
			}
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
// with the fixed qualification fixture, the readiness file is private, and
// cancelling the context stops the listener. Requests without tools are the
// namer's auto-answered calls, so the tool-bearing body is the session loop's
// own round.
func TestFixtureProviderCompletesTurnAndStops(t *testing.T) {
	t.Parallel()
	ready := filepath.Join(t.TempDir(), "ready.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, ready) }()

	baseURL := waitReady(t, ready)
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
				Content string `json:"content"`
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
	if choice.FinishReason != "stop" {
		t.Fatalf("turn did not complete at the provider: finish_reason %q", choice.FinishReason)
	}
	for _, marker := range []string{"QUALIFICATION_prose_a7", "**QUALIFICATION_emphasis_b8**", "QUALIFICATION_code_c9"} {
		if !strings.Contains(choice.Message.Content, marker) {
			t.Fatalf("fixture answer lost %q: %q", marker, choice.Message.Content)
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
	if _, err := http.Post(baseURL+"/chat/completions", "application/json", strings.NewReader(request)); err == nil {
		t.Fatal("listener survived cancellation")
	} else if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("unexpected post-cancellation failure: %v", err)
	}
}
