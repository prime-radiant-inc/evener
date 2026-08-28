package hub

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// mutRoute is one mutating HTTP route the B2 fuzz drives. {id} in the template
// is replaced with the fuzzed id token (path- or query-escaped depending on
// where it sits); useBody attaches the fuzzed body bytes as the request body.
type mutRoute struct {
	method   string
	template string
	useBody  bool
}

// mutatingRoutes are exactly the routes the Phase-4 read-only handler fuzz
// excluded: append-only tombstones for retired routes, the
// live-git seam reads, and the live steer/queue/drain-as-steer actions under
// /s. Retired slots stay in place so route-indexed fuzz corpora remain
// compatible; they now exercise ordinary unknown-route handling. Under the B0
// sandbox, the live routes are contained: git-head uses its seam, and action
// verbs resolve "not live" before any daemon dial.
var mutatingRoutes = []mutRoute{
	{http.MethodPost, "/api/spawn", true},
	{http.MethodGet, "/api/models?harness={id}", false},
	// Unique unknown-route tombstones retain the removed slots without
	// inventorying the retired routes.
	{http.MethodPost, "/__retired__/session-rest/{id}/clear", false},
	{http.MethodPost, "/__retired__/session-rest/{id}/model", true},
	{http.MethodPost, "/__retired__/session-rest/{id}/reasoning-effort", true},
	{http.MethodPost, "/__retired__/session-rest/{id}/interrupt", false},
	{http.MethodPost, "/__retired__/session-rest/{id}/compact", false},
	{http.MethodPost, "/__retired__/session-rest/{id}/shutdown", false},
	{http.MethodPost, "/__retired__/session-rest/{id}/send", true},
	{http.MethodPost, "/__retired__/session-rest/{id}/fork", true},
	// Active form-action routes.
	{http.MethodPost, "/s/{id}/steer", true},
	{http.MethodPost, "/s/{id}/queue", true},
	{http.MethodPost, "/s/{id}/drain-as-steer", true},
	{http.MethodPost, "/s/{id}/promote-queued", true},
}

// buildMutatingTarget substitutes the fuzzed id into a route template, escaping
// it for its position (query value vs path segment).
func buildMutatingTarget(template, id string) string {
	if strings.Contains(template, "={id}") {
		return strings.ReplaceAll(template, "{id}", url.QueryEscape(id))
	}
	return strings.ReplaceAll(template, "{id}", url.PathEscape(id))
}

// isServerFault reports whether an HTTP status is a server fault we treat as a
// defect. 500 (Internal Server Error) and any 5xx other than the two the hub
// emits BY DESIGN — 502 Bad Gateway (an upstream/daemon error mapped through
// writeAPIWireError) and 503 Service Unavailable (a deliberately-mapped
// appwire.Unavailable, e.g. an unknown model harness) — are faults. The
// deliberate 502/503 mappings are correct responses to bad input, not crashes.
func isServerFault(code int) bool {
	return code >= 500 && code != http.StatusBadGateway && code != http.StatusServiceUnavailable
}

// FuzzWebMutatingHandler fuzzes the hub's mutating HTTP routes under the B0
// sandbox. It is the write-path counterpart to FuzzWebHandler (read-only GETs).
//
// Oracles, per fuzzed (routeIndex, id, body):
//   - never panic (an unrecovered panic crashes the worker → a reported crasher);
//   - no server fault (isServerFault: a 500 or non-deliberate 5xx is a defect);
//   - never serve the out-of-root secret (path-escape tripwire);
//   - never dial the network (the deny-transport tripwire) — no launch path
//     reaches a real subprocess, and no model/git call reaches a provider.
//
// The recording spawner and recording mkdir guarantee no real process or
// directory is ever materialized regardless of input, so those escapes need no
// separate per-iteration assertion.
func FuzzWebMutatingHandler(f *testing.F) {
	deny := installDenyTransportTB(f)

	s := newSandbox(f)
	handler := s.Web.Handler()

	idx := func(template string) uint8 {
		for i, r := range mutatingRoutes {
			if r.template == template {
				return uint8(i)
			}
		}
		return 0
	}
	seeds := []struct {
		template string
		id       string
		body     string
	}{
		{"/api/spawn", "", `{"harness":"evener","working_dir":"` + s.CWD + `","model":"openai/gpt-5.5"}`},
		// Keep the retired route's optional-payload seed in its original position;
		// the route-indexed corpus remains append-only after the route removal.
		{"/api/spawn", "", `{"harness":"evener","working_dir":"` + s.CWD + `","model":"openai/gpt-5.5","prompt":"hi","agent":"engineer","reasoning_effort":"high","non_interactive":true,"launch_overrides":{"maxRounds":3},"items":[{"type":"input_image","mediaType":"image/png","data":"aGVsbG8="}]}`},
		// Keep the retired route's image-limit seed in its original position too;
		// changing route-indexed inputs would invalidate existing corpus entries.
		{"/api/spawn", "", `{"harness":"evener","working_dir":"` + s.CWD + `","items":[{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"},{"type":"image"}]}`},
		{"/api/models?harness={id}", "evener", ""},
		{"/api/models?harness={id}", "codex", ""},
		// Tombstone seeds retain their route indices for existing corpora.
		{"/__retired__/session-rest/{id}/clear", sandboxSessionID, ""},
		{"/__retired__/session-rest/{id}/model", sandboxSessionID, `{"model":"openai/gpt-5.5"}`},
		{"/__retired__/session-rest/{id}/reasoning-effort", sandboxSessionID, `{"reasoning_effort":"high"}`},
		{"/__retired__/session-rest/{id}/interrupt", sandboxSessionID, ""},
		{"/__retired__/session-rest/{id}/compact", sandboxSessionID, ""},
		{"/__retired__/session-rest/{id}/shutdown", sandboxSessionID, ""},
		{"/__retired__/session-rest/{id}/send", sandboxSessionID, `{"text":"hi"}`},
		{"/__retired__/session-rest/{id}/fork", sandboxSessionID, `{"sourceTurnId":"turn_1","editedInput":"x"}`},
		{"/s/{id}/steer", sandboxSessionID, `{"text":"go"}`},
		{"/s/{id}/queue", sandboxSessionID, `{"text":"later"}`},
		{"/s/{id}/drain-as-steer", sandboxSessionID, `{}`},
		{"/s/{id}/promote-queued", sandboxSessionID, `{"index":0}`},
	}
	for _, seed := range seeds {
		f.Add(idx(seed.template), seed.id, []byte(seed.body))
	}

	f.Fuzz(func(t *testing.T, routeIdx uint8, id string, body []byte) {
		route := mutatingRoutes[int(routeIdx)%len(mutatingRoutes)]
		target := buildMutatingTarget(route.template, id)

		var rdr io.Reader
		if route.useBody {
			rdr = bytes.NewReader(body)
		}
		req := httptest.NewRequest(route.method, target, rdr)
		req.Host = "127.0.0.1:9180"
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req) // Oracle 1: never panics.

		// Oracle 2: no server fault.
		if isServerFault(rec.Code) {
			t.Fatalf("server fault from %s %s: status=%d body=%s", route.method, target, rec.Code, truncateForLog(rec.Body.String()))
		}
		// Oracle 3: never serve the out-of-root secret.
		if bytes.Contains(rec.Body.Bytes(), s.Secret) {
			t.Fatalf("path escape: %s %s served the out-of-root secret (status=%d)", route.method, target, rec.Code)
		}
		// Oracle 4: no route may have dialed the network.
		if att := deny.Attempts(); len(att) != 0 {
			t.Fatalf("network attempt during %s %s: %v", route.method, target, att)
		}
	})
}
