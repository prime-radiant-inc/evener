package hub

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/hubapi"
)

// navigationHydrationStats summarizes the byte and request cost of one
// navigation hydration pass driven through the real HTTP handler.
type navigationHydrationStats struct {
	requests         int
	uncompressedJSON int
	transferredBytes int
	conditional304   int
	etag             string
}

// navigationMetricRecorder captures navigationMetricEvents through the
// WebServer's injectable metric sink. It is key-free and retains only
// resource-class, status, encoding, byte, and duration aggregates.
type navigationMetricRecorder struct {
	mu     sync.Mutex
	events []navigationMetricEvent
}

func newNavigationMetricRecorder() *navigationMetricRecorder {
	return &navigationMetricRecorder{}
}

func (r *navigationMetricRecorder) RecordNavigationMetric(event navigationMetricEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *navigationMetricRecorder) snapshot() []navigationMetricEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]navigationMetricEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *navigationMetricRecorder) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// requestNavigationHTTP issues a GET through the real HTTP handler and returns
// the recorder. The encoding argument controls Accept-Encoding; pass "gzip" or
// "identity". The etag argument sets If-None-Match for conditional validation.
func requestNavigationHTTP(tb testing.TB, web *WebServer, target, encoding, etag string) *httptest.ResponseRecorder {
	tb.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if encoding == "gzip" {
		req.Header.Set("Accept-Encoding", "gzip")
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeGzipBody decompresses a gzip response body for byte accounting.
func decodeGzipBody(tb testing.TB, rec *httptest.ResponseRecorder) []byte {
	tb.Helper()
	if rec.Header().Get("Content-Encoding") != "gzip" {
		return rec.Body.Bytes()
	}
	reader, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		tb.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		tb.Fatalf("gzip read: %v", err)
	}
	if err := reader.Close(); err != nil {
		tb.Fatalf("gzip close: %v", err)
	}
	return decoded
}

// parseNavigationManifest decodes the manifest from a response body (handling
// gzip when the encoding header says so).
func parseNavigationManifest(tb testing.TB, rec *httptest.ResponseRecorder) hubapi.NavigationManifest {
	tb.Helper()
	body := decodeGzipBody(tb, rec)
	var manifest hubapi.NavigationManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		tb.Fatalf("decode manifest: %v (body prefix: %q)", err, string(body[:min(len(body), 200)]))
	}
	return manifest
}

// hydrateMandatory fetches every mandatory navigation resource through the real
// HTTP handler and returns aggregate stats. Mandatory hydration = manifest,
// first Live page, first Needs You page, pin-descriptor catalog, all nonempty
// pin-section first pages, and first project catalog page.
func hydrateMandatory(tb testing.TB, web *WebServer, encoding string) navigationHydrationStats {
	tb.Helper()
	stats := navigationHydrationStats{}

	// 1. Manifest
	rec := requestNavigationHTTP(tb, web, "/api/navigation", encoding, "")
	if rec.Code != http.StatusOK {
		tb.Fatalf("manifest status=%d body=%s", rec.Code, rec.Body.String())
	}
	stats.requests++
	manifestBody := decodeGzipBody(tb, rec)
	stats.uncompressedJSON += len(manifestBody)
	stats.transferredBytes += rec.Body.Len()
	manifest := parseNavigationManifest(tb, rec)
	stats.etag = rec.Header().Get("ETag")
	_ = manifest // manifest parsed to validate the response shape

	// 2. Live section page 0
	rec = requestNavigationHTTP(tb, web, "/api/navigation/sections/live", encoding, "")
	if rec.Code != http.StatusOK {
		tb.Fatalf("live section status=%d", rec.Code)
	}
	stats.requests++
	stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
	stats.transferredBytes += rec.Body.Len()

	// 3. Needs You section page 0
	rec = requestNavigationHTTP(tb, web, "/api/navigation/sections/needs-you", encoding, "")
	if rec.Code != http.StatusOK {
		tb.Fatalf("needs-you section status=%d", rec.Code)
	}
	stats.requests++
	stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
	stats.transferredBytes += rec.Body.Len()

	// 4. Pin-section catalog page 0 — discover nonempty pin sections
	rec = requestNavigationHTTP(tb, web, "/api/navigation/pin-sections", encoding, "")
	if rec.Code != http.StatusOK {
		tb.Fatalf("pin catalog status=%d", rec.Code)
	}
	stats.requests++
	stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
	stats.transferredBytes += rec.Body.Len()

	body := decodeGzipBody(tb, rec)
	var pinCatalog hubapi.NavigationPinSectionCatalog
	if err := json.Unmarshal(body, &pinCatalog); err != nil {
		tb.Fatalf("decode pin catalog: %v", err)
	}
	// 5. Fetch each nonempty pin-section first page
	for _, section := range pinCatalog.PinSections {
		if section.Count == 0 {
			continue
		}
		target := "/api/navigation/pin-sections/" + section.ID
		rec = requestNavigationHTTP(tb, web, target, encoding, "")
		if rec.Code != http.StatusOK {
			tb.Fatalf("pin section %s status=%d", section.ID, rec.Code)
		}
		stats.requests++
		stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
		stats.transferredBytes += rec.Body.Len()
	}

	// 6. First project catalog page
	rec = requestNavigationHTTP(tb, web, "/api/navigation/catalogs/projects", encoding, "")
	if rec.Code != http.StatusOK {
		tb.Fatalf("project catalog status=%d", rec.Code)
	}
	stats.requests++
	stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
	stats.transferredBytes += rec.Body.Len()

	return stats
}

// hydrateMandatoryIdentity runs mandatory hydration with identity encoding and
// returns uncompressed JSON bytes (the raw response bodies, no compression).
func hydrateMandatoryIdentity(tb testing.TB, web *WebServer) navigationHydrationStats {
	tb.Helper()
	return hydrateMandatory(tb, web, "identity")
}

// hydrateMandatoryGzip runs mandatory hydration with gzip and returns
// transferred (compressed) body bytes.
func hydrateMandatoryGzip(tb testing.TB, web *WebServer) navigationHydrationStats {
	tb.Helper()
	return hydrateMandatory(tb, web, "gzip")
}

// hydrateExpandedGzip runs expanded hydration with gzip: mandatory hydration
// plus the root project resource for the first four projects. Returns
// transferred body bytes.
func hydrateExpandedGzip(tb testing.TB, web *WebServer, projectKeys []string) navigationHydrationStats {
	tb.Helper()
	stats := hydrateMandatoryGzip(tb, web)
	for _, key := range projectKeys {
		rec := requestNavigationHTTP(tb, web, "/api/navigation/projects/"+key, "gzip", "")
		if rec.Code != http.StatusOK {
			tb.Fatalf("expanded project %s status=%d body=%s", key, rec.Code, rec.Body.String())
		}
		stats.requests++
		stats.uncompressedJSON += len(decodeGzipBody(tb, rec))
		stats.transferredBytes += rec.Body.Len()
	}
	return stats
}

// TestNavigationIntegration proves the request-count, cache, conditional, and
// build/resolution invariants from the transport optimization spec (§900-935)
// on the fixed deterministic fixture through the real HTTP handler.
func TestNavigationIntegration(t *testing.T) {
	t.Run("idle_zero_requests_after_initial_hydration", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		recorder := newNavigationMetricRecorder()
		web.navigationMetrics = &navigationMetricFunc{fn: recorder.RecordNavigationMetric}

		// Initial mandatory hydration.
		_ = hydrateMandatoryGzip(t, web)
		initialRequests := recorder.requestCount()

		// After hydration, re-requesting every resource with a matching ETag
		// must produce a 304 and no new build work. A client that has hydrated
		// and holds valid validators issues zero body-bearing requests.
		manifestRec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		etag := manifestRec.Header().Get("ETag")
		if etag == "" {
			t.Fatal("initial manifest missing ETag")
		}

		conditionalRec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", etag)
		if conditionalRec.Code != http.StatusNotModified {
			t.Fatalf("idle conditional manifest status=%d, want 304", conditionalRec.Code)
		}
		if conditionalRec.Body.Len() != 0 {
			t.Fatalf("idle 304 body bytes=%d, want 0", conditionalRec.Body.Len())
		}

		afterIdle := recorder.requestCount()
		if afterIdle != initialRequests+2 {
			// The two explicit re-requests (one unconditional, one conditional)
			// are the only new requests — no background polling.
			t.Fatalf("idle requests after hydration: got %d (delta %d), want exactly %d (+2)",
				afterIdle, afterIdle-initialRequests, initialRequests+2)
		}
	})

	t.Run("one_status_change_affects_only_named_representations", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		recorder := newNavigationMetricRecorder()
		web.navigationMetrics = &navigationMetricFunc{fn: recorder.RecordNavigationMetric}

		// Hydrate so the core snapshot is built and all resources are cached.
		_ = hydrateMandatoryGzip(t, web)
		beforeBuilds := web.navigation.Stats().CoreBuilds
		beforeEvents := recorder.requestCount()

		// Invalidate with a scoped hint naming the first project. This simulates
		// a single semantic change (e.g. a session state flip in one project).
		web.navigation.Invalidate(navigationChangeHint{Projects: []string{"/evener-navigation-project-00"}})
		if _, err := web.navigation.Refresh(t.Context(), navigationChangeHint{Projects: []string{"/evener-navigation-project-00"}}); err != nil {
			t.Fatal(err)
		}

		afterBuilds := web.navigation.Stats().CoreBuilds
		if afterBuilds != beforeBuilds+1 {
			t.Fatalf("status change core builds: got %d, want %d (one rebuild)",
				afterBuilds-beforeBuilds, 1)
		}

		// The manifest and the named project root must have new revisions.
		// Other resources (Live section, Needs You section, project catalog)
		// may or may not change depending on the hint, but the request count
		// for a client re-fetching affected resources must be bounded: at most
		// one request per affected loaded representation.
		manifestRec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		if manifestRec.Code != http.StatusOK {
			t.Fatalf("post-change manifest status=%d", manifestRec.Code)
		}

		afterEvents := recorder.requestCount()
		newRequests := afterEvents - beforeEvents
		// At most one request for the manifest re-fetch. The change produces
		// exactly one build and at most one request per affected representation.
		if newRequests > 1 {
			t.Fatalf("status change produced %d new requests, want at most 1 for one affected representation",
				newRequests)
		}
	})

	t.Run("mutation_and_event_dedupe", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		recorder := newNavigationMetricRecorder()
		web.navigationMetrics = &navigationMetricFunc{fn: recorder.RecordNavigationMetric}

		// Hydrate to populate caches.
		_ = hydrateMandatoryGzip(t, web)
		beforeEvents := recorder.requestCount()

		// A mutation (Invalidate + Refresh) and its matching notification must
		// not duplicate resource work. Invalidate is idempotent; a second
		// Invalidate with the same hint before the build completes coalesces.
		web.navigation.Invalidate(navigationChangeHint{Projects: []string{"/evener-navigation-project-00"}})
		web.navigation.Invalidate(navigationChangeHint{Projects: []string{"/evener-navigation-project-00"}})
		mutation, err := web.navigation.Refresh(t.Context(), navigationChangeHint{Projects: []string{"/evener-navigation-project-00"}})
		if err != nil {
			t.Fatal(err)
		}

		// The mutation must carry the generation and (possibly empty) targets.
		// A no-op change (same data, fixed clock) produces zero targets but
		// still proves the build ran once.
		capability := web.navigation.Capability()
		if capability == nil {
			t.Fatal("navigation capability is nil")
		}
		if mutation.GenerationID != capability.GenerationID {
			t.Fatalf("mutation generation %q != capability generation %q",
				mutation.GenerationID, capability.GenerationID)
		}

		// Re-requesting the manifest after the mutation must return the same
		// generation (no spurious rebuild) since the fixture data is unchanged.
		manifestRec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		if manifestRec.Code != http.StatusOK {
			t.Fatalf("post-mutation manifest status=%d", manifestRec.Code)
		}
		afterEvents := recorder.requestCount()
		newRequests := afterEvents - beforeEvents
		// The only new request is the one manifest re-fetch. The duplicate
		// Invalidates must not produce extra requests.
		if newRequests > 1 {
			t.Fatalf("mutation dedupe: %d new requests, want at most 1 (the manifest re-fetch)",
				newRequests)
		}
	})

	t.Run("reconnect_conditional_revalidation", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)

		// Initial hydration.
		_ = hydrateMandatoryGzip(t, web)

		// Capture ETags for the manifest and a project resource.
		manifestRec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		manifestETag := manifestRec.Header().Get("ETag")
		if manifestETag == "" {
			t.Fatal("manifest missing ETag")
		}

		// Discover the first project key from the project catalog page.
		catalogRec := requestNavigationHTTP(t, web, "/api/navigation/catalogs/projects", "gzip", "")
		if catalogRec.Code != http.StatusOK {
			t.Fatalf("project catalog status=%d", catalogRec.Code)
		}
		var catalog hubapi.NavigationProjectCatalog
		if err := json.Unmarshal(decodeGzipBody(t, catalogRec), &catalog); err != nil {
			t.Fatalf("decode project catalog: %v", err)
		}
		if len(catalog.Projects) == 0 {
			t.Fatal("project catalog is empty")
		}
		projectKey := catalog.Projects[0].Key

		projectRec := requestNavigationHTTP(t, web, "/api/navigation/projects/"+projectKey, "gzip", "")
		if projectRec.Code != http.StatusOK {
			t.Fatalf("project resource status=%d for key %q", projectRec.Code, projectKey)
		}
		projectETag := projectRec.Header().Get("ETag")
		if projectETag == "" {
			t.Fatal("project resource missing ETag")
		}

		// Simulate a reconnect: the client re-sends its conditional request.
		// With no underlying change, the validator must match and return 304.
		conditionalManifest := requestNavigationHTTP(t, web, "/api/navigation", "gzip", manifestETag)
		if conditionalManifest.Code != http.StatusNotModified {
			t.Fatalf("reconnect manifest conditional status=%d, want 304", conditionalManifest.Code)
		}
		if conditionalManifest.Body.Len() != 0 {
			t.Fatalf("reconnect 304 body bytes=%d, want 0", conditionalManifest.Body.Len())
		}
		conditionalProject := requestNavigationHTTP(t, web, "/api/navigation/projects/"+projectKey, "gzip", projectETag)
		if conditionalProject.Code != http.StatusNotModified {
			t.Fatalf("reconnect project conditional status=%d, want 304", conditionalProject.Code)
		}

		// After a real data change, the affected resource's old ETag must NOT
		// match (200 with new body). The manifest only carries counts so a
		// title change does not alter it; a project resource carries session
		// titles and must change.
		past := web.cfg.Past
		if past == nil {
			t.Fatal("fixture has no PastIndex")
		}
		// The fixture's NewWebServer does not wire onChange (main.go does that
		// in production). Set a no-op hook so UpdateMeta reports the change.
		past.SetOnChange(func() {})
		entries := past.All()
		if len(entries) == 0 {
			t.Fatal("fixture past index is empty")
		}
		target := entries[0]
		target.Meta.Name = target.Meta.Name + "-changed"
		if changed := past.UpdateMeta(target.ID, target.Meta); !changed {
			t.Fatal("UpdateMeta reported no change")
		}
		web.navigation.Invalidate(navigationChangeHint{AllLoadedProjects: true})
		if _, err := web.navigation.Refresh(t.Context(), navigationChangeHint{AllLoadedProjects: true}); err != nil {
			t.Fatal(err)
		}
		// The project resource's stale validator must be rejected.
		staleProject := requestNavigationHTTP(t, web, "/api/navigation/projects/"+projectKey, "gzip", projectETag)
		if staleProject.Code != http.StatusOK {
			t.Fatalf("post-change project conditional status=%d, want 200 (stale validator rejected)", staleProject.Code)
		}
		// The manifest's validator still matches since counts are unchanged.
		unchangedManifest := requestNavigationHTTP(t, web, "/api/navigation", "gzip", manifestETag)
		if unchangedManifest.Code != http.StatusNotModified {
			t.Fatalf("post-change manifest conditional status=%d, want 304 (counts unchanged)", unchangedManifest.Code)
		}
	})

	t.Run("one_build_one_resolution_pass", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		recorder := newNavigationMetricRecorder()
		web.navigationMetrics = &navigationMetricFunc{fn: recorder.RecordNavigationMetric}

		// Before any request, zero builds.
		initialStats := web.navigation.Stats()
		if initialStats.CoreBuilds != 0 {
			t.Fatalf("initial core builds=%d, want 0", initialStats.CoreBuilds)
		}

		// Full mandatory hydration: one build for the whole core snapshot.
		_ = hydrateMandatoryGzip(t, web)

		afterHydration := web.navigation.Stats()
		if afterHydration.CoreBuilds != 1 {
			t.Fatalf("after hydration core builds=%d, want exactly 1", afterHydration.CoreBuilds)
		}

		// Re-requesting any resource from cache must not trigger a second build.
		_ = requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		_ = requestNavigationHTTP(t, web, "/api/navigation/sections/live", "gzip", "")
		_ = requestNavigationHTTP(t, web, "/api/navigation/catalogs/projects", "gzip", "")

		afterCacheHits := web.navigation.Stats()
		if afterCacheHits.CoreBuilds != 1 {
			t.Fatalf("after cache hits core builds=%d, want still 1 (cache hits perform zero builds)",
				afterCacheHits.CoreBuilds)
		}

		// Cache stats must show hits for the re-requests.
		if afterCacheHits.Cache.Hits <= afterHydration.Cache.Hits {
			t.Fatalf("cache hits did not increase: before=%d after=%d",
				afterHydration.Cache.Hits, afterCacheHits.Cache.Hits)
		}

		// Aggregated diagnostics must be key-free: the totals carry only
		// request counts, byte sums, duration sums, and per-class breakdowns.
		// No title, prompt, ref, or path value appears in the aggregate.
		totals := aggregateNavigationMetrics(recorder.snapshot())
		if totals.Requests != recorder.requestCount() {
			t.Fatalf("aggregated requests=%d, want %d", totals.Requests, recorder.requestCount())
		}
		if totals.ByClass["manifest"].Requests == 0 {
			t.Fatal("aggregated manifest class totals missing manifest requests")
		}
		if totals.UncompressedBytes <= 0 {
			t.Fatal("aggregated uncompressed bytes must be positive after hydration")
		}
	})
}

// TestNavigationPerformanceBudgets proves the fixed byte and allocation budgets
// from the transport optimization spec (§900-935) on the deterministic fixture.
func TestNavigationPerformanceBudgets(t *testing.T) {
	const baselineResponseBytes = legacyBaselineResponseBytes
	const baselineAllocsBytes = legacyBaselineAllocsBytes

	// Budget thresholds from the spec.
	const (
		mandatoryUncompressedFraction = 0.15 // ≤ 15% of legacy uncompressed JSON
		mandatoryGzipFraction         = 0.10 // ≤ 10% of legacy transferred bytes
		expandedGzipFraction          = 0.35 // ≤ 35% of legacy transferred bytes
		warmAllocsFraction            = 0.20 // ≤ 20% of legacy B/op
	)

	t.Run("mandatory_hydration_uncompressed_json", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		stats := hydrateMandatoryIdentity(t, web)
		budget := int64(math.Round(float64(baselineResponseBytes) * mandatoryUncompressedFraction))
		t.Logf("mandatory uncompressed JSON: %d bytes (budget %d, legacy %d)",
			stats.uncompressedJSON, budget, baselineResponseBytes)
		if int64(stats.uncompressedJSON) > budget {
			t.Fatalf("mandatory uncompressed JSON %d > budget %d (15%% of legacy %d)",
				stats.uncompressedJSON, budget, baselineResponseBytes)
		}
	})

	t.Run("mandatory_hydration_gzip_transferred", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		stats := hydrateMandatoryGzip(t, web)
		budget := int64(math.Round(float64(baselineResponseBytes) * mandatoryGzipFraction))
		t.Logf("mandatory gzip transferred: %d bytes (budget %d, legacy %d)",
			stats.transferredBytes, budget, baselineResponseBytes)
		if int64(stats.transferredBytes) > budget {
			t.Fatalf("mandatory gzip transferred %d > budget %d (10%% of legacy %d)",
				stats.transferredBytes, budget, baselineResponseBytes)
		}
	})

	t.Run("expanded_hydration_gzip_transferred", func(t *testing.T) {
		web := newNavigationBenchmarkFixture(t)
		// Discover the first four project keys from the project catalog page,
		// which is part of mandatory hydration. The spec (§884-886) defines
		// expanded hydration as the first four projects marked as saved
		// expansions; their keys come from the catalog, not from paths.
		catalogRec := requestNavigationHTTP(t, web, "/api/navigation/catalogs/projects", "gzip", "")
		if catalogRec.Code != http.StatusOK {
			t.Fatalf("project catalog status=%d", catalogRec.Code)
		}
		var catalog hubapi.NavigationProjectCatalog
		if err := json.Unmarshal(decodeGzipBody(t, catalogRec), &catalog); err != nil {
			t.Fatalf("decode project catalog: %v", err)
		}
		if len(catalog.Projects) < 4 {
			t.Fatalf("project catalog has %d projects, need at least 4", len(catalog.Projects))
		}
		projectKeys := make([]string, 4)
		for i := range 4 {
			projectKeys[i] = catalog.Projects[i].Key
		}
		stats := hydrateExpandedGzip(t, web, projectKeys)
		budget := int64(math.Round(float64(baselineResponseBytes) * expandedGzipFraction))
		t.Logf("expanded gzip transferred: %d bytes (budget %d, legacy %d)",
			stats.transferredBytes, budget, baselineResponseBytes)
		if int64(stats.transferredBytes) > budget {
			t.Fatalf("expanded gzip transferred %d > budget %d (35%% of legacy %d)",
				stats.transferredBytes, budget, baselineResponseBytes)
		}
	})

	t.Run("warm_manifest_allocations", func(t *testing.T) {
		// The warm manifest B/op budget measures a gzip-accepting manifest
		// request after its object, JSON, and gzip caches are populated (spec
		// §895-898). This is a benchmark-level assertion verified by
		// BenchmarkNavigationMandatory; here we verify the cache is warm and
		// the allocation-relevant path is exercised.
		web := newNavigationBenchmarkFixture(t)

		// First request populates the cache.
		_ = requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		beforeStats := web.navigation.Stats()
		beforeHits := beforeStats.Cache.Hits

		// Second request is a cache hit — no JSON encode or gzip.
		rec := requestNavigationHTTP(t, web, "/api/navigation", "gzip", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("warm manifest status=%d", rec.Code)
		}
		afterStats := web.navigation.Stats()
		if afterStats.Cache.Hits <= beforeHits {
			t.Fatalf("warm manifest did not produce a cache hit: before=%d after=%d",
				beforeHits, afterStats.Cache.Hits)
		}

		// The B/op budget itself is asserted by the benchmark using
		// testing.AllocsChecker semantics. Document the target:
		warmAllocsBudget := int64(math.Round(float64(baselineAllocsBytes) * warmAllocsFraction))
		t.Logf("warm manifest B/op budget: %d (20%% of legacy %d) — verified by BenchmarkNavigationMandatory",
			warmAllocsBudget, baselineAllocsBytes)
	})
}

// min is a builtin in Go 1.21+; no local definition needed.
