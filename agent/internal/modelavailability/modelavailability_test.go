package modelavailability

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestCaptureUsesOneDeadlineAndKeepsSuccessfulPartialChoices(t *testing.T) {
	started := make(chan struct{}, 2)
	fetch := func(ctx context.Context, name string) ([]llm.ModelInfo, error) {
		started <- struct{}{}
		if name == "slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []llm.ModelInfo{{ID: "z"}, {ID: "a"}, {ID: "a"}}, nil
	}
	s := Capture(context.Background(), []string{"slow", "good"}, fetch, 30*time.Millisecond)
	if s.Complete || len(s.Choices) != 2 || s.Choices[0] != "good/a" || s.Choices[1] != "good/z" {
		t.Fatalf("snapshot = %#v", s)
	}
	if s.Status["slow"].Kind != StatusTimeout || s.Status["good"].Kind != StatusSuccess {
		t.Fatalf("status = %#v", s.Status)
	}
}

func TestRenderInlineRequiresCompleteCountAndUTF8ByteBounds(t *testing.T) {
	s := testSnapshot("v1", true, "p/α", "p/β")
	if got, ok := s.Inline(2, len([]byte("Verified startup snapshot v1: p/α, p/β."))); !ok || !strings.Contains(got, "p/β") {
		t.Fatalf("inline = %q, %v", got, ok)
	}
	if _, ok := s.Inline(2, len([]byte("Verified startup snapshot v1: p/α, p/β."))-1); ok {
		t.Fatal("inline exceeded exact UTF-8 byte bound")
	}
	partial := s
	partial.Complete = false
	if _, ok := partial.Inline(10, 1000); ok {
		t.Fatal("partial snapshot was inlined")
	}
}

func TestCursorIsOpaqueSnapshotBoundAndPagesByBytesAndCount(t *testing.T) {
	s := testSnapshot("v1", true, "p/a", "p/b", "p/c")
	page, err := s.Page("", 2, len([]byte("p/a\np/b")))
	if err != nil || len(page.Choices) != 2 || page.Next == "" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if page.Next == "2" {
		t.Fatalf("cursor exposed offset: %q", page.Next)
	}
	next, err := s.Page(page.Next, 2, 100)
	if err != nil || len(next.Choices) != 1 || next.Choices[0] != "p/c" {
		t.Fatalf("next = %#v, %v", next, err)
	}
	if _, err := s.Page(page.Next, 2, 100); err == nil {
		t.Fatal("cursor unexpectedly reusable")
	}
}

func testSnapshot(version string, complete bool, choices ...string) Snapshot {
	return Snapshot{Version: version, Complete: complete, Choices: choices, key: []byte("test-key"), mu: &sync.Mutex{}, used: map[string]bool{}}
}
