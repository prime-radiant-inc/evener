package hubstart

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func TestHubRPCURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{"https becomes wss", "https://hub.example.com", "wss://hub.example.com/rpc"},
		{"http becomes ws", "http://127.0.0.1:8080", "ws://127.0.0.1:8080/rpc"},
		{"other scheme passthrough", "unix:/tmp/sock", "unix:/tmp/sock/rpc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hubRPCURL(HubAddress{BaseURL: tc.base}); got != tc.want {
				t.Fatalf("hubRPCURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

func TestStateHomeForSerfStateDir(t *testing.T) {
	got := StateHomeForSerfStateDir("/home/me/.local/state/serf")
	want := filepath.Dir(filepath.Clean("/home/me/.local/state/serf"))
	if got != want {
		t.Fatalf("StateHomeForSerfStateDir = %q, want %q", got, want)
	}
	// Trailing slash + whitespace are cleaned before taking the parent.
	if got := StateHomeForSerfStateDir("  /a/b/serf/  "); got != "/a/b" {
		t.Fatalf("StateHomeForSerfStateDir(padded) = %q, want /a/b", got)
	}
}

func TestLooksLikeBindFailure(t *testing.T) {
	if looksLikeBindFailure(nil) {
		t.Fatal("nil error should not look like a bind failure")
	}
	if !looksLikeBindFailure(errors.New("listen tcp :8080: bind: permission denied")) {
		t.Fatal("bind error should be detected")
	}
	if !looksLikeBindFailure(errors.New("address already in use")) {
		t.Fatal("address-already-in-use should be detected")
	}
	if looksLikeBindFailure(errors.New("connection refused")) {
		t.Fatal("connection refused is not a bind failure")
	}
}

func TestStartupError_ErrorMessagesPerKind(t *testing.T) {
	tests := []struct {
		kind   StartupErrorKind
		prefix string
	}{
		{StartupErrorMissingHubBinary, "cannot find serf-hub binary: "},
		{StartupErrorBindFailure, "hub failed to bind: "},
		{StartupErrorUnhealthyHub, "hub is unhealthy: "},
		{StartupErrorIncompatibleAPI, "hub API is incompatible: "},
		{StartupErrorStaleEnvironment, "hub state/auth environment is stale: "},
		{StartupErrorRemoteNoAutoStart, "remote hub is not reachable and cannot be auto-started: "},
		{StartupErrorHubUnavailable, "hub is not reachable: "},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			e := StartupError{Kind: tc.kind, Detail: "boom"}
			if got, want := e.Error(), tc.prefix+"boom"; got != want {
				t.Fatalf("Error() = %q, want %q", got, want)
			}
		})
	}
}

func TestStartupError_DetailFallsBackToWrappedErr(t *testing.T) {
	wrapped := errors.New("underlying cause")
	e := StartupError{Kind: StartupErrorUnhealthyHub, Err: wrapped}
	if got := e.Error(); got != "hub is unhealthy: underlying cause" {
		t.Fatalf("Error() = %q, want detail from wrapped err", got)
	}
	if !errors.Is(e, wrapped) {
		t.Fatal("Unwrap should expose the wrapped error")
	}
}

func TestEnvDefault(t *testing.T) {
	env := map[string]string{"SET": "value"}
	getenv := func(k string) string { return env[k] }
	if got := EnvDefault(getenv, "SET", "fallback"); got != "value" {
		t.Fatalf("EnvDefault(set) = %q, want value", got)
	}
	if got := EnvDefault(getenv, "MISSING", "fallback"); got != "fallback" {
		t.Fatalf("EnvDefault(missing) = %q, want fallback", got)
	}
}

// recordingRoundTripper captures the request it sees so the bearer-injection
// behaviour can be asserted without a live server.
type recordingRoundTripper struct {
	seen *http.Request
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.seen = req
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
}

func TestHTTPClientWithBearer(t *testing.T) {
	base := &http.Client{}
	// Empty token returns the original client untouched.
	if got := HTTPClientWithBearer(base, ""); got != base {
		t.Fatal("empty token should return the base client unchanged")
	}

	rec := &recordingRoundTripper{}
	base = &http.Client{Transport: rec}
	client := HTTPClientWithBearer(base, "secret")
	if client == base {
		t.Fatal("non-empty token should return a new client")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://hub.local/health", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("client.Do err = %v", err)
	}
	if got := rec.seen.Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization header = %q, want Bearer secret", got)
	}
	// The original request must not be mutated (RoundTrip clones).
	if req.Header.Get("Authorization") != "" {
		t.Fatal("original request should not carry the bearer header")
	}
}

func TestBearerTransport_EmptyTokenPassesThrough(t *testing.T) {
	rec := &recordingRoundTripper{}
	tr := &bearerTransport{base: rec, token: ""}
	req, _ := http.NewRequest(http.MethodGet, "http://hub.local/x", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip err = %v", err)
	}
	if rec.seen != req {
		t.Fatal("empty-token transport should forward the original request unchanged")
	}
}

func TestClassifyStartHubError(t *testing.T) {
	addr := HubAddress{BaseURL: "http://localhost:9999"}
	var se StartupError
	if !errors.As(classifyStartHubError(addr, errors.New("bind: in use")), &se) || se.Kind != StartupErrorBindFailure {
		t.Fatalf("bind error classified as %q, want bind-failure", se.Kind)
	}
	if !errors.As(classifyStartHubError(addr, errors.New("nope")), &se) || se.Kind != StartupErrorUnhealthyHub {
		t.Fatalf("generic error classified as %q, want unhealthy-hub", se.Kind)
	}
}
