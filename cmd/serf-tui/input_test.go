package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendCompact_Success(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compact" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	// Strip "http://" prefix to get addr.
	addr := ts.URL[len("http://"):]
	cmd := sendCompact(addr)
	msg := cmd()

	result, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("expected compactDoneMsg, got %T", msg)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
	if !called {
		t.Error("server handler not called")
	}
}

func TestSendCompact_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	addr := ts.URL[len("http://"):]
	cmd := sendCompact(addr)
	msg := cmd()

	result, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("expected compactDoneMsg, got %T", msg)
	}
	if result.err == nil {
		t.Error("expected error, got nil")
	}
}

func TestSlashCompactInterception(t *testing.T) {
	// Verify that /compact is recognized as a slash command.
	tests := []struct {
		input string
		want  bool
	}{
		{"/compact", true},
		{" /compact ", true},
		{"/compact extra args", true},
		{"hello /compact", false},
		{"/notacommand", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.input), func(t *testing.T) {
			cmd, _ := parseSlashCommand(tt.input)
			got := cmd == "compact"
			if got != tt.want {
				t.Errorf("parseSlashCommand(%q) compact = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
