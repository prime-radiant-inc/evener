package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// serializeToolDefsSize measures a definition list the way the estimator
// bills it, independently of the cache under test.
func serializeToolDefsSize(t *testing.T, defs any) int {
	t.Helper()
	b, err := json.Marshal(defs)
	if err != nil {
		t.Fatalf("marshal tool defs: %v", err)
	}
	return len(b)
}

// TestCachedToolDefsJSONBytesTracksTheCachedList pins the cache's one
// invariant: the byte count a request stamps is exactly the serialized length
// of the tool list beside it. A rebuild that changes the list without
// re-measuring it would price every later round from a stale length, which
// silently changes the estimate rather than merely serving a stale cache
// (#6406).
func TestCachedToolDefsJSONBytesTracksTheCachedList(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	if len(s.cachedToolDefs) == 0 {
		t.Fatal("fixture session advertised no tools")
	}
	if got, want := s.cachedToolDefsJSONBytes, serializeToolDefsSize(t, s.cachedToolDefs); got != want {
		t.Fatalf("cachedToolDefsJSONBytes = %d, want %d", got, want)
	}
	before := s.cachedToolDefsJSONBytes

	// registerTool is the lifecycle path that grows the advertised list at
	// runtime; it rebuilds the cache, so the byte count must move with it.
	s.registerTool("byte_probe_tool", "test-only: measures cache invalidation", map[string]any{"type": "object"},
		func(context.Context, any) (any, error) { return "done", nil })
	s.mu.Lock()
	defs, bytes := s.cachedToolDefs, s.cachedToolDefsJSONBytes
	s.mu.Unlock()
	if got, want := bytes, serializeToolDefsSize(t, defs); got != want {
		t.Fatalf("after registering a tool cachedToolDefsJSONBytes = %d, want %d", got, want)
	}
	if bytes <= before {
		t.Fatalf("after registering a tool cachedToolDefsJSONBytes = %d, want more than %d", bytes, before)
	}

	// A request built from that snapshot carries exactly the cached length.
	built := s.buildModelRequest(s.currentProfile(), "sys", nil, defs, "", bytes)
	if built.ToolsJSONBytes != bytes {
		t.Fatalf("built request ToolsJSONBytes = %d, want the cached %d", built.ToolsJSONBytes, bytes)
	}
	if len(built.Tools) == 0 {
		t.Fatal("built request carried no tools")
	}
}

// TestPreparedRoundRequestCarriesTheToolByteStamp covers what the builder test
// above cannot: that the real per-round path threads the session's cached
// length into the request it prepares. Without this, dropping the snapshot in
// prepareModelRequestWithError would silently restore the per-round marshal
// with every other test still green — the estimate stays correct, only its
// cost returns (#6406).
func TestPreparedRoundRequestCarriesTheToolByteStamp(t *testing.T) {
	t.Parallel()
	s := newSession(t)
	if s.cachedToolDefsJSONBytes <= 0 {
		t.Fatalf("fixture session cached no tool byte length")
	}
	var timings events.RoundTimings
	_, _, _, req, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if len(req.Tools) == 0 {
		t.Fatal("prepared request carried no tools")
	}
	if got, want := req.ToolsJSONBytes, serializeToolDefsSize(t, req.Tools); got != want {
		t.Fatalf("prepared request ToolsJSONBytes = %d, want the serialized list's %d", got, want)
	}
	// The stamp must price the same request the marshal would: compare against
	// the same request stripped of its stamp.
	unstamped := req
	unstamped.ToolsJSONBytes = 0
	if got, want := llm.EstimateInputTokensForResolved(s.currentProfile().Resolved(), req).Tokens,
		llm.EstimateInputTokensForResolved(s.currentProfile().Resolved(), unstamped).Tokens; got != want {
		t.Fatalf("stamped estimate = %d, want the unstamped %d", got, want)
	}
}
