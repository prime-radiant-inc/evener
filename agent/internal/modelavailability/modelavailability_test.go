package modelavailability

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestCaptureKeepsSuccessfulChoicesWhenAnotherProviderFails(t *testing.T) {
	fetch := func(_ context.Context, name string) ([]llm.ModelInfo, error) {
		if name == "failed" {
			return nil, errors.New("unavailable")
		}
		return []llm.ModelInfo{{ID: "z"}, {ID: "a"}, {ID: "a"}}, nil
	}
	s := Capture(context.Background(), []string{"failed", "good"}, "", fetch, nil, time.Hour)
	if s.Complete || len(s.Choices) != 2 || s.Choices[0] != "good/a" || s.Choices[1] != "good/z" {
		t.Fatalf("snapshot = %#v", s)
	}
	if s.Status["failed"].Kind != StatusFailure || s.Status["good"].Kind != StatusSuccess {
		t.Fatalf("status = %#v", s.Status)
	}
}

func TestCaptureStopsAtOneDeterministicDeadline(t *testing.T) {
	parent := &deadlineBarrierContext{done: make(chan struct{})}
	started := make(chan struct{}, 2)
	done := make(chan Snapshot, 1)
	go func() {
		done <- Capture(parent, []string{"first", "second"}, "", func(ctx context.Context, _ string) ([]llm.ModelInfo, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}, nil, time.Hour)
	}()
	<-started
	<-started
	close(parent.done)

	s := <-done
	if s.Complete || s.Status["first"].Kind != StatusTimeout || s.Status["second"].Kind != StatusTimeout {
		t.Fatalf("deadline snapshot = %#v", s)
	}
}

func TestCaptureRejectsUnsafeModelIdentifiers(t *testing.T) {
	models := []llm.ModelInfo{
		{ID: "safe"},
		{ID: " padded "},
		{ID: "space inside"},
		{ID: "line\nbreak"},
		{ID: "bidi\u202eoverride"},
		{ID: strings.Repeat("x", 257)},
		{ID: string([]byte{0xff})},
	}
	snapshot := Capture(context.Background(), []string{"provider"}, "", func(context.Context, string) ([]llm.ModelInfo, error) {
		return models, nil
	}, nil, time.Second)

	want := []string{"provider/padded", "provider/safe"}
	if !slices.Equal(snapshot.Choices, want) {
		t.Fatalf("choices = %q, want %q", snapshot.Choices, want)
	}
	if snapshot.Complete || snapshot.Status["provider"].Kind != StatusLimited {
		t.Fatalf("unsafe omissions were not reported as incomplete: %#v", snapshot)
	}
}

func TestCaptureRejectsProviderNamesThatCouldExpandThePageEnvelope(t *testing.T) {
	providers := []string{"safe", "bad&name", `bad"name`, strings.Repeat("x", 65)}
	var calls atomic.Int32
	snapshot := Capture(context.Background(), providers, "", func(context.Context, string) ([]llm.ModelInfo, error) {
		calls.Add(1)
		return []llm.ModelInfo{{ID: "model"}}, nil
	}, nil, time.Second)

	if got := calls.Load(); got != 1 {
		t.Fatalf("provider fetches = %d, want only the safe provider", got)
	}
	if snapshot.Complete || !slices.Equal(snapshot.Choices, []string{"safe/model"}) {
		t.Fatalf("provider-sanitized snapshot = %#v", snapshot)
	}
}

func TestCaptureEnforcesProviderModelAndByteBounds(t *testing.T) {
	t.Run("providers", func(t *testing.T) {
		providers := make([]string, 65)
		for i := range providers {
			providers[i] = fmt.Sprintf("provider-%02d", len(providers)-1-i)
		}
		var calls atomic.Int32
		snapshot := Capture(context.Background(), providers, "", func(_ context.Context, name string) ([]llm.ModelInfo, error) {
			calls.Add(1)
			return []llm.ModelInfo{{ID: "model"}}, nil
		}, nil, time.Second)
		if got := calls.Load(); got != 16 {
			t.Fatalf("provider fetches = %d, want bounded at 16", got)
		}
		if snapshot.Complete || len(snapshot.Choices) != 16 {
			t.Fatalf("provider-bounded snapshot = %#v", snapshot)
		}
		if snapshot.Choices[0] != "provider-00/model" || snapshot.Choices[15] != "provider-15/model" {
			t.Fatalf("provider bound was not deterministic: first=%q last=%q", snapshot.Choices[0], snapshot.Choices[15])
		}
	})

	t.Run("models", func(t *testing.T) {
		models := make([]llm.ModelInfo, 4097)
		for i := range models {
			models[i].ID = fmt.Sprintf("model-%04d", i)
		}
		snapshot := Capture(context.Background(), []string{"provider"}, "", func(context.Context, string) ([]llm.ModelInfo, error) {
			return models, nil
		}, nil, time.Second)
		if len(snapshot.Choices) != 4096 || snapshot.Complete || snapshot.Status["provider"].Kind != StatusLimited {
			t.Fatalf("model-bounded snapshot: choices=%d complete=%v status=%#v", len(snapshot.Choices), snapshot.Complete, snapshot.Status)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		models := make([]llm.ModelInfo, 4096)
		for i := range models {
			models[i].ID = fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 195))
		}
		snapshot := Capture(context.Background(), []string{"provider"}, "", func(context.Context, string) ([]llm.ModelInfo, error) {
			return models, nil
		}, nil, time.Second)
		var capturedBytes int
		for _, choice := range snapshot.Choices {
			capturedBytes += len([]byte(choice))
		}
		if capturedBytes > 256*1024 {
			t.Fatalf("captured choice bytes = %d, want at most %d", capturedBytes, 256*1024)
		}
		if snapshot.Complete || snapshot.Status["provider"].Kind != StatusLimited {
			t.Fatalf("byte truncation was not reported as incomplete: %#v", snapshot.Status)
		}
	})
}

func TestCaptureStopsFilteringAtTheFirstHardBound(t *testing.T) {
	models := make([]llm.ModelInfo, captureMaxModels+100)
	for i := range models {
		models[i].ID = fmt.Sprintf("model-%04d", i)
	}
	var visibilityCalls atomic.Int32
	snapshot := Capture(context.Background(), []string{"provider"}, "", func(context.Context, string) ([]llm.ModelInfo, error) {
		return models, nil
	}, func(string, llm.ModelInfo) bool {
		visibilityCalls.Add(1)
		return true
	}, time.Hour)

	if got := visibilityCalls.Load(); got != captureMaxModels {
		t.Fatalf("visibility checks = %d, want hard stop at %d", got, captureMaxModels)
	}
	if snapshot.Complete || len(snapshot.Choices) != captureMaxModels || snapshot.Status["provider"].Kind != StatusLimited {
		t.Fatalf("hard-bounded snapshot: choices=%d complete=%v status=%#v", len(snapshot.Choices), snapshot.Complete, snapshot.Status)
	}
}

func TestBoundedModelIDsRejectsCanceledResults(t *testing.T) {
	models := []llm.ModelInfo{{ID: "first"}, {ID: "second"}}
	t.Run("before filtering", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var visibilityCalls int
		ids, limited, err := boundedModelIDs(ctx, "provider", models, func(string, llm.ModelInfo) bool {
			visibilityCalls++
			return true
		})
		if !errors.Is(err, context.Canceled) || ids != nil || limited || visibilityCalls != 0 {
			t.Fatalf("canceled result = ids:%q limited:%v calls:%d err:%v", ids, limited, visibilityCalls, err)
		}
	})

	t.Run("during filtering", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var visibilityCalls int
		ids, limited, err := boundedModelIDs(ctx, "provider", models, func(string, llm.ModelInfo) bool {
			visibilityCalls++
			cancel()
			return true
		})
		if !errors.Is(err, context.Canceled) || ids != nil || limited || visibilityCalls != 1 {
			t.Fatalf("mid-filter cancellation = ids:%q limited:%v calls:%d err:%v", ids, limited, visibilityCalls, err)
		}
	})
}

func TestCaptureProviderBoundKeepsPublicPageEnvelopeUsable(t *testing.T) {
	providers := make([]string, 65)
	for i := range providers {
		providers[i] = fmt.Sprintf("provider-%02d-%s", i, strings.Repeat("x", 52))
	}
	snapshot := Capture(context.Background(), providers, "", func(context.Context, string) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "model"}}, nil
	}, nil, time.Second)

	page, err := snapshot.Page("", DefaultPageMaxCount, DefaultPageMaxBytes)
	if err != nil {
		t.Fatalf("bounded capture produced an unusable page envelope: %v", err)
	}
	if got := page.SerializedBytes(); got > DefaultPageMaxBytes {
		t.Fatalf("page envelope = %d bytes, exceeds %d", got, DefaultPageMaxBytes)
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

func TestCursorRejectsChangedPageBounds(t *testing.T) {
	s := testSnapshot("v1", true, "p/a", "p/b")
	page, err := s.Page("", 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Page(page.Next, 2, 1024); err == nil {
		t.Fatal("cursor accepted a changed count bound")
	}
	if _, err := s.Page(page.Next, 1, 512); err == nil {
		t.Fatal("cursor accepted a changed byte bound")
	}
}

func TestPageReturnsBoundedEnvelopeAndProgressesOversizedOrEmptySnapshots(t *testing.T) {
	s := testSnapshot("v1", false, strings.Repeat("x", 1000))
	p, err := s.Page("", 1, 256)
	if err != nil || !p.Terminal || len(p.Oversized) != 1 {
		t.Fatalf("oversized page=%#v err=%v", p, err)
	}
	if got := p.SerializedBytes(); got > 256 {
		t.Fatalf("serialized bytes=%d, want at most 256", got)
	}
	empty := testSnapshot("v2", false)
	p, err = empty.Page("", 1, 1024)
	if err != nil || !p.Terminal || p.Status == nil {
		t.Fatalf("empty page=%#v err=%v", p, err)
	}
}

func TestPageRejectsTerminalEnvelopeOverByteBound(t *testing.T) {
	s := testSnapshot("v1", false)
	s.Status = map[string]ProviderStatus{
		"provider": {Kind: StatusFailure, Detail: strings.Repeat("x", 256)},
	}

	page, err := s.Page("", 1, 128)
	if err == nil {
		t.Fatalf("Page returned oversized terminal envelope (%d bytes): %#v", page.SerializedBytes(), page)
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
	return Snapshot{Version: version, Complete: complete, Choices: choices, key: []byte("test-key")}
}

type deadlineBarrierContext struct {
	done chan struct{}
}

func (*deadlineBarrierContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *deadlineBarrierContext) Done() <-chan struct{}     { return c.done }
func (c *deadlineBarrierContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (*deadlineBarrierContext) Value(any) any { return nil }
