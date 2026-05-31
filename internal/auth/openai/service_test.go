package openai

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/auth/openai/oaitest"
)

func TestLoginSucceedsViaCallbackPath(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 7, 23, 15, 0, 0, time.UTC)
	var gotPort int

	server := &stubCallbackServer{
		redirectURI: "http://localhost:1455/auth/callback",
		waitResult:  CallbackResult{Code: "callback-code", State: "expected-state"},
	}

	svc := newTestService(now)
	svc.startCallbackServer = func(_ Config, port int, _ string) (callbackServer, error) {
		gotPort = port
		return server, nil
	}
	svc.exchangeCode = func(ctx context.Context, client *http.Client, cfg Config, req TokenExchangeRequest) (TokenSet, error) {
		if req.Code != "callback-code" {
			t.Fatalf("Code = %q, want %q", req.Code, "callback-code")
		}
		if req.RedirectURI != server.redirectURI {
			t.Fatalf("RedirectURI = %q, want %q", req.RedirectURI, server.redirectURI)
		}
		if strings.TrimSpace(req.CodeVerifier) == "" {
			t.Fatal("CodeVerifier = empty, want generated verifier")
		}
		return TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken: testJWT(t, map[string]any{
				"email":        "user@example.com",
				"account_id":   "acct_123",
				"workspace_id": "ws_123",
			}),
			TokenType: "Bearer",
			Scope:     "openid profile email offline_access",
			Expiry:    now.Add(time.Hour),
		}, nil
	}

	status, err := svc.Login(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if status.Source != AuthSourceOAuth {
		t.Fatalf("Source = %q, want %q", status.Source, AuthSourceOAuth)
	}
	if status.Email != "user@example.com" {
		t.Fatalf("Email = %q, want %q", status.Email, "user@example.com")
	}
	if gotPort != DefaultCallbackPort {
		t.Fatalf("callback port = %d, want %d", gotPort, DefaultCallbackPort)
	}

	record, err := LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.Email != "user@example.com" || record.AccountID != "acct_123" || record.WorkspaceID != "ws_123" {
		t.Fatalf("stored record = %+v", record)
	}
}

func TestLoginSucceedsViaManualPastebackPath(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 7, 23, 20, 0, 0, time.UTC)
	var expectedState string
	var gotPort int

	server := &stubCallbackServer{
		redirectURI: "http://localhost:1455/auth/callback",
		waitErr:     context.DeadlineExceeded,
	}

	svc := newTestService(now)
	svc.startCallbackServer = func(_ Config, port int, state string) (callbackServer, error) {
		expectedState = state
		gotPort = port
		return server, nil
	}
	svc.readRedirectURL = func(context.Context) (string, error) {
		return server.redirectURI + "?code=manual-code&state=" + expectedState, nil
	}
	svc.exchangeCode = func(ctx context.Context, client *http.Client, cfg Config, req TokenExchangeRequest) (TokenSet, error) {
		if req.Code != "manual-code" {
			t.Fatalf("Code = %q, want %q", req.Code, "manual-code")
		}
		return TokenSet{
			AccessToken:  "manual-access-token",
			RefreshToken: "manual-refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			Expiry:       now.Add(2 * time.Hour),
		}, nil
	}

	status, err := svc.Login(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if gotPort != DefaultCallbackPort {
		t.Fatalf("callback port = %d, want %d", gotPort, DefaultCallbackPort)
	}

	record, err := LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.AccessToken != "manual-access-token" {
		t.Fatalf("stored AccessToken = %q, want %q", record.AccessToken, "manual-access-token")
	}
}

func TestLoginManualPastebackDoesNotWaitForCallbackTimeout(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 7, 23, 22, 0, 0, time.UTC)
	var expectedState string

	svc := newTestService(now)
	svc.cfg = Config{CallbackTimeout: 10 * time.Second}
	svc.startCallbackServer = func(_ Config, _ int, state string) (callbackServer, error) {
		expectedState = state
		return &stubCallbackServer{
			redirectURI: "http://localhost:1455/auth/callback",
			waitFunc: func(ctx context.Context) (CallbackResult, error) {
				<-ctx.Done()
				return CallbackResult{}, ctx.Err()
			},
		}, nil
	}
	svc.readRedirectURL = func(context.Context) (string, error) {
		return "http://localhost:1455/auth/callback?code=manual-code&state=" + expectedState, nil
	}
	svc.exchangeCode = func(context.Context, *http.Client, Config, TokenExchangeRequest) (TokenSet, error) {
		return TokenSet{
			AccessToken:  "manual-access-token",
			RefreshToken: "manual-refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	status, err := svc.Login(ctx, stateDir, "openai")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Login() took %v, want manual fallback without waiting for callback timeout", elapsed)
	}
}

func TestLoginBrowserOpenFailureIsNonFatal(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 7, 23, 25, 0, 0, time.UTC)

	svc := newTestService(now)
	svc.openBrowser = func(string) error { return errors.New("browser unavailable") }
	svc.startCallbackServer = func(Config, int, string) (callbackServer, error) {
		return &stubCallbackServer{
			redirectURI: "http://localhost:1455/auth/callback",
			waitResult:  CallbackResult{Code: "callback-code", State: "expected-state"},
		}, nil
	}
	svc.exchangeCode = func(context.Context, *http.Client, Config, TokenExchangeRequest) (TokenSet, error) {
		return TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			Expiry:       now.Add(time.Hour),
		}, nil
	}

	if _, err := svc.Login(context.Background(), stateDir, "openai"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
}

func TestStatusSignedOutWhenNoEnvOrStoredAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	svc := newTestService(time.Date(2026, 5, 7, 23, 30, 0, 0, time.UTC))

	status, err := svc.Status(t.TempDir(), "openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SignedIn {
		t.Fatal("SignedIn = true, want false")
	}
	if status.Source != AuthSourceSignedOut {
		t.Fatalf("Source = %q, want %q", status.Source, AuthSourceSignedOut)
	}
}

func TestStatusUsesEnvWhenNoStoredAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()

	svc := newTestService(time.Date(2026, 5, 7, 23, 35, 0, 0, time.UTC))
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if status.Source != AuthSourceEnv {
		t.Fatalf("Source = %q, want %q", status.Source, AuthSourceEnv)
	}
}

func TestStatusPrefersStoredOAuthOverEnv(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()
	if err := SaveAuth(stateDir, "openai", sampleAuthRecord()); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(time.Date(2026, 5, 7, 23, 35, 0, 0, time.UTC))
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if status.Source != AuthSourceOAuth {
		t.Fatalf("Source = %q, want %q", status.Source, AuthSourceOAuth)
	}
}

func TestStatusReflectsStoredOAuthState(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(time.Date(2026, 5, 7, 23, 40, 0, 0, time.UTC))
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.SignedIn {
		t.Fatal("SignedIn = false, want true")
	}
	if status.Source != AuthSourceOAuth {
		t.Fatalf("Source = %q, want %q", status.Source, AuthSourceOAuth)
	}
	if status.Email != record.Email {
		t.Fatalf("Email = %q, want %q", status.Email, record.Email)
	}
}

func TestLogoutDeletesStoredAuth(t *testing.T) {
	stateDir := t.TempDir()
	if err := SaveAuth(stateDir, "openai", sampleAuthRecord()); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(time.Date(2026, 5, 7, 23, 45, 0, 0, time.UTC))
	deleted, err := svc.Logout(stateDir, "openai")
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if !deleted {
		t.Fatal("Logout() deleted = false, want true")
	}
	if _, err := LoadAuth(stateDir, "openai"); !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("LoadAuth() error = %v, want ErrAuthNotFound", err)
	}
}

func TestRuntimeCredentialsStoredAuthWinsOverEnv(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	record.AccessToken = "stored-access-token"
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(time.Date(2026, 5, 7, 23, 50, 0, 0, time.UTC))
	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if creds.BearerToken != "stored-access-token" {
		t.Fatalf("BearerToken = %q, want %q", creds.BearerToken, "stored-access-token")
	}
	if creds.Source != AuthSourceOAuth {
		t.Fatalf("Source = %q, want %q", creds.Source, AuthSourceOAuth)
	}
}

func TestRuntimeCredentialsFallsBackToEnvWhenNoStoredAuth(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	t.Setenv("OPENAI_API_KEY", "sk-env")
	stateDir := t.TempDir()

	svc := newTestService(time.Date(2026, 5, 7, 23, 50, 0, 0, time.UTC))
	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if creds.BearerToken != "sk-env" {
		t.Fatalf("BearerToken = %q, want %q", creds.BearerToken, "sk-env")
	}
	if creds.Source != AuthSourceEnv {
		t.Fatalf("Source = %q, want %q", creds.Source, AuthSourceEnv)
	}
}

func TestRuntimeCredentialsReturnsStoredTokenWhenFresh(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	record.AccessToken = "stored-access-token"
	record.Expiry = time.Date(2026, 5, 8, 1, 0, 0, 0, time.UTC)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(time.Date(2026, 5, 7, 23, 55, 0, 0, time.UTC))
	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if creds.BearerToken != "stored-access-token" {
		t.Fatalf("BearerToken = %q, want %q", creds.BearerToken, "stored-access-token")
	}
	if creds.Source != AuthSourceOAuth {
		t.Fatalf("Source = %q, want %q", creds.Source, AuthSourceOAuth)
	}
}

func TestRuntimeCredentialsRefreshesNearExpiryToken(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.AccessToken = "stale-access-token"
	record.RefreshToken = "refresh-token"
	record.Expiry = now.Add(2 * time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(now)
	svc.refreshToken = func(ctx context.Context, client *http.Client, cfg Config, req RefreshTokenRequest) (TokenSet, error) {
		if req.RefreshToken != "refresh-token" {
			t.Fatalf("RefreshToken = %q, want %q", req.RefreshToken, "refresh-token")
		}
		return TokenSet{
			AccessToken:  "fresh-access-token",
			RefreshToken: "",
			IDToken: testJWT(t, map[string]any{
				"email": "fresh@example.com",
			}),
			TokenType: "Bearer",
			Scope:     "openid profile email offline_access",
			Expiry:    now.Add(time.Hour),
		}, nil
	}

	creds, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err != nil {
		t.Fatalf("ResolveRuntimeCredentials() error = %v", err)
	}
	if creds.BearerToken != "fresh-access-token" {
		t.Fatalf("BearerToken = %q, want %q", creds.BearerToken, "fresh-access-token")
	}

	record, err = LoadAuth(stateDir, "openai")
	if err != nil {
		t.Fatalf("LoadAuth() error = %v", err)
	}
	if record.AccessToken != "fresh-access-token" {
		t.Fatalf("stored AccessToken = %q, want %q", record.AccessToken, "fresh-access-token")
	}
	if record.RefreshToken != "refresh-token" {
		t.Fatalf("stored RefreshToken = %q, want preserved refresh token", record.RefreshToken)
	}
	if record.Email != "fresh@example.com" {
		t.Fatalf("stored Email = %q, want %q", record.Email, "fresh@example.com")
	}
}

func TestRuntimeCredentialsRefreshFailureRequiresRelogin(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 5, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 400: invalid_grant")
	}

	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrLoginRequired", err)
	}
	if !strings.Contains(err.Error(), "serf openai login") {
		t.Fatalf("ResolveRuntimeCredentials() error = %q, want re-login guidance", err)
	}
}

// TestRuntimeCredentialsNoStoredAuthUnwrapsToErrAuthNotFound verifies that with
// no stored auth and no env key, the login-required error stays errors.Is
// reachable to BOTH ErrLoginRequired and its ErrAuthNotFound cause (the %w
// wrapping), so callers like the openai env-adapter factory can match either.
func TestRuntimeCredentialsNoStoredAuthUnwrapsToErrAuthNotFound(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "")
	svc := newTestService(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))

	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrLoginRequired", err)
	}
	if !errors.Is(err, ErrAuthNotFound) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want errors.Is ErrAuthNotFound (the %%w cause)", err)
	}
}

func TestRuntimeCredentialsTransientRefreshFailureDoesNotRequireRelogin(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 10, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 503")
	}

	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if err == nil {
		t.Fatal("ResolveRuntimeCredentials() error = nil, want error")
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, should not require re-login", err)
	}
}

func TestNewServiceAppliesDefaultHTTPTimeoutForPartialConfig(t *testing.T) {
	svc := NewService(Config{ClientID: "client-id"}, nil)
	if svc.client == nil {
		t.Fatal("client = nil")
	}
	if svc.client.Timeout != DefaultConfig().HTTPTimeout {
		t.Fatalf("client timeout = %v, want %v", svc.client.Timeout, DefaultConfig().HTTPTimeout)
	}
}

func newTestService(now time.Time) *Service {
	tokens := TokenSet{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		TokenType:    "Bearer",
		Scope:        "openid profile email offline_access",
		Expiry:       now.Add(time.Hour),
	}

	return &Service{
		cfg: DefaultConfig(),
		client: &http.Client{
			Timeout: time.Second,
		},
		now:         func() time.Time { return now },
		openBrowser: func(string) error { return nil },
		readRedirectURL: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		startCallbackServer: func(Config, int, string) (callbackServer, error) {
			return &stubCallbackServer{
				redirectURI: "http://localhost:1455/auth/callback",
				waitResult:  CallbackResult{Code: "callback-code", State: "expected-state"},
			}, nil
		},
		exchangeCode: func(context.Context, *http.Client, Config, TokenExchangeRequest) (TokenSet, error) {
			return tokens, nil
		},
		refreshToken: func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
			return TokenSet{}, errors.New("unexpected refresh")
		},
	}
}

type stubCallbackServer struct {
	redirectURI string
	waitResult  CallbackResult
	waitErr     error
	waitFunc    func(context.Context) (CallbackResult, error)
	closed      bool
}

func (s *stubCallbackServer) RedirectURI() string {
	return s.redirectURI
}

func (s *stubCallbackServer) Wait(ctx context.Context) (CallbackResult, error) {
	if s.waitFunc != nil {
		return s.waitFunc(ctx)
	}
	return s.waitResult, s.waitErr
}

func (s *stubCallbackServer) Close() error {
	s.closed = true
	return nil
}
