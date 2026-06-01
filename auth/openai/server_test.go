package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCallbackServerCapturesSuccessfulCallback(t *testing.T) {
	server := startTestCallbackServer(t, "expected-state", time.Second)

	resp := requestCallback(t, server.RedirectURI()+"?code=auth-code&state=expected-state")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(strings.ToLower(body), "success") {
		t.Fatalf("callback body = %q, want success message", body)
	}

	result, err := server.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.Code != "auth-code" {
		t.Fatalf("Code = %q, want %q", result.Code, "auth-code")
	}
	if result.State != "expected-state" {
		t.Fatalf("State = %q, want %q", result.State, "expected-state")
	}
}

func TestCallbackServerRejectsStateMismatch(t *testing.T) {
	server := startTestCallbackServer(t, "expected-state", time.Second)

	resp := requestCallback(t, server.RedirectURI()+"?code=auth-code&state=wrong-state")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body := readBody(t, resp)
	if !strings.Contains(strings.ToLower(body), "failed") {
		t.Fatalf("callback body = %q, want failure message", body)
	}

	_, err := server.Wait(context.Background())
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("Wait() error = %v, want %v", err, ErrStateMismatch)
	}
}

func TestCallbackServerWaitTimesOut(t *testing.T) {
	server := startTestCallbackServer(t, "expected-state", 10*time.Millisecond)

	_, err := server.Wait(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() error = %v, want %v", err, context.DeadlineExceeded)
	}
}

func TestCallbackServerCloseShutsDownListener(t *testing.T) {
	server := startTestCallbackServer(t, "expected-state", time.Second)

	if err := server.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err := http.Get(server.RedirectURI())
	if err == nil {
		t.Fatal("GET after Close() error = nil, want listener closed")
	}
}

func startTestCallbackServer(t *testing.T, expectedState string, timeout time.Duration) *CallbackServer {
	t.Helper()

	cfg := DefaultConfig()
	cfg.CallbackTimeout = timeout

	server, err := StartCallbackServer(context.Background(), cfg, 0, expectedState)
	if err != nil {
		t.Fatalf("StartCallbackServer() error = %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	return server
}

func requestCallback(t *testing.T, url string) *http.Response {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return string(body)
}
