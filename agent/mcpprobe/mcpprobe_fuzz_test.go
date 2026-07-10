package mcpprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// FuzzProbe exercises the real concurrent Probe/probeOne flow while replacing
// only its two external boundaries. The scripted lookup makes generated stdio
// configs independent of PATH, and the RoundTripper rejects every HTTP request
// without allowing a network connection.
//
// Oracles:
//   - results retain the input config order, even though probes run concurrently;
//   - one failed config does not prevent other rows from being returned;
//   - stdio lookup and HTTP/SSE transport calls hit the supplied per-call seams;
//   - configured headers reach the transport for both HTTP and SSE probes.
func FuzzProbe(f *testing.F) {
	f.Add("present", "missing", "http", "sse", "token")
	f.Add("", "", "", "", "")
	f.Add("spaces and symbols !@#", "missing", "http-row", "sse-row", "Bearer fuzz-token")

	f.Fuzz(func(t *testing.T, availableName, missingName, httpName, sseName, headerValue string) {
		const (
			availableCommand = "mcpprobe-fuzz-present"
			missingCommand   = "mcpprobe-fuzz-missing"
		)
		headerValue = probeFuzzHeaderValue(headerValue)

		observed := &probeFuzzObserver{}
		deps := probeDeps{
			lookupPath: func(command string) (string, error) {
				observed.recordLookup(command)
				if command == availableCommand {
					return "/virtual/bin/" + command, nil
				}
				return "", fmt.Errorf("command %q is absent", command)
			},
			httpClient: &http.Client{Transport: &probeFuzzRoundTripper{
				observed: observed,
				handler:  probeFuzzMCPHandler(),
			}},
		}

		configs := []mcpconfig.ServerConfig{
			{Name: availableName, Type: "stdio", Command: availableCommand},
			{
				Name:    httpName,
				Type:    "http",
				URL:     "http://mcpprobe.invalid/http",
				Headers: map[string]string{"X-Serf-Probe": headerValue},
			},
			{Name: missingName, Command: missingCommand}, // empty Type defaults to stdio
			{
				Name:    sseName,
				Type:    "sse",
				URL:     "http://mcpprobe.invalid/sse",
				Headers: map[string]string{"X-Serf-Probe": headerValue},
			},
			{Name: "unknown", Type: "unsupported"},
			{Name: "missing-sse-url", Type: "sse"},
			{Name: "missing-http-url", Type: "http"},
		}

		results := probeWithDeps(context.Background(), configs, deps)
		wantTransports := []string{"stdio", "http", "stdio", "sse", "unsupported", "sse", "http"}
		wantStatuses := []string{"available", "available", "missing", "unreachable", "unreachable", "unreachable", "unreachable"}
		if len(results) != len(configs) {
			t.Fatalf("len(results) = %d, want %d", len(results), len(configs))
		}
		for i, result := range results {
			if result.Name != configs[i].Name {
				t.Errorf("results[%d].Name = %q, want input config name %q", i, result.Name, configs[i].Name)
			}
			if result.Transport != wantTransports[i] {
				t.Errorf("results[%d].Transport = %q, want %q", i, result.Transport, wantTransports[i])
			}
			if result.Status != wantStatuses[i] {
				t.Errorf("results[%d].Status = %q, want %q (Error=%q)", i, result.Status, wantStatuses[i], result.Error)
			}
			if result.Status == "available" && result.Error != "" {
				t.Errorf("results[%d].Error = %q for available result", i, result.Error)
			}
			if result.Status != "available" && result.Error == "" {
				t.Errorf("results[%d].Error is empty for failed result", i)
			}
		}

		lookups, requests := observed.snapshot()
		sort.Strings(lookups)
		wantLookups := []string{availableCommand, missingCommand}
		sort.Strings(wantLookups)
		if strings.Join(lookups, "\x00") != strings.Join(wantLookups, "\x00") {
			t.Errorf("lookup calls = %q, want %q", lookups, wantLookups)
		}

		seenPaths := map[string]bool{}
		for _, request := range requests {
			seenPaths[request.path] = true
			values, ok := request.headers["X-Serf-Probe"]
			if !ok || len(values) != 1 || values[0] != headerValue {
				t.Errorf("request %s %s header X-Serf-Probe = %q, want [%q]", request.method, request.path, values, headerValue)
			}
		}
		if !seenPaths["/http"] || !seenPaths["/sse"] {
			t.Errorf("fake transport paths = %v, want both /http and /sse", seenPaths)
		}

		// The public wrapper is also safe to exercise with an unsupported type:
		// it never invokes either external boundary.
		publicResults := Probe(context.Background(), []mcpconfig.ServerConfig{{Name: "public", Type: "unsupported"}})
		if len(publicResults) != 1 || publicResults[0].Status != "unreachable" || publicResults[0].Error == "" {
			t.Errorf("Probe unsupported result = %+v, want one unreachable row with an error", publicResults)
		}
		assertProbeFuzzTransportConstruction(t, headerValue)
	})
}

func assertProbeFuzzTransportConstruction(t *testing.T, headerValue string) {
	t.Helper()
	cfg := mcpconfig.ServerConfig{
		Type:    "http",
		URL:     "http://mcpprobe.invalid/direct",
		Headers: map[string]string{"X-Serf-Probe": headerValue},
	}
	for _, deps := range []probeDeps{{}, {httpClient: &http.Client{}}} {
		transport, err := transportForProbe(cfg, deps)
		if err != nil {
			t.Fatalf("transportForProbe(%+v) error: %v", deps, err)
		}
		httpTransport, ok := transport.(*mcpsdk.StreamableClientTransport)
		if !ok || httpTransport.HTTPClient == nil || httpTransport.HTTPClient.Transport == nil {
			t.Fatalf("transportForProbe(%+v) = %#v, want streamable transport with a header client", deps, transport)
		}
	}
	if _, err := transportForProbe(mcpconfig.ServerConfig{Type: "unsupported"}, probeDeps{}); err == nil {
		t.Error("transportForProbe accepted an unsupported transport type")
	}
}

type probeFuzzObserver struct {
	mu       sync.Mutex
	lookups  []string
	requests []probeFuzzRequest
}

type probeFuzzRequest struct {
	method  string
	path    string
	headers http.Header
}

func (o *probeFuzzObserver) recordLookup(command string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lookups = append(o.lookups, command)
}

func (o *probeFuzzObserver) recordRequest(req *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, probeFuzzRequest{
		method:  req.Method,
		path:    req.URL.Path,
		headers: req.Header.Clone(),
	})
}

func (o *probeFuzzObserver) snapshot() ([]string, []probeFuzzRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.lookups...), append([]probeFuzzRequest(nil), o.requests...)
}

type probeFuzzRoundTripper struct {
	observed *probeFuzzObserver
	handler  http.Handler
}

func (t *probeFuzzRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.observed.recordRequest(req)
	if req.URL.Path == "/http" {
		if req.Method == http.MethodGet {
			// StreamableClientTransport opens a standalone SSE stream after a
			// successful initialize. The SDK explicitly accepts 405 as a server
			// that does not offer that optional stream; returning it here keeps
			// the in-process RoundTripper synchronous and bounded.
			return probeFuzzResponse(req, http.StatusMethodNotAllowed, "standalone SSE unavailable"), nil
		}
		recorder := httptest.NewRecorder()
		t.handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Request = req
		return response, nil
	}
	return probeFuzzResponse(req, http.StatusBadRequest, "mcpprobe fuzz transport rejection"), nil
}

func probeFuzzResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func probeFuzzMCPHandler() http.Handler {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "probe-fuzz-server", Version: "v1"}, nil)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return server
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
}

func probeFuzzHeaderValue(value string) string {
	if len(value) > 32 {
		value = value[:32]
	}
	return fmt.Sprintf("%x", value)
}

var _ http.RoundTripper = (*probeFuzzRoundTripper)(nil)
