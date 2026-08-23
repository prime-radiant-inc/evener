package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFake429HandlerModels covers the /models path (200 response).
func TestFake429HandlerModels(t *testing.T) {
	h := handler("5")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fake-model") {
		t.Fatalf("models body missing fake-model: %s", rec.Body.String())
	}
}

// TestFake429HandlerCompletion covers the 429 path.
func TestFake429HandlerCompletion(t *testing.T) {
	h := handler("10")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("completion status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "10" {
		t.Fatalf("Retry-After = %q, want 10", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("body missing rate_limit_error: %s", rec.Body.String())
	}
}

// TestFake429HandlerMultipleHits covers the counter incrementing.
func TestFake429HandlerMultipleHits(t *testing.T) {
	h := handler("3")
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("hit %d status = %d, want 429", i, rec.Code)
		}
	}
}

// TestFake429ListenSuccess covers the listen function.
func TestFake429ListenSuccess(t *testing.T) {
	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	if ln.Addr() == nil {
		t.Fatalf("listener addr should not be nil")
	}
}

// TestFake429ListenFailure covers the listen error path.
func TestFake429ListenFailure(t *testing.T) {
	_, err := listen("invalid:addr:format:extra")
	if err == nil {
		t.Fatalf("listen with invalid addr should error")
	}
}

// TestFake429Serve covers the serve function.
func TestFake429Serve(t *testing.T) {
	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Close the listener to make serve return.
	_ = ln.Close()
	if err := serve(ln, "5"); err == nil {
		t.Fatalf("serve on closed listener should error")
	}
}
