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
// injects the configured headers into every request.
func TestClientWithHeaders_NilBase_InjectsHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := mcphttp.ClientWithHeaders(nil, map[string]string{
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
	base := &http.Client{
		Transport: &recordingRoundTripper{},
	}
	baseTransport := base.Transport

	client := mcphttp.ClientWithHeaders(base, map[string]string{"X-Injected": "yes"})
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
	if got := req.Header.Get("X-Injected"); got != "yes" {
		t.Errorf("X-Injected header = %q, want %q", got, "yes")
	}
}
