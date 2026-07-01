package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// httpClientWithHeaders must return a client whose RoundTripper injects the
// configured headers into every request.
func TestCov_HTTPClientWithHeaders_InjectsHeaders(t *testing.T) {
	var gotAuth, gotCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := httpClientWithHeaders(map[string]string{
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

// A sse/http config carrying headers wires the header-injecting client onto the
// transport (the len(cfg.Headers)>0 branch of transportForConfig).
func TestCov_TransportForConfig_WithHeaders(t *testing.T) {
	sse, err := transportForConfig(mcpconfig.ServerConfig{
		Type: "sse", URL: "http://localhost:9", Headers: map[string]string{"Authorization": "x"}})
	if err != nil {
		t.Fatalf("sse with headers: %v", err)
	}
	st, ok := sse.(*mcpsdk.SSEClientTransport)
	if !ok || st.HTTPClient == nil {
		t.Errorf("sse transport should carry a header-injecting HTTPClient, got %T", sse)
	}

	httpT, err := transportForConfig(mcpconfig.ServerConfig{
		Type: "http", URL: "http://localhost:9", Headers: map[string]string{"Authorization": "x"}})
	if err != nil {
		t.Fatalf("http with headers: %v", err)
	}
	ht, ok := httpT.(*mcpsdk.StreamableClientTransport)
	if !ok || ht.HTTPClient == nil {
		t.Errorf("http transport should carry a header-injecting HTTPClient, got %T", httpT)
	}
}
