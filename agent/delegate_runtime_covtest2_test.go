package agent

import (
	"context"
	"html"
	"strings"
	"testing"
	"time"
)

// TestDelegateQuietAttentionID covers delegateQuietAttentionID (lines 160-162).
func TestDelegateQuietAttentionID(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_123", generation: 2}
	got := delegateQuietAttentionID(lease)
	want := "quiet:dlg_123:2:1"
	if got != want {
		t.Fatalf("delegateQuietAttentionID = %q, want %q", got, want)
	}
}

// TestDelegateQuietAttentionIDForStretch covers delegateQuietAttentionIDForStretch
// (lines 164-166).
func TestDelegateQuietAttentionIDForStretch(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_abc", generation: 5}
	tests := []struct {
		seq  uint64
		want string
	}{
		{0, "quiet:dlg_abc:5:0"},
		{1, "quiet:dlg_abc:5:1"},
		{99, "quiet:dlg_abc:5:99"},
	}
	for _, tc := range tests {
		if got := delegateQuietAttentionIDForStretch(lease, tc.seq); got != tc.want {
			t.Errorf("stretch(%d) = %q, want %q", tc.seq, got, tc.want)
		}
	}
}

// TestDelegateQuietAttentionContent covers delegateQuietAttentionContent
// (lines 168-174).
func TestDelegateQuietAttentionContent(t *testing.T) {
	t.Parallel()
	lease := delegateLease{delegateID: "dlg_x<y>", generation: 1}
	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	got := delegateQuietAttentionContent(lease, at)
	if !strings.HasPrefix(got, "<delegate-notification") {
		t.Fatalf("expected delegate-notification wrapper, got %q", got)
	}
	// HTML-escaped delegate ID.
	if !strings.Contains(got, html.EscapeString("dlg_x<y>")) {
		t.Fatalf("expected escaped delegate ID in content, got %q", got)
	}
}

// TestDelegateInputWasPreseededCov covers delegateInputWasPreseeded
// (lines 767-770).
func TestDelegateInputWasPreseededCov(t *testing.T) {
	t.Parallel()
	// No context value: returns false.
	if delegateInputWasPreseeded(context.Background(), "sess1", "hello") {
		t.Fatal("expected false for background context")
	}
	// Matching context value: returns true.
	ctx := context.WithValue(context.Background(), delegatePreseededInputContextKey{}, delegatePreseededInput{
		sessionID: "sess1",
		input:     "hello",
	})
	if !delegateInputWasPreseeded(ctx, "sess1", "hello") {
		t.Fatal("expected true for matching preseeded input")
	}
	// Mismatched session ID: returns false.
	if delegateInputWasPreseeded(ctx, "sess2", "hello") {
		t.Fatal("expected false for mismatched session ID")
	}
	// Mismatched input: returns false.
	if delegateInputWasPreseeded(ctx, "sess1", "world") {
		t.Fatal("expected false for mismatched input")
	}
}

// TestBindStableDelegateActivity_NilChild covers the nil-guard for
// bindStableDelegateActivity (lines 281-283).
func TestBindStableDelegateActivity_NilChild(t *testing.T) {
	t.Parallel()
	bindStableDelegateActivity(nil, nil, delegateLease{})
	// Should not panic.
}

// TestBindStableDelegateActivityToOwner_NilChild covers the nil-guard for
// bindStableDelegateActivityToOwner (lines 289-292).
func TestBindStableDelegateActivityToOwner_NilChild(t *testing.T) {
	t.Parallel()
	bindStableDelegateActivityToOwner(nil, nil, delegateLease{}, nil)
	// Should not panic.
}
