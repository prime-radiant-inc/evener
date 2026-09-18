package mcphttp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/agent/internal/mcphttp"
)

// recordingRoundTripper records the request it was handed so a test can assert
// on injected headers without a real network round trip.
type recordingRoundTripper struct {
	got *http.Request
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}}, nil
}

// ClientWithHeaders(nil, ...) must return a usable client whose transport
// injects the configured headers into requests to the configured server.
func TestClientWithHeaders_NilBase_InjectsHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := mcphttp.ClientWithHeaders(nil, srv.URL, map[string]string{
		"Authorization": "Bearer tok",
		"X-Custom":      "val",
	})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization header = %q, want injected value", gotAuth)
	}
	if gotCustom != "val" {
		t.Errorf("X-Custom header = %q, want injected value", gotCustom)
	}
}

// A non-nil base must be copied, not mutated: its own transport is wrapped and
// its other fields survive onto the returned client.
func TestClientWithHeaders_BaseCopiedNotMutated(t *testing.T) {
	rec := &recordingRoundTripper{}
	base := &http.Client{
		Transport: rec,
	}
	baseTransport := base.Transport

	client := mcphttp.ClientWithHeaders(base, "https://example.invalid/", map[string]string{"X-Injected": "yes"})
	if client == base {
		t.Fatal("ClientWithHeaders returned the base client, want a copy")
	}
	if base.Transport != baseTransport {
		t.Fatal("ClientWithHeaders mutated the base client's transport")
	}
	if client.Transport == baseTransport {
		t.Fatal("returned client still carries the base transport, want it wrapped")
	}

	req, err := http.NewRequest(http.MethodGet, "https://example.invalid/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := client.Transport.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if rec.got == nil || rec.got.Header.Get("X-Injected") != "yes" {
		t.Errorf("base transport request = %#v, want injected X-Injected header", rec.got)
	}
	if got := req.Header.Get("X-Injected"); got != "" {
		t.Errorf("original request was mutated: X-Injected = %q", got)
	}
}

// RoundTrip must not mutate the caller-owned request; the base transport must
// receive a distinct clone carrying the injected headers.
func TestHeaderRoundTripper_DoesNotMutateRequest(t *testing.T) {
	rec := &recordingRoundTripper{}
	rt := &mcphttp.HeaderRoundTripper{Base: rec, Origin: "https://example.invalid:443", Headers: map[string]string{"X-Injected": "yes"}}

	req, err := http.NewRequest(http.MethodGet, "https://example.invalid/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("round trip: %v", err)
	}

	if got := req.Header.Get("X-Injected"); got != "" {
		t.Errorf("original request header = %q, want empty (request must not be mutated)", got)
	}
	if rec.got == nil {
		t.Fatal("base transport received no request")
	}
	if rec.got == req {
		t.Error("base transport received the original request, want a clone")
	}
	if got := rec.got.Header.Get("X-Injected"); got != "yes" {
		t.Errorf("base transport request header = %q, want %q", got, "yes")
	}
}

// closeTrackingRoundTripper records whether CloseIdleConnections reached it.
type closeTrackingRoundTripper struct {
	recordingRoundTripper
	closed bool
}

func (c *closeTrackingRoundTripper) CloseIdleConnections() { c.closed = true }

// CloseIdleConnections on the returned client must reach a wrapped transport
// that supports it, and must be a harmless no-op when it does not.
func TestClientWithHeaders_ForwardsCloseIdleConnections(t *testing.T) {
	rec := &closeTrackingRoundTripper{}
	client := mcphttp.ClientWithHeaders(&http.Client{Transport: rec}, "https://mcp.example/mcp", map[string]string{"X": "y"})
	client.CloseIdleConnections()
	if !rec.closed {
		t.Error("CloseIdleConnections did not reach the wrapped transport")
	}

	plain := mcphttp.ClientWithHeaders(&http.Client{Transport: &recordingRoundTripper{}}, "https://mcp.example/mcp", map[string]string{"X": "y"})
	plain.CloseIdleConnections() // must not panic when the base lacks the method
}

// ClientWithHeaders must scope injection to the configured endpoint's
// normalized origin. Otherwise a cross-origin redirect, which http.Client sends
// without sensitive headers, would have its credentials re-added by the
// transport and leaked to the redirect target.
func TestClientWithHeaders_OriginScopedInjection(t *testing.T) {
	client := mcphttp.ClientWithHeaders(nil, "https://mcp.example/mcp", map[string]string{"Authorization": "Bearer secret"})
	rt, ok := client.Transport.(*mcphttp.HeaderRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want *mcphttp.HeaderRoundTripper", client.Transport)
	}
	if rt.Origin != "https://mcp.example:443" {
		t.Fatalf("scoped origin = %q, want %q", rt.Origin, "https://mcp.example:443")
	}

	rec := &recordingRoundTripper{}
	rt.Base = rec

	same, err := http.NewRequest(http.MethodGet, "https://mcp.example/mcp", nil)
	if err != nil {
		t.Fatalf("new same-host request: %v", err)
	}
	if _, err := rt.RoundTrip(same); err != nil {
		t.Fatalf("same-host round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("same-host Authorization = %q, want injected value", got)
	}

	rec.got = nil
	other, err := http.NewRequest(http.MethodGet, "https://evil.example/mcp", nil)
	if err != nil {
		t.Fatalf("new cross-host request: %v", err)
	}
	if _, err := rt.RoundTrip(other); err != nil {
		t.Fatalf("cross-host round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "" {
		t.Errorf("cross-host Authorization = %q, want none (must not leak credentials)", got)
	}

	// Same host, different letter case: DNS hostnames are case-insensitive, so
	// the configured headers must still be injected.
	rec.got = nil
	mixedCase, err := http.NewRequest(http.MethodGet, "https://MCP.Example/mcp", nil)
	if err != nil {
		t.Fatalf("new mixed-case request: %v", err)
	}
	if _, err := rt.RoundTrip(mixedCase); err != nil {
		t.Fatalf("mixed-case round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("mixed-case same-host Authorization = %q, want injected value", got)
	}
}

// End-to-end: a redirect to the same host on a different port is a different
// origin, so the transport must not inject Authorization there. The clone in
// RoundTrip is what makes this hold end-to-end: http.Client copies headers from
// the original (un-injected) request on a same-host redirect, and the transport
// declines to re-add them for the foreign origin.
func TestClientWithHeaders_SameHostDifferentPortRedirectDropsHeaders(t *testing.T) {
	var destAuth string
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/next", http.StatusFound)
	}))
	defer src.Close()

	client := mcphttp.ClientWithHeaders(nil, src.URL, map[string]string{"Authorization": "Bearer secret"})
	resp, err := client.Get(src.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if destAuth != "" {
		t.Errorf("same-host different-port redirect received Authorization = %q, want none", destAuth)
	}
}

// End-to-end: a redirect within the same origin keeps the configured headers,
// so scoping does not drop credentials on ordinary in-origin path redirects.
func TestClientWithHeaders_SameOriginRedirectKeepsHeaders(t *testing.T) {
	var finalAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		finalAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := mcphttp.ClientWithHeaders(nil, srv.URL, map[string]string{"Authorization": "Bearer secret"})
	resp, err := client.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if finalAuth != "Bearer secret" {
		t.Errorf("same-origin redirect Authorization = %q, want injected value", finalAuth)
	}
}

// IPv6 origins must be normalized with net.JoinHostPort so a bare host and a
// bracketed host:port cannot collide. A distinct IPv6 origin must not receive
// the configured credentials.
func TestClientWithHeaders_IPv6OriginsDistinct(t *testing.T) {
	client := mcphttp.ClientWithHeaders(nil, "https://[::1]/mcp", map[string]string{"Authorization": "Bearer secret"})
	rt, ok := client.Transport.(*mcphttp.HeaderRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want *mcphttp.HeaderRoundTripper", client.Transport)
	}
	if rt.Origin != "https://[::1]:443" {
		t.Fatalf("IPv6 origin = %q, want %q", rt.Origin, "https://[::1]:443")
	}

	rec := &recordingRoundTripper{}
	rt.Base = rec

	same, err := http.NewRequest(http.MethodGet, "https://[::1]:443/mcp", nil)
	if err != nil {
		t.Fatalf("new same-origin request: %v", err)
	}
	if _, err := rt.RoundTrip(same); err != nil {
		t.Fatalf("same-origin round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("same IPv6 origin Authorization = %q, want injected value", got)
	}

	rec.got = nil
	other, err := http.NewRequest(http.MethodGet, "https://[::1:1]/mcp", nil)
	if err != nil {
		t.Fatalf("new distinct-origin request: %v", err)
	}
	if _, err := rt.RoundTrip(other); err != nil {
		t.Fatalf("distinct-origin round trip: %v", err)
	}
	if got := rec.got.Header.Get("Authorization"); got != "" {
		t.Errorf("distinct IPv6 origin Authorization = %q, want none", got)
	}
}

// An unparseable or hostless endpoint must fail closed: the returned client
// injects nothing rather than leaking credentials to every host.
func TestClientWithHeaders_HostlessEndpointFailsClosed(t *testing.T) {
	for _, endpoint := range []string{"/relative/mcp", "://bad-url", ""} {
		client := mcphttp.ClientWithHeaders(nil, endpoint, map[string]string{"Authorization": "Bearer secret"})
		rt, ok := client.Transport.(*mcphttp.HeaderRoundTripper)
		if !ok {
			// Fail-closed is implemented by leaving the base transport in place.
			continue
		}
		rec := &recordingRoundTripper{}
		rt.Base = rec
		req, err := http.NewRequest(http.MethodGet, "https://mcp.example/mcp", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("round trip: %v", err)
		}
		if got := rec.got.Header.Get("Authorization"); got != "" {
			t.Errorf("endpoint %q: Authorization = %q, want none (must fail closed)", endpoint, got)
		}
	}
}
