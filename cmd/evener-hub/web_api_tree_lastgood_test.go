package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type stubThreadLister struct {
	id    string
	resp  appwire.ThreadListResponse
	err   error
	calls int
}

func (s *stubThreadLister) ID() string { return s.id }

func (s *stubThreadLister) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	s.calls++
	return s.resp, s.err
}

func threadIDs(threads []appwire.Thread) []string {
	out := make([]string, 0, len(threads))
	for _, t := range threads {
		out = append(out, t.ID)
	}
	return out
}

func TestListThreadsWithFallbackRetainsLastGood(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{}}
	lister := &stubThreadLister{
		id:   "codex",
		resp: appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}, {ID: "th_2"}}},
	}

	// First call succeeds and is cached.
	got := s.listThreadsWithFallback(context.Background(), lister)
	if want := []string{"th_1", "th_2"}; !equalStrings(threadIDs(got), want) {
		t.Fatalf("first list = %v, want %v", threadIDs(got), want)
	}

	// A transient error must not blank the source — last-known-good is retained.
	lister.resp = appwire.ThreadListResponse{}
	lister.err = errors.New("dial timeout")
	got = s.listThreadsWithFallback(context.Background(), lister)
	if want := []string{"th_1", "th_2"}; !equalStrings(threadIDs(got), want) {
		t.Fatalf("error list = %v, want retained %v", threadIDs(got), want)
	}

	// A successful empty list does clear the cache — a genuinely-gone source
	// ages out rather than lingering forever.
	lister.err = nil
	lister.resp = appwire.ThreadListResponse{Data: nil}
	got = s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("empty success list = %v, want empty", threadIDs(got))
	}

	// And after clearing, an error returns empty (nothing to retain).
	lister.err = errors.New("dial timeout")
	got = s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("error after clear = %v, want empty", threadIDs(got))
	}
}

func TestListThreadsWithFallbackInitialCallErrorsEmpty(t *testing.T) {
	s := &WebServer{cfg: hubcore.WebConfig{}}
	lister := &stubThreadLister{id: "codex", err: errors.New("down")}
	got := s.listThreadsWithFallback(context.Background(), lister)
	if len(got) != 0 {
		t.Fatalf("first-call error = %v, want empty", threadIDs(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLegacyRepresentationCachesAndSeparatesShapes(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	var builds atomic.Int32
	build := func(inputs navigationBuildInputs) (any, error) {
		builds.Add(1)
		return map[string]any{"title": inputs.Tree.Projects[0].Name}, nil
	}
	first, err := service.LegacyRepresentation(t.Context(), "full", build)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.LegacyRepresentation(t.Context(), "full", build)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.JSON) != string(second.JSON) || builds.Load() != 1 {
		t.Fatalf("cache builds=%d bytes differ", builds.Load())
	}
	if _, err := service.LegacyRepresentation(t.Context(), "summary", build); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 2 {
		t.Fatalf("shape keys shared build count=%d", builds.Load())
	}
	if _, err := json.Marshal(first.Object); err != nil {
		t.Fatal(err)
	}
	if len(first.JSON) == 0 || first.JSON[len(first.JSON)-1] != '\n' {
		start := len(first.JSON) - 8
		if start < 0 {
			start = 0
		}
		t.Fatalf("legacy JSON framing = %q, want trailing newline", first.JSON[start:])
	}
	if string(first.JSON) != string(second.JSON) {
		t.Fatal("cached legacy encoding changed between requests")
	}
}

func TestLegacyRepresentationRevisionAndConcurrentCoalescing(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	entered, release := make(chan struct{}), make(chan struct{})
	var builds atomic.Int32
	build := func(navigationBuildInputs) (any, error) {
		builds.Add(1)
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return map[string]string{"ok": "yes"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.LegacyRepresentation(context.Background(), "full", build); err != nil {
				t.Errorf("legacy: %v", err)
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("concurrent builds=%d, want 1", builds.Load())
	}
	source.changeTitle("changed")
	if _, err := service.LegacyRepresentation(t.Context(), "full", build); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != 2 {
		t.Fatalf("revision builds=%d, want 2", builds.Load())
	}
}

func TestLegacyRepresentationBuildFailureIsExplicitAndNotCached(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	good := func(navigationBuildInputs) (any, error) { return map[string]string{"good": "yes"}, nil }
	if _, err := service.LegacyRepresentation(t.Context(), "full", good); err != nil {
		t.Fatal(err)
	}
	bad := func(navigationBuildInputs) (any, error) { return nil, errors.New("legacy build failed") }
	if _, err := service.LegacyRepresentation(t.Context(), "other", bad); err == nil {
		t.Fatal("failed legacy build returned success")
	}
	if _, err := service.LegacyRepresentation(t.Context(), "full", bad); err != nil {
		t.Fatal("last good entry was poisoned: ", err)
	}
}

func TestLegacyRepresentationColdFailureRetriesSameKey(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	var builds atomic.Int32
	build := func(navigationBuildInputs) (any, error) {
		if builds.Add(1) == 1 {
			return nil, errors.New("cold failure")
		}
		return map[string]string{"ok": "yes"}, nil
	}
	if _, err := service.LegacyRepresentation(t.Context(), "retry", build); err == nil {
		t.Fatal("cold failure returned success")
	}
	if _, err := service.LegacyRepresentation(t.Context(), "retry", build); err != nil {
		t.Fatal("retry failed: ", err)
	}
	if builds.Load() != 2 {
		t.Fatalf("builds=%d, want 2", builds.Load())
	}
}

func TestLegacyRepresentationEncodeFailureRetriesSameKey(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	var builds atomic.Int32
	build := func(navigationBuildInputs) (any, error) {
		if builds.Add(1) == 1 {
			return make(chan int), nil // json cannot encode channels
		}
		return map[string]string{"ok": "yes"}, nil
	}
	if _, err := service.LegacyRepresentation(t.Context(), "encode-retry", build); err == nil {
		t.Fatal("encode failure returned success")
	}
	if _, err := service.LegacyRepresentation(t.Context(), "encode-retry", build); err != nil {
		t.Fatal("encode retry failed: ", err)
	}
	if builds.Load() != 2 {
		t.Fatalf("builds=%d, want 2", builds.Load())
	}
}

func TestLegacyRepresentationFreezesGeneratedAtPerRevision(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	service := newTestNavigationService(t, source)
	build := func(inputs navigationBuildInputs) (any, error) {
		return struct {
			GeneratedAt time.Time `json:"generated_at"`
			Title       string    `json:"title"`
		}{hubNavigationNow(), inputs.Tree.Projects[0].Current[0].Title}, nil
	}
	first, err := service.LegacyRepresentation(t.Context(), "frozen", build)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.LegacyRepresentation(t.Context(), "frozen", build)
	if err != nil || string(first.JSON) != string(second.JSON) {
		t.Fatalf("cached generated_at changed: %v", err)
	}
	source.changeTitle("new revision")
	third, err := service.LegacyRepresentation(t.Context(), "frozen", build)
	if err != nil || string(first.JSON) == string(third.JSON) {
		t.Fatalf("new revision did not change frozen representation: %v", err)
	}
}
