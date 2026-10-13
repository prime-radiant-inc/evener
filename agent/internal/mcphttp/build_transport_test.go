package mcphttp_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/internal/mcphttp"
)

// TestBuildTransport_TypeSelectsTheSDKTransport pins the mapping both the
// connection manager and the reachability probe depend on: a configured "http"
// server is reached over the streamable transport and "sse" over the SSE one,
// each pointed at the configured endpoint.
func TestBuildTransport_TypeSelectsTheSDKTransport(t *testing.T) {
	t.Parallel()
	const endpoint = "https://mcp.example.test/rpc"

	httpTransport, err := mcphttp.BuildTransport("http", endpoint, nil, nil)
	if err != nil {
		t.Fatalf("BuildTransport(http): %v", err)
	}
	streamable, ok := httpTransport.(*mcpsdk.StreamableClientTransport)
	if !ok {
		t.Fatalf("BuildTransport(http) = %T, want *mcpsdk.StreamableClientTransport", httpTransport)
	}
	if streamable.Endpoint != endpoint {
		t.Fatalf("http endpoint = %q, want %q", streamable.Endpoint, endpoint)
	}

	sseTransport, err := mcphttp.BuildTransport("sse", endpoint, nil, nil)
	if err != nil {
		t.Fatalf("BuildTransport(sse): %v", err)
	}
	sse, ok := sseTransport.(*mcpsdk.SSEClientTransport)
	if !ok {
		t.Fatalf("BuildTransport(sse) = %T, want *mcpsdk.SSEClientTransport", sseTransport)
	}
	if sse.Endpoint != endpoint {
		t.Fatalf("sse endpoint = %q, want %q", sse.Endpoint, endpoint)
	}
}

// TestBuildTransport_RefusalsCarryIdentity covers the ways a configured server
// cannot be turned into a transport. The refusals are asserted by identity, not
// by message, per docs/developing-evener/testing.md.
func TestBuildTransport_RefusalsCarryIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"http", "sse"} {
		transport, err := mcphttp.BuildTransport(kind, "", nil, nil)
		if !errors.Is(err, mcphttp.ErrEndpointRequired) {
			t.Errorf("BuildTransport(%q, no url) error = %v, want mcphttp.ErrEndpointRequired", kind, err)
		}
		if transport != nil {
			t.Errorf("BuildTransport(%q, no url) returned a transport, want none", kind)
		}
	}

	transport, err := mcphttp.BuildTransport("websocket", "wss://mcp.example.test", nil, nil)
	if !errors.Is(err, mcphttp.ErrUnsupportedTransport) {
		t.Errorf("BuildTransport(websocket) error = %v, want mcphttp.ErrUnsupportedTransport", err)
	}
	if transport != nil {
		t.Error("BuildTransport(websocket) returned a transport, want none")
	}
}

// TestBuildTransport_ClientAndHeaderWiring pins the client contract each caller
// relies on — the probe's pooled client is used as given, a client supplied
// without headers is used as given, and no client with no headers leaves the
// transport on the SDK's own client instead of installing an empty wrapper — and
// that a configured header reaches the wire. Header scoping to the endpoint's
// origin is ClientWithHeaders' own contract, pinned in mcphttp_test.go.
func TestBuildTransport_ClientAndHeaderWiring(t *testing.T) {
	t.Parallel()
	configured := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		configured <- r.Header.Get("X-Test")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	headers := map[string]string{"X-Test": "configured"}
	for _, kind := range []string{"http", "sse"} {
		transport, err := mcphttp.BuildTransport(kind, server.URL, headers, nil)
		if err != nil {
			t.Fatalf("BuildTransport(%q, headers): %v", kind, err)
		}
		client := transportClient(t, transport)
		if client == nil {
			t.Fatalf("BuildTransport(%q, headers) left the transport on the SDK's client, want one carrying the headers", kind)
		}
		get(t, client, server.URL)
		if got := <-configured; got != "configured" {
			t.Fatalf("BuildTransport(%q) header on the wire = %q, want %q", kind, got, "configured")
		}
	}

	probeClient := &http.Client{}
	withoutHeaders, err := mcphttp.BuildTransport("sse", server.URL, nil, probeClient)
	if err != nil {
		t.Fatalf("BuildTransport(sse, no headers): %v", err)
	}
	if got := transportClient(t, withoutHeaders); got != probeClient {
		t.Fatal("a caller's client must be used as given when the server configures no headers")
	}

	noClient, err := mcphttp.BuildTransport("http", server.URL, nil, nil)
	if err != nil {
		t.Fatalf("BuildTransport(http, no client, no headers): %v", err)
	}
	if got := transportClient(t, noClient); got != nil {
		t.Fatal("no client and no headers must leave the transport on the SDK's own client")
	}

	if _, err := mcphttp.BuildTransport("websocket", server.URL, headers, nil); !errors.Is(err, mcphttp.ErrUnsupportedTransport) {
		t.Errorf("BuildTransport(websocket, headers) error = %v, want mcphttp.ErrUnsupportedTransport", err)
	}
}

// transportClient returns the HTTP client a built transport sends through, or nil
// when the transport left the choice to the SDK.
func transportClient(t *testing.T, transport mcpsdk.Transport) *http.Client {
	t.Helper()
	switch built := transport.(type) {
	case *mcpsdk.SSEClientTransport:
		return built.HTTPClient
	case *mcpsdk.StreamableClientTransport:
		return built.HTTPClient
	default:
		t.Fatalf("unexpected transport type %T", transport)
		return nil
	}
}

// get performs one request through client and fails on an error.
func get(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = resp.Body.Close()
}
