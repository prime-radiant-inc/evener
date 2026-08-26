package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newLegacyTreeHTTPTestServer(t *testing.T, source *testNavigationSource) (*WebServer, *testNavigationSource) {
	t.Helper()
	web := &WebServer{navigation: newTestNavigationService(t, source)}
	return web, source
}

func doLegacyTreeHTTP(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	return rec
}

func TestLegacyTreeHTTPRepeatedRequestsUseOneCachedBuild(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web, _ := newLegacyTreeHTTPTestServer(t, source)
	var projector atomic.Int32
	web.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		projector.Add(1)
		return web.buildLegacyTreeResponse(inputs)
	}
	h := web.Handler()
	first := doLegacyTreeHTTP(t, h, "/api/tree")
	second := doLegacyTreeHTTP(t, h, "/api/tree")
	if first.Code != http.StatusOK || second.Code != http.StatusOK || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("responses: first=%d second=%d equal=%t", first.Code, second.Code, bytes.Equal(first.Body.Bytes(), second.Body.Bytes()))
	}
	if source.captureCount() != 1 || projector.Load() != 1 {
		t.Fatalf("capture=%d projector=%d, want one each", source.captureCount(), projector.Load())
	}
	stats := web.navigation.Stats()
	if stats.Cache.Misses != 1 || stats.Cache.Hits != 1 || stats.Cache.Coalesced != 0 {
		t.Fatalf("cache stats=%+v", stats.Cache)
	}
	cap := web.navigation.Capability()
	if cap == nil || cap.Sequence != 0 {
		t.Fatalf("capability=%+v, want unchanged sequence", cap)
	}
	select {
	case <-web.navigation.publicationReady:
		t.Fatal("unexpected publication-ready token")
	default:
	}
	if got := web.navigation.DrainPublications(); len(got) != 0 {
		t.Fatalf("publications=%+v", got)
	}
}

func TestLegacyTreeHTTPConcurrentRequestsCoalesce(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web, _ := newLegacyTreeHTTPTestServer(t, source)
	entered := make(chan struct{})
	release := make(chan struct{})
	var projector atomic.Int32
	web.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		if projector.Add(1) == 1 {
			close(entered)
			<-release
		}
		return web.buildLegacyTreeResponse(inputs)
	}
	h := web.Handler()
	const n = 8
	responses := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range responses {
		go func(i int) {
			defer wg.Done()
			responses[i] = doLegacyTreeHTTP(t, h, "/api/tree")
		}(i)
	}
	<-entered
	close(release)
	wg.Wait()
	for i, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i, response.Code, response.Body.String())
		}
		if i > 0 && !bytes.Equal(response.Body.Bytes(), responses[0].Body.Bytes()) {
			t.Fatalf("request %d returned different bytes", i)
		}
	}
	stats := web.navigation.Stats()
	if projector.Load() != 1 || stats.Cache.Misses != 1 || stats.Cache.Coalesced != n-1 {
		t.Fatalf("projector=%d cache=%+v, want one build and %d coalesced", projector.Load(), stats.Cache, n-1)
	}
}

func TestLegacyTreeHTTPSummaryAndFullCachesAreIsolated(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web, _ := newLegacyTreeHTTPTestServer(t, source)
	var full atomic.Int32
	web.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		full.Add(1)
		return web.buildLegacyTreeResponse(inputs)
	}
	h := web.Handler()
	fullFirst := doLegacyTreeHTTP(t, h, "/api/tree")
	summaryFirst := doLegacyTreeHTTP(t, h, "/api/tree?summary=1")
	summarySecond := doLegacyTreeHTTP(t, h, "/api/tree?summary=1")
	fullSecond := doLegacyTreeHTTP(t, h, "/api/tree")
	for _, response := range []*httptest.ResponseRecorder{fullFirst, summaryFirst, summarySecond, fullSecond} {
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if full.Load() != 1 || source.captureCount() != 1 {
		t.Fatalf("full projector=%d captures=%d, want one full projection and one source capture", full.Load(), source.captureCount())
	}
	if !bytes.Equal(summaryFirst.Body.Bytes(), summarySecond.Body.Bytes()) || !bytes.Equal(fullFirst.Body.Bytes(), fullSecond.Body.Bytes()) {
		t.Fatal("cached shape bytes changed")
	}
	var summary struct {
		GeneratedAt time.Time `json:"generated_at"`
	}
	if err := json.Unmarshal(summaryFirst.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	var fullResponse struct {
		GeneratedAt time.Time `json:"generated_at"`
	}
	if err := json.Unmarshal(fullFirst.Body.Bytes(), &fullResponse); err != nil {
		t.Fatal(err)
	}
	if summary.GeneratedAt.IsZero() || fullResponse.GeneratedAt.IsZero() {
		t.Fatal("missing generated_at")
	}
	source.changeTitle("new-revision")
	fullRevision := doLegacyTreeHTTP(t, h, "/api/tree")
	if fullRevision.Code != http.StatusOK || bytes.Equal(fullFirst.Body.Bytes(), fullRevision.Body.Bytes()) {
		t.Fatal("new build revision reused the old full representation")
	}
	var changed struct {
		GeneratedAt time.Time `json:"generated_at"`
	}
	if err := json.Unmarshal(fullRevision.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if !changed.GeneratedAt.After(fullResponse.GeneratedAt) {
		t.Fatalf("generated_at=%s after revision, want after %s", changed.GeneratedAt, fullResponse.GeneratedAt)
	}
}

func TestLegacyTreeHTTPServersDoNotShareSourceCacheOrAuth(t *testing.T) {
	sourceA := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	sourceB := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	sourceB.changeTitle("server-b")
	webA, _ := newLegacyTreeHTTPTestServer(t, sourceA)
	webB, _ := newLegacyTreeHTTPTestServer(t, sourceB)
	webA.cfg.AuthToken, webB.cfg.AuthToken = "token-a", "token-b"
	var buildsA, buildsB atomic.Int32
	webA.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		buildsA.Add(1)
		return webA.buildLegacyTreeResponse(inputs)
	}
	webB.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		buildsB.Add(1)
		return webB.buildLegacyTreeResponse(inputs)
	}
	a := webA.Handler()
	b := webB.Handler()
	request := func(h http.Handler, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/tree", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	firstA, firstB := request(a, "token-a"), request(b, "token-b")
	secondA, secondB := request(a, "token-a"), request(b, "token-b")
	for name, response := range map[string]*httptest.ResponseRecorder{"a1": firstA, "b1": firstB, "a2": secondA, "b2": secondB} {
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", name, response.Code)
		}
	}
	if bytes.Equal(firstA.Body.Bytes(), firstB.Body.Bytes()) || !bytes.Equal(firstA.Body.Bytes(), secondA.Body.Bytes()) || !bytes.Equal(firstB.Body.Bytes(), secondB.Body.Bytes()) {
		t.Fatal("server bodies were not independently cached")
	}
	if buildsA.Load() != 1 || buildsB.Load() != 1 || sourceA.captureCount() != 1 || sourceB.captureCount() != 1 {
		t.Fatalf("builds=(%d,%d) captures=(%d,%d)", buildsA.Load(), buildsB.Load(), sourceA.captureCount(), sourceB.captureCount())
	}
}

func TestLegacyTreeHTTPCurrentRevisionFailureDoesNotServeStale(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web, _ := newLegacyTreeHTTPTestServer(t, source)
	var fail atomic.Bool
	web.legacyTreeBuild = func(inputs navigationBuildInputs) (any, error) {
		if fail.Load() {
			return func() {}, nil
		}
		return web.buildLegacyTreeResponse(inputs)
	}
	h := web.Handler()
	first := doLegacyTreeHTTP(t, h, "/api/tree")
	if first.Code != http.StatusOK {
		t.Fatalf("initial status=%d", first.Code)
	}
	source.changeTitle("revision-two")
	fail.Store(true)
	failed := doLegacyTreeHTTP(t, h, "/api/tree")
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("failed status=%d body=%s", failed.Code, failed.Body.String())
	}
	source.mu.Lock()
	source.err = nil
	source.mu.Unlock()
	fail.Store(false)
	retry := doLegacyTreeHTTP(t, h, "/api/tree")
	if retry.Code != http.StatusOK || !bytes.Contains(retry.Body.Bytes(), []byte("revision-two")) {
		t.Fatalf("retry status/body=%d %s", retry.Code, retry.Body.String())
	}
}
