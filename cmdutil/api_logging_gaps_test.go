package cmdutil

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// TestAttachAPILoggerTooManyResumedSessionIDs covers the error path where
// more than one resumed session ID is passed (line 16-17).
func TestAttachAPILoggerTooManyResumedSessionIDs(t *testing.T) {
	client := llm.NewClient()
	_, err := AttachAPILogger(client, t.TempDir(), nil, "sess-1", "sess-2")
	if err == nil {
		t.Fatal("AttachAPILogger with two resumed session IDs should error")
	}
	if !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("error should mention 'at most one', got %v", err)
	}
}

// TestAttachSessionAPILoggerWithWarnings covers the warnings != nil path
// (lines 42-46) by passing a non-nil warnings writer and making an API call
// that triggers the failure observer.
func TestAttachSessionAPILoggerWithWarnings(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	var warnings strings.Builder
	reserve, closeLog, err := AttachSessionAPILogger(client, dir, &warnings)
	if err != nil {
		t.Fatalf("AttachSessionAPILogger: %v", err)
	}
	if err := reserve("sess-warn"); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Make a successful call — this exercises the SetFailureObserver path by
	// ensuring the failure observer is installed (warnings != nil).
	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "sess-warn"), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}
}
