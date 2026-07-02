package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseRedirectURLRejectsMissingSchemeOrHost covers the scheme/host
// emptiness guard: a relative URL parses cleanly yet has no scheme or host.
func TestParseRedirectURLRejectsMissingSchemeOrHost(t *testing.T) {
	_, _, err := ParseRedirectURL("/auth/callback?code=c&state=s")
	if !errors.Is(err, ErrInvalidRedirectURL) {
		t.Fatalf("ParseRedirectURL(relative) error = %v, want ErrInvalidRedirectURL", err)
	}
}

// TestDeleteAuthMissingFileReportsNotDeleted covers the ErrNotExist arm of
// DeleteAuth: no auth file present returns (false, nil).
func TestDeleteAuthMissingFileReportsNotDeleted(t *testing.T) {
	deleted, err := DeleteAuth(t.TempDir(), "openai")
	if err != nil {
		t.Fatalf("DeleteAuth(missing) error = %v, want nil", err)
	}
	if deleted {
		t.Fatal("DeleteAuth(missing) reported a deletion, want false")
	}
}

// TestFlexibleNumberRejectsNonIntegerJSONNumber covers the bare-number
// ParseUint error arm: a fractional JSON number is a valid JSON value (so
// UnmarshalJSON is invoked) but is not a base-10 unsigned integer.
func TestFlexibleNumberRejectsNonIntegerJSONNumber(t *testing.T) {
	var n flexibleNumber
	if err := json.Unmarshal([]byte("1.5"), &n); err == nil {
		t.Fatal("UnmarshalJSON(1.5) error = nil, want parse failure")
	}
}

// cov_errBodyRoundTripper returns a 2xx response whose body errors on read, so
// the io.ReadAll failure arm in RequestDeviceCode can be reached.
type cov_errBodyRoundTripper struct{}

func (cov_errBodyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(cov_errReader{}),
		Request:    req,
	}, nil
}

type cov_errReader struct{}

func (cov_errReader) Read([]byte) (int, error) { return 0, errors.New("body read failed") }

func covIssuerConfig() Config {
	c := DefaultConfig()
	c.IssuerBaseURL = authfz_issuer
	return c
}

// TestRequestDeviceCodeSurfacesTransportError covers the client.Do error arm.
func TestRequestDeviceCodeSurfacesTransportError(t *testing.T) {
	client := &http.Client{Transport: authfz_fakeRoundTripper{networkErr: true}}
	if _, err := RequestDeviceCode(context.Background(), client, covIssuerConfig()); err == nil {
		t.Fatal("RequestDeviceCode() error = nil, want transport failure")
	}
}

// TestRequestDeviceCodeSurfacesBodyReadError covers the io.ReadAll error arm.
func TestRequestDeviceCodeSurfacesBodyReadError(t *testing.T) {
	client := &http.Client{Transport: cov_errBodyRoundTripper{}}
	_, err := RequestDeviceCode(context.Background(), client, covIssuerConfig())
	if err == nil || !strings.Contains(err.Error(), "read device usercode response") {
		t.Fatalf("RequestDeviceCode() error = %v, want response read failure", err)
	}
}

// TestPollDeviceAuthOnceSurfacesTransportError covers the client.Do error arm.
func TestPollDeviceAuthOnceSurfacesTransportError(t *testing.T) {
	client := &http.Client{Transport: authfz_fakeRoundTripper{networkErr: true}}
	_, _, err := PollDeviceAuthOnce(context.Background(), client, covIssuerConfig(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err == nil {
		t.Fatal("PollDeviceAuthOnce() error = nil, want transport failure")
	}
}

// TestPollDeviceAuthDefaultsAndContextCancel covers, in one call, the nil-client
// default, the non-positive-interval default, and the loop-top context-cancel
// return of pollDeviceAuth.
func TestPollDeviceAuthDefaultsAndContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pollDeviceAuth(ctx, nil, DefaultConfig(), DeviceCode{}, pollOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pollDeviceAuth(cancelled) error = %v, want context.Canceled", err)
	}
}

// cov_nonTCPAddr is a net.Addr that is not a *net.TCPAddr, exercising the
// listenerPort type-assertion failure arm.
type cov_nonTCPAddr struct{}

func (cov_nonTCPAddr) Network() string { return "unix" }
func (cov_nonTCPAddr) String() string  { return "/tmp/socket" }

func TestListenerPortRejectsNonTCPAddr(t *testing.T) {
	if _, err := listenerPort(cov_nonTCPAddr{}); err == nil {
		t.Fatal("listenerPort(non-TCP) error = nil, want type-mismatch failure")
	}
}

// TestCallbackServerWaitAcceptsNilContext covers the nil-context guard in Wait
// by driving a real localhost callback to completion.
func TestCallbackServerWaitAcceptsNilContext(t *testing.T) {
	cb, err := StartCallbackServer(context.Background(), DefaultConfig(), 0, "state-nil")
	if err != nil {
		t.Fatalf("StartCallbackServer() error = %v", err)
	}
	defer func() { _ = cb.Close() }()

	go func() {
		resp, err := http.Get(cb.RedirectURI() + "?code=code-nil&state=state-nil")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	//nolint:staticcheck // deliberately passing a nil context to exercise the guard.
	result, err := cb.Wait(nil)
	if err != nil {
		t.Fatalf("Wait(nil) error = %v", err)
	}
	if result.Code != "code-nil" {
		t.Fatalf("Wait(nil) code = %q, want code-nil", result.Code)
	}
}

// TestLoginAcceptsNilContext covers Login's nil-context guard through a
// successful stubbed login flow.
func TestLoginAcceptsNilContext(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	//nolint:staticcheck // deliberately passing a nil context to exercise the guard.
	if _, err := svc.Login(nil, t.TempDir(), "openai"); err != nil {
		t.Fatalf("Login(nil ctx) error = %v", err)
	}
}

// TestRequestDeviceCodeSurfacesRequestBuildError covers the
// http.NewRequestWithContext error arm: a control character in the issuer URL
// makes the request builder reject the endpoint.
func TestRequestDeviceCodeSurfacesRequestBuildError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IssuerBaseURL = "http://example.test/\x7f"
	if _, err := RequestDeviceCode(context.Background(), nil, cfg); err == nil {
		t.Fatal("RequestDeviceCode() error = nil, want request-build failure")
	}
}

// TestPollDeviceAuthOnceSurfacesRequestBuildError covers the same arm in
// PollDeviceAuthOnce.
func TestPollDeviceAuthOnceSurfacesRequestBuildError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.IssuerBaseURL = "http://example.test/\x7f"
	_, _, err := PollDeviceAuthOnce(context.Background(), nil, cfg, DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err == nil {
		t.Fatal("PollDeviceAuthOnce() error = nil, want request-build failure")
	}
}

// covBlockingCallbackServer never returns from Wait until its context ends, so
// Login's manual-redirect goroutine is the branch that resolves the login.
func covBlockingCallbackServer() *stubCallbackServer {
	return &stubCallbackServer{
		redirectURI: "http://localhost:1455/auth/callback",
		waitFunc: func(ctx context.Context) (CallbackResult, error) {
			<-ctx.Done()
			return CallbackResult{}, ctx.Err()
		},
	}
}

// TestLoginManualRedirectParseFailure covers the ParseRedirectURL error arm of
// Login's manual-redirect goroutine (and the resulting error return).
func TestLoginManualRedirectParseFailure(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	svc.startCallbackServer = func(context.Context, Config, int, string) (callbackServer, error) {
		return covBlockingCallbackServer(), nil
	}
	svc.readRedirectURL = func(context.Context) (string, error) {
		return "/relative-no-scheme?code=c", nil // parses but fails the scheme/host guard
	}

	if _, err := svc.Login(context.Background(), t.TempDir(), "openai"); err == nil ||
		!errors.Is(err, ErrInvalidRedirectURL) {
		t.Fatalf("Login() error = %v, want ErrInvalidRedirectURL", err)
	}
}

// TestLoginManualRedirectStateMismatch covers the ValidateState error arm of
// Login's manual-redirect goroutine.
func TestLoginManualRedirectStateMismatch(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	svc.startCallbackServer = func(context.Context, Config, int, string) (callbackServer, error) {
		return covBlockingCallbackServer(), nil
	}
	svc.readRedirectURL = func(context.Context) (string, error) {
		return "http://localhost/auth/callback?code=c&state=WRONG", nil
	}

	if _, err := svc.Login(context.Background(), t.TempDir(), "openai"); err == nil ||
		!errors.Is(err, ErrStateMismatch) {
		t.Fatalf("Login() error = %v, want ErrStateMismatch", err)
	}
}

// TestWaitForLoginCompletion exercises the channel-selection arms directly: a
// cancelled manual result is dropped in favor of a later callback success, and
// once both sources report cancellation the cancelled parent context surfaces.
func TestWaitForLoginCompletion(t *testing.T) {
	t.Run("manual cancel then callback success", func(t *testing.T) {
		cb := make(chan callbackResult, 1)
		manual := make(chan callbackResult, 1)
		manual <- callbackResult{err: context.Canceled}
		cb <- callbackResult{result: CallbackResult{Code: "ok"}}

		got, err := waitForLoginCompletion(context.Background(), cb, manual)
		if err != nil {
			t.Fatalf("waitForLoginCompletion() error = %v", err)
		}
		if got.Code != "ok" {
			t.Fatalf("Code = %q, want ok", got.Code)
		}
	})

	t.Run("both deadline-exceeded then parent ctx error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cb := make(chan callbackResult, 1)
		manual := make(chan callbackResult, 1)
		cb <- callbackResult{err: context.DeadlineExceeded}
		manual <- callbackResult{err: context.DeadlineExceeded}

		_, err := waitForLoginCompletion(ctx, cb, manual)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForLoginCompletion() error = %v, want context.Canceled", err)
		}
	})

	t.Run("both deadline-exceeded with live parent reports deadline", func(t *testing.T) {
		cb := make(chan callbackResult, 1)
		manual := make(chan callbackResult, 1)
		cb <- callbackResult{err: context.DeadlineExceeded}
		manual <- callbackResult{err: context.DeadlineExceeded}

		_, err := waitForLoginCompletion(context.Background(), cb, manual)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waitForLoginCompletion() error = %v, want context.DeadlineExceeded", err)
		}
	})
}

// covDeviceService wires a Service to the device mock server with a poll loop
// that skips the inter-poll sleep.
func covDeviceService(t *testing.T, m *deviceMockServer) *Service {
	t.Helper()
	svc := NewService(m.cfg(), m.server.Client())
	svc.pollDeviceAuth = func(ctx context.Context, client *http.Client, cfg Config, dc DeviceCode) (DeviceCodeSuccess, error) {
		return pollDeviceAuth(ctx, client, cfg, dc, pollOptions{
			sleep: func(context.Context, time.Duration) error { return nil },
		})
	}
	return svc
}

func covUsercodeOK(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-cov", "user_code": "COV-CODE", "interval": "0",
		})
	}
}

func covTokenOK(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"authorization_code": "auth-cov", "code_challenge": "chal", "code_verifier": "ver-cov",
		})
	}
}

// TestLoginWithDeviceExchangeFailure covers the exchangeDeviceCode error arm:
// the device poll succeeds but the OAuth token endpoint rejects the exchange.
func TestLoginWithDeviceExchangeFailure(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = covUsercodeOK(t)
	m.token = covTokenOK(t)
	m.oauthToken = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}

	svc := covDeviceService(t, m)
	if _, err := svc.LoginWithDevice(context.Background(), t.TempDir(), "openai", nil); err == nil {
		t.Fatal("LoginWithDevice() error = nil, want token-exchange failure")
	}
}

// TestLoginWithDeviceSaveFailure covers the SaveAuth error arm: a full
// successful device flow whose state directory cannot be created.
func TestLoginWithDeviceSaveFailure(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = covUsercodeOK(t)
	m.token = covTokenOK(t)
	m.oauthToken = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, tokenEndpointResponse{
			AccessToken: "a", RefreshToken: "r", TokenType: "Bearer", Scope: "openid", ExpiresIn: 3600,
		})
	}

	stateFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(stateFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	svc := covDeviceService(t, m)
	if _, err := svc.LoginWithDevice(context.Background(), stateFile, "openai", nil); err == nil {
		t.Fatal("LoginWithDevice() error = nil, want SaveAuth failure")
	}
}

// TestLoginWithDeviceAppliesIDTokenClaimsAndNilContext covers the applyClaims
// arm (a parseable id_token), the watcher's default-interval branch (interval
// left at zero), and LoginWithDevice's nil-context guard, in one success flow.
func TestLoginWithDeviceAppliesIDTokenClaimsAndNilContext(t *testing.T) {
	idToken := testJWT(t, map[string]any{"email": "cov@example.com"})

	m := newDeviceMockServer(t)
	m.usercode = covUsercodeOK(t)
	m.token = covTokenOK(t)
	m.oauthToken = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, tokenEndpointResponse{
			AccessToken: "a", RefreshToken: "r", IDToken: idToken,
			TokenType: "Bearer", Scope: "openid", ExpiresIn: 3600,
		})
	}

	svc := covDeviceService(t, m)
	svc.concurrentLoginWatchInterval = 0 // force the watcher's default-interval branch

	//nolint:staticcheck // deliberately passing a nil context to exercise the guard.
	status, err := svc.LoginWithDevice(nil, t.TempDir(), "openai", nil)
	if err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	if status.Email != "cov@example.com" {
		t.Fatalf("status.Email = %q, want cov@example.com (id_token claims not applied)", status.Email)
	}
}

// TestWatchForConcurrentLoginSkipsNonOAuthRecord covers the non-OAuth-source
// continue arm of the watcher: a parallel process writes an env-sourced record
// (which must be ignored) before writing the OAuth record that ends the flow.
func TestWatchForConcurrentLoginSkipsNonOAuthRecord(t *testing.T) {
	m := newDeviceMockServer(t)
	stateDir := t.TempDir()

	m.usercode = covUsercodeOK(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // never naturally succeeds
	}

	svc := NewService(m.cfg(), m.server.Client())
	svc.concurrentLoginWatchInterval = 10 * time.Millisecond

	go func() {
		base := AuthRecord{
			Version: 1, Provider: "openai", ObtainedAt: time.Now(), TokenType: "Bearer",
			Scope: "openid", AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour),
		}
		// First a non-OAuth record: the watcher must load and skip it.
		envRecord := base
		envRecord.Source = AuthSourceEnv
		time.Sleep(30 * time.Millisecond)
		_ = SaveAuth(stateDir, "openai", envRecord)

		// Then the OAuth record that the watcher accepts, ending the flow.
		oauthRecord := base
		oauthRecord.Source = AuthSourceOAuth
		oauthRecord.ObtainedAt = time.Now()
		time.Sleep(40 * time.Millisecond)
		_ = SaveAuth(stateDir, "openai", oauthRecord)
	}()

	status, err := svc.LoginWithDevice(context.Background(), stateDir, "openai", nil)
	if err != nil {
		t.Fatalf("LoginWithDevice() error = %v", err)
	}
	if status.Source != AuthSourceOAuth {
		t.Fatalf("status.Source = %q, want %q", status.Source, AuthSourceOAuth)
	}
}
