package modelavailability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	page, err := s.Page("", 2, 1024)
	if err != nil || len(page.Choices) != 2 || page.Next == "" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if page.Next == "2" {
		t.Fatalf("cursor exposed offset: %q", page.Next)
	}
	next, err := s.Page(page.Next, 2, 1024)
	if err != nil || len(next.Choices) != 1 || next.Choices[0] != "p/c" {
		t.Fatalf("next = %#v, %v", next, err)
	}
	if _, err := s.Page(page.Next, 2, 1024); err != nil {
		t.Fatalf("cursor should be idempotent: %v", err)
	}
}

func TestPageReturnsBoundedEnvelopeAndProgressesOversizedOrEmptySnapshots(t *testing.T) {
	s := testSnapshot("v1", false, strings.Repeat("x", 1000))
	p, err := s.Page("", 1, 256)
	if err != nil || !p.Terminal || len(p.Oversized) != 1 {
		t.Fatalf("oversized page=%#v err=%v", p, err)
	}
	if got := p.SerializedBytes(); got > 1024 {
		t.Fatalf("serialized bytes=%d", got)
	}
	empty := testSnapshot("v2", false)
	p, err = empty.Page("", 1, 1024)
	if err != nil || !p.Terminal || p.Status == nil {
		t.Fatalf("empty page=%#v err=%v", p, err)
	}
}

func TestCursorRejectsUnknownFieldsAndIsIdempotent(t *testing.T) {
	s := testSnapshot("v1", true, "p/a", "p/b")
	p, err := s.Page("", 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(p.Next, 1, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(p.Next, 1, 1024); err != nil {
		t.Fatalf("retry not idempotent: %v", err)
	}
}

func TestCursorRejectsAuthenticatedUnknownAndTrailingFields(t *testing.T) {
	s := testSnapshot("v1", true, "p/a")
	payload := []byte(`{"schema":"model-list-v1","binding":"root","snapshot":"v1","offset":0,"count":1,"bytes":1024,"extra":1}`)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
	if _, err := s.Page(token, 1, 1024); err == nil {
		t.Fatal("authenticated unknown field accepted")
	}
	valid := cursor{"model-list-v1", "root", "v1", 0, 1, 1024}
	b, _ := json.Marshal(valid)
	b = append(b, []byte(` {}`)...)
	mac = hmac.New(sha256.New, s.key)
	_, _ = mac.Write(b)
	token = base64.RawURLEncoding.EncodeToString(append(b, mac.Sum(nil)...))
	if _, err := s.Page(token, 1, 1024); err == nil {
		t.Fatal("authenticated trailing data accepted")
	}
}

func testSnapshot(version string, complete bool, choices ...string) Snapshot {
	return Snapshot{Version: version, Complete: complete, Choices: choices, key: []byte("test-key"), mu: &sync.Mutex{}}
}
