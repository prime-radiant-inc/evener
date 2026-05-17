package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deviceMockServer is a tiny httptest router that lets each test wire up the
// three relevant endpoints independently. Counters live on the struct so
// assertions can check how many times each was called.
type deviceMockServer struct {
	t            *testing.T
	server       *httptest.Server
	usercode     http.HandlerFunc
	token        http.HandlerFunc
	oauthToken   http.HandlerFunc
	usercodeHits atomic.Int32
	tokenHits    atomic.Int32
	oauthHits    atomic.Int32
}

func newDeviceMockServer(t *testing.T) *deviceMockServer {
	t.Helper()
	m := &deviceMockServer{t: t}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			m.usercodeHits.Add(1)
			if m.usercode != nil {
				m.usercode(w, r)
				return
			}
			http.Error(w, "usercode not configured", http.StatusInternalServerError)
		case "/api/accounts/deviceauth/token":
			m.tokenHits.Add(1)
			if m.token != nil {
				m.token(w, r)
				return
			}
			http.Error(w, "token not configured", http.StatusInternalServerError)
		case "/oauth/token":
			m.oauthHits.Add(1)
			if m.oauthToken != nil {
				m.oauthToken(w, r)
				return
			}
			http.Error(w, "oauth not configured", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *deviceMockServer) cfg() Config {
	c := DefaultConfig()
	c.IssuerBaseURL = m.server.URL
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode mock body: %v", err)
		}
	}
}

func TestRequestDeviceCodeSuccessParsesInterval(t *testing.T) {
	m := newDeviceMockServer(t)

	var capturedBody map[string]any
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-123",
			"user_code":      "CODE-12345",
			"interval":       "7",
		})
	}

	dc, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if dc.UserCode != "CODE-12345" || dc.DeviceAuthID != "dev-123" {
		t.Fatalf("DeviceCode = %+v", dc)
	}
	if dc.Interval != 7*time.Second {
		t.Fatalf("Interval = %v, want 7s", dc.Interval)
	}
	if want := m.server.URL + "/codex/device"; dc.VerificationURL != want {
		t.Fatalf("VerificationURL = %q, want %q", dc.VerificationURL, want)
	}
	if got, _ := capturedBody["client_id"].(string); got != ClientID {
		t.Fatalf("client_id = %q, want %q", got, ClientID)
	}
}

func TestRequestDeviceCodeAcceptsNumericInterval(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-num",
			"user_code":      "NUMC",
			"interval":       3,
		})
	}

	dc, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if dc.Interval != 3*time.Second {
		t.Fatalf("Interval = %v, want 3s", dc.Interval)
	}
}

func TestRequestDeviceCodeDefaultsIntervalWhenMissing(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-missing",
			"user_code":      "MISS",
		})
	}

	dc, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err != nil {
		t.Fatalf("RequestDeviceCode() error = %v", err)
	}
	if dc.Interval != defaultDeviceInterval {
		t.Fatalf("Interval = %v, want %v", dc.Interval, defaultDeviceInterval)
	}
}

func TestRequestDeviceCodeNotFoundIsClearError(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}

	_, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err == nil {
		t.Fatal("RequestDeviceCode() error = nil, want device-code disabled error")
	}
	if !strings.Contains(err.Error(), "device-code login is not enabled") {
		t.Fatalf("error = %q, want clear device-code disabled message", err)
	}
}

func TestRequestDeviceCodeSurfacesUnexpectedStatus(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}

	_, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err == nil {
		t.Fatal("RequestDeviceCode() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %q, want status 502 surfaced", err)
	}
}

func TestPollDeviceAuthHappyPathAfterTwoPending(t *testing.T) {
	m := newDeviceMockServer(t)
	var attempt atomic.Int32
	m.token = func(w http.ResponseWriter, r *http.Request) {
		switch attempt.Add(1) {
		case 1:
			w.WriteHeader(http.StatusForbidden)
		case 2:
			w.WriteHeader(http.StatusNotFound)
		default:
			writeJSON(t, w, http.StatusOK, map[string]any{
				"authorization_code": "auth-321",
				"code_challenge":     "challenge-321",
				"code_verifier":      "verifier-321",
			})
		}
	}

	dc := DeviceCode{
		DeviceAuthID: "dev-321",
		UserCode:     "CODE-321",
		Interval:     10 * time.Millisecond,
	}
	got, err := pollDeviceAuth(context.Background(), m.server.Client(), m.cfg(), dc, pollOptions{
		maxWait: time.Second,
	})
	if err != nil {
		t.Fatalf("pollDeviceAuth() error = %v", err)
	}
	if got.AuthorizationCode != "auth-321" || got.CodeVerifier != "verifier-321" || got.CodeChallenge != "challenge-321" {
		t.Fatalf("DeviceCodeSuccess = %+v", got)
	}
	if attempt.Load() != 3 {
		t.Fatalf("token endpoint hit %d times, want 3", attempt.Load())
	}
}

func TestPollDeviceAuthTimesOut(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}

	// Inject a fake clock so we can simulate the 15-minute timeout in a few
	// loop iterations without any wall-clock sleep.
	var mu sync.Mutex
	current := time.Unix(0, 0)
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	sleep := func(_ context.Context, d time.Duration) error {
		mu.Lock()
		current = current.Add(d)
		mu.Unlock()
		return nil
	}

	dc := DeviceCode{
		DeviceAuthID: "dev-timeout",
		UserCode:     "CODE-TO",
		Interval:     20 * time.Millisecond,
	}
	_, err := pollDeviceAuth(context.Background(), m.server.Client(), m.cfg(), dc, pollOptions{
		maxWait: 50 * time.Millisecond,
		sleep:   sleep,
		now:     now,
	})
	if err == nil {
		t.Fatal("pollDeviceAuth() error = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %q, want timeout message", err)
	}
}

func TestPollDeviceAuthContextCancellationDuringSleep(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}

	ctx, cancel := context.WithCancel(context.Background())
	dc := DeviceCode{
		DeviceAuthID: "dev-cancel",
		UserCode:     "CODE-C",
		Interval:     time.Hour, // long enough that the cancel must abort it
	}

	done := make(chan error, 1)
	go func() {
		_, err := pollDeviceAuth(ctx, m.server.Client(), m.cfg(), dc, pollOptions{
			maxWait: 10 * time.Second,
		})
		done <- err
	}()

	// Give the goroutine a moment to make at least one request and enter sleep.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pollDeviceAuth() error = nil, want context cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pollDeviceAuth() did not return after context cancel")
	}
}

func TestPollDeviceAuthSurfacesUnexpectedStatus(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}

	dc := DeviceCode{
		DeviceAuthID: "dev-500",
		UserCode:     "CODE-500",
		Interval:     10 * time.Millisecond,
	}
	_, err := pollDeviceAuth(context.Background(), m.server.Client(), m.cfg(), dc, pollOptions{
		maxWait: time.Second,
	})
	if err == nil {
		t.Fatal("pollDeviceAuth() error = nil, want 500 error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %q, want status 500 surfaced", err)
	}
}

func TestExchangeDeviceCodeUsesDeviceRedirectURI(t *testing.T) {
	m := newDeviceMockServer(t)
	var gotForm url.Values
	m.oauthToken = func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		writeJSON(t, w, http.StatusOK, tokenEndpointResponse{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      "id-token",
			TokenType:    "Bearer",
			Scope:        "openid",
			ExpiresIn:    3600,
		})
	}

	tokens, err := ExchangeDeviceCode(context.Background(), m.server.Client(), m.cfg(), "auth-code", "verifier-from-server")
	if err != nil {
		t.Fatalf("ExchangeDeviceCode() error = %v", err)
	}
	if tokens.AccessToken != "access-token" {
		t.Fatalf("AccessToken = %q", tokens.AccessToken)
	}
	wantRedirect := m.server.URL + "/deviceauth/callback"
	if got := gotForm.Get("redirect_uri"); got != wantRedirect {
		t.Fatalf("redirect_uri = %q, want %q", got, wantRedirect)
	}
	if got := gotForm.Get("code_verifier"); got != "verifier-from-server" {
		t.Fatalf("code_verifier = %q, want %q", got, "verifier-from-server")
	}
	if got := gotForm.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type = %q, want authorization_code", got)
	}
}

func TestLoginWithDeviceEndToEnd(t *testing.T) {
	m := newDeviceMockServer(t)
	stateDir := t.TempDir()

	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-e2e",
			"user_code":      "E2E-CODE",
			"interval":       "0",
		})
	}
	var pollAttempt atomic.Int32
	m.token = func(w http.ResponseWriter, r *http.Request) {
		if pollAttempt.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"authorization_code": "auth-e2e",
			"code_challenge":     "challenge-e2e",
			"code_verifier":      "verifier-e2e",
		})
	}
	m.oauthToken = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, tokenEndpointResponse{
			AccessToken:  "access-e2e",
			RefreshToken: "refresh-e2e",
			IDToken:      "id-e2e",
			TokenType:    "Bearer",
			Scope:        "openid",
			ExpiresIn:    3600,
		})
	}

	svc := NewService(m.cfg(), m.server.Client())

	var captured DeviceCode
	status, err := svc.LoginWithDevice(context.Background(), stateDir, func(dc DeviceCode) {
		captured = dc
	})
	if err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	if captured.UserCode != "E2E-CODE" {
		t.Fatalf("showPrompt UserCode = %q, want %q", captured.UserCode, "E2E-CODE")
	}
	if !status.SignedIn || status.Source != AuthSourceOAuth {
		t.Fatalf("status = %+v, want signed-in oauth", status)
	}

	record, err := LoadAuth(stateDir)
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if record.AccessToken != "access-e2e" || record.RefreshToken != "refresh-e2e" {
		t.Fatalf("stored record = %+v", record)
	}
}

func TestLoginWithDeviceUsercodeFailureLeavesNoAuth(t *testing.T) {
	m := newDeviceMockServer(t)
	stateDir := t.TempDir()

	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}

	svc := NewService(m.cfg(), m.server.Client())
	called := false
	_, err := svc.LoginWithDevice(context.Background(), stateDir, func(DeviceCode) { called = true })
	if err == nil {
		t.Fatal("LoginWithDevice() error = nil, want device-code disabled error")
	}
	if called {
		t.Fatal("showPrompt called despite usercode failure")
	}

	if _, err := LoadAuth(stateDir); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth error = %v, want ErrAuthNotFound", err)
	}
}

