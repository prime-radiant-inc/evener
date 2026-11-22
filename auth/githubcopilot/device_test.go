package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// swapNow replaces the package clock for one test and restores it after.
// Task 2 tests never run in parallel, so one shared fake clock is safe.
func swapNow(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	current := at
	previous := nowFunc
	nowFunc = func() time.Time { return current }
	t.Cleanup(func() { nowFunc = previous })
	return &current
}

func newTestConfig(server *httptest.Server) Config {
	cfg := DefaultConfig()
	cfg.GitHubBaseURL = server.URL
	cfg.APIBaseURL = server.URL
	cfg.HTTPTimeout = 2 * time.Second
	return cfg
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func rawResponseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// tokenEndpointScript serves GitHub's device and refresh token endpoint. Tests
// queue error codes and HTTP statuses; the queue order is the request order.
type tokenEndpointScript struct {
	mu       sync.Mutex
	codes    []string
	statuses []int
	tokens   map[string]any
	calls    int
	forms    []url.Values
}

func (s *tokenEndpointScript) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/login/oauth/access_token" {
			t.Errorf("request = %s %s, want POST /login/oauth/access_token", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		s.mu.Lock()
		s.calls++
		s.forms = append(s.forms, r.PostForm)
		code := ""
		if len(s.codes) > 0 {
			code = s.codes[0]
			s.codes = s.codes[1:]
		}
		status := 0
		if len(s.statuses) > 0 {
			status = s.statuses[0]
			s.statuses = s.statuses[1:]
		}
		tokens := s.tokens
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			writeJSON(w, map[string]string{"error": "server_error"})
			return
		}
		if code != "" {
			writeJSON(w, map[string]string{"error": code})
			return
		}
		if tokens == nil {
			tokens = tokenResponse("access-1", "refresh-1")
		}
		writeJSON(w, tokens)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *tokenEndpointScript) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *tokenEndpointScript) lastForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.forms) == 0 {
		return nil
	}
	return s.forms[len(s.forms)-1]
}

func tokenResponse(accessToken, refreshToken string) map[string]any {
	return map[string]any{
		"access_token":             accessToken,
		"refresh_token":            refreshToken,
		"token_type":               "bearer",
		"scope":                    "read:user offline_access",
		"expires_in":               28800,
		"refresh_token_expires_in": 15897600,
	}
}

func testDeviceCode(at time.Time) DeviceCode {
	return DeviceCode{
		DeviceCode:      "device-code-1",
		UserCode:        "ABCD-1234",
		VerificationURL: "https://github.com/login/device",
		Interval:        5 * time.Second,
		Expiry:          at.Add(15 * time.Minute),
	}
}

func TestRequestDeviceCodeReturnsDisplayFields(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	var mu sync.Mutex
	var form url.Values
	var accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/login/device/code" {
			t.Errorf("request = %s %s, want POST /login/device/code", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		mu.Lock()
		accept = r.Header.Get("Accept")
		form = r.PostForm
		mu.Unlock()
		writeJSON(w, map[string]any{
			"device_code":      "device-code-1",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://github.com/login/device",
			"expires_in":       900,
			"interval":         5,
		})
	}))
	t.Cleanup(server.Close)

	dc, err := RequestDeviceCode(context.Background(), server.Client(), newTestConfig(server))
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if dc.DeviceCode != "device-code-1" || dc.UserCode != "ABCD-1234" {
		t.Fatalf("device = %q, user = %q; want device-code-1 and ABCD-1234", dc.DeviceCode, dc.UserCode)
	}
	if dc.VerificationURL != "https://github.com/login/device" {
		t.Fatalf("verification URL = %q, want https://github.com/login/device", dc.VerificationURL)
	}
	if dc.Interval != 5*time.Second {
		t.Fatalf("interval = %v, want 5s", dc.Interval)
	}
	if !dc.Expiry.Equal(at.Add(15 * time.Minute)) {
		t.Fatalf("expiry = %v, want %v", dc.Expiry, at.Add(15*time.Minute))
	}
	mu.Lock()
	defer mu.Unlock()
	if accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", accept)
	}
	if form.Get("client_id") != ClientID {
		t.Errorf("client_id = %q, want %q", form.Get("client_id"), ClientID)
	}
	if form.Get("scope") != "read:user offline_access" {
		t.Errorf("scope = %q, want read:user offline_access", form.Get("scope"))
	}
}

func TestRequestDeviceCodeDefaultsMissingInterval(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	server := rawResponseServer(t, 0, `{"device_code":"device-code-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":600}`)
	dc, err := RequestDeviceCode(context.Background(), server.Client(), newTestConfig(server))
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if dc.Interval != 5*time.Second {
		t.Fatalf("interval = %v, want the 5s default", dc.Interval)
	}
	if !dc.Expiry.Equal(at.Add(10 * time.Minute)) {
		t.Fatalf("expiry = %v, want %v", dc.Expiry, at.Add(10*time.Minute))
	}
}

func TestRequestDeviceCodeRejectsMalformedResponses(t *testing.T) {
	cases := map[string]string{
		"invalid json":             "{",
		"missing device code":      `{"user_code":"u","verification_uri":"https://github.com/login/device","expires_in":900}`,
		"missing user code":        `{"device_code":"d","verification_uri":"https://github.com/login/device","expires_in":900}`,
		"missing verification uri": `{"device_code":"d","user_code":"u","expires_in":900}`,
		"missing expiry":           `{"device_code":"d","user_code":"u","verification_uri":"https://github.com/login/device"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, body)
			if _, err := RequestDeviceCode(context.Background(), server.Client(), newTestConfig(server)); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("RequestDeviceCode() error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestPollDeviceOncePendingAndSlowDown(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{codes: []string{"authorization_pending", "slow_down"}}
	server := script.server(t)
	dc := testDeviceCode(at)
	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second} {
		tokens, state, interval, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), dc)
		if err != nil {
			t.Fatalf("poll %d error = %v", i, err)
		}
		if state != PollPending {
			t.Fatalf("poll %d state = %v, want PollPending", i, state)
		}
		if interval != want {
			t.Fatalf("poll %d interval = %v, want %v", i, interval, want)
		}
		if tokens != (TokenSet{}) {
			t.Fatalf("poll %d returned a token set while pending", i)
		}
	}
	form := script.lastForm()
	if form.Get("client_id") != ClientID || form.Get("device_code") != dc.DeviceCode {
		t.Fatalf("device poll form carried client_id = %q device_code = %q", form.Get("client_id"), form.Get("device_code"))
	}
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
		t.Fatalf("grant_type = %q, want the device code grant", form.Get("grant_type"))
	}
}

func TestPollDeviceOnceAuthorizedReturnsTokenSet(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{}
	server := script.server(t)
	tokens, state, _, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), testDeviceCode(at))
	if err != nil {
		t.Fatalf("PollDeviceOnce() error = %v", err)
	}
	if state != PollAuthorized {
		t.Fatalf("state = %v, want PollAuthorized", state)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" {
		t.Fatalf("token set did not carry the issued pair")
	}
	if tokens.TokenType != "bearer" || tokens.Scope != "read:user offline_access" {
		t.Fatalf("token type = %q scope = %q", tokens.TokenType, tokens.Scope)
	}
	if !tokens.AccessExpiry.Equal(at.Add(8 * time.Hour)) {
		t.Fatalf("access expiry = %v, want %v", tokens.AccessExpiry, at.Add(8*time.Hour))
	}
	if !tokens.RefreshExpiry.Equal(at.Add(15897600 * time.Second)) {
		t.Fatalf("refresh expiry = %v, want %v", tokens.RefreshExpiry, at.Add(15897600*time.Second))
	}
}

func TestPollDeviceOnceReportsDenial(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{codes: []string{"access_denied"}}
	server := script.server(t)
	if _, _, _, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), testDeviceCode(at)); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("PollDeviceOnce() error = %v, want ErrAccessDenied", err)
	}
}

func TestPollDeviceOnceReportsServerExpiry(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{codes: []string{"expired_token"}}
	server := script.server(t)
	_, state, _, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), testDeviceCode(at))
	if err != nil {
		t.Fatalf("PollDeviceOnce() error = %v", err)
	}
	if state != PollExpired {
		t.Fatalf("state = %v, want PollExpired", state)
	}
}

func TestPollDeviceOnceExpiresLocallyWithoutRequest(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{}
	server := script.server(t)
	dc := testDeviceCode(at)
	dc.Expiry = at.Add(-time.Second)
	_, state, interval, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), dc)
	if err != nil {
		t.Fatalf("PollDeviceOnce() error = %v", err)
	}
	if state != PollExpired {
		t.Fatalf("state = %v, want PollExpired", state)
	}
	if interval != dc.Interval {
		t.Fatalf("interval = %v, want %v", interval, dc.Interval)
	}
	if script.callCount() != 0 {
		t.Fatalf("expired device code contacted GitHub %d times, want 0", script.callCount())
	}
}

func TestPollDeviceOnceRejectsMalformedResponses(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	for name, body := range map[string]string{"invalid json": "{", "missing access token": "{}"} {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, body)
			if _, _, _, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), testDeviceCode(at)); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("PollDeviceOnce() error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestPollDeviceOnceHonorsCancellation(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{}
	server := script.server(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := PollDeviceOnce(ctx, server.Client(), newTestConfig(server), testDeviceCode(at)); !errors.Is(err, context.Canceled) {
		t.Fatalf("PollDeviceOnce() error = %v, want context.Canceled", err)
	}
	if script.callCount() != 0 {
		t.Fatalf("cancelled poll contacted GitHub %d times, want 0", script.callCount())
	}
}
