package openai

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/auth/openai/oaitest"
)

// TestWithOptionsSetCollaboratorsAndIgnoreNil exercises the three With* helpers,
// including their nil-argument guards, through the real Service fields.
func TestWithOptionsSetCollaboratorsAndIgnoreNil(t *testing.T) {
	svc := NewService(DefaultConfig(), nil)

	notified := false
	svc.WithConcurrentLoginNotifier(func() { notified = true })
	if svc.notifyConcurrentLogin == nil {
		t.Fatal("WithConcurrentLoginNotifier did not set notifier")
	}
	svc.notifyConcurrentLogin()
	if !notified {
		t.Fatal("registered notifier was not the one invoked")
	}

	// A non-nil opener replaces the default; a subsequent nil opener is ignored.
	openerCalled := false
	svc.WithBrowserOpener(func(string) error { openerCalled = true; return nil })
	svc.WithBrowserOpener(nil)
	if err := svc.openBrowser("http://example.test"); err != nil {
		t.Fatalf("openBrowser() error = %v", err)
	}
	if !openerCalled {
		t.Fatal("WithBrowserOpener(nil) clobbered the previously set opener")
	}

	// Same for the manual redirect reader.
	svc.WithManualRedirectReader(func(context.Context) (string, error) {
		return "http://localhost/auth/callback?code=c&state=s", nil
	})
	svc.WithManualRedirectReader(nil)
	got, err := svc.readRedirectURL(context.Background())
	if err != nil {
		t.Fatalf("readRedirectURL() error = %v", err)
	}
	if !strings.Contains(got, "code=c") {
		t.Fatalf("readRedirectURL() = %q, want the reader set by WithManualRedirectReader", got)
	}
}

// TestWithOptionsChainReturnsReceiver verifies the fluent builder returns the
// same Service so calls can chain.
func TestWithOptionsChainReturnsReceiver(t *testing.T) {
	svc := NewService(DefaultConfig(), nil)
	got := svc.
		WithConcurrentLoginNotifier(func() {}).
		WithBrowserOpener(func(string) error { return nil }).
		WithManualRedirectReader(func(context.Context) (string, error) { return "", nil })
	if got != svc {
		t.Fatal("With* chain did not return the receiver")
	}
}

// TestNewServiceDefaultReadRedirectBlocksUntilCancel confirms the default
// manual-redirect reader wired by NewService blocks until the context ends and
// then reports the cancellation cause.
func TestNewServiceDefaultReadRedirectBlocksUntilCancel(t *testing.T) {
	svc := NewService(DefaultConfig(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := svc.readRedirectURL(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("default readRedirectURL error = %v, want context.Canceled", err)
	}
}

// TestNewServiceDefaultStartCallbackServer confirms NewService's default
// startCallbackServer wiring stands up a real localhost callback server.
func TestNewServiceDefaultStartCallbackServer(t *testing.T) {
	svc := NewService(DefaultConfig(), nil)

	// Port 0 asks the OS for a free port so the test never collides with a
	// long-lived process on the well-known callback ports.
	cb, err := svc.startCallbackServer(context.Background(), svc.config(), 0, "state")
	if err != nil {
		t.Fatalf("default startCallbackServer error = %v", err)
	}
	defer func() { _ = cb.Close() }()

	if !strings.HasPrefix(cb.RedirectURI(), "http://localhost:") {
		t.Fatalf("RedirectURI = %q, want localhost callback", cb.RedirectURI())
	}
}

func TestLoginStartCallbackServerFailurePropagates(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC))
	svc.startCallbackServer = func(context.Context, Config, int, string) (callbackServer, error) {
		return nil, errors.New("port in use")
	}

	if _, err := svc.Login(context.Background(), t.TempDir(), "openai"); err == nil ||
		!strings.Contains(err.Error(), "port in use") {
		t.Fatalf("Login() error = %v, want callback-server startup failure", err)
	}
}

func TestLoginAuthorizeURLFailurePropagates(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 5, 0, 0, time.UTC))
	// An empty RedirectURI makes AuthorizeURL fail its required-field check.
	svc.startCallbackServer = func(context.Context, Config, int, string) (callbackServer, error) {
		return &stubCallbackServer{redirectURI: ""}, nil
	}

	if _, err := svc.Login(context.Background(), t.TempDir(), "openai"); err == nil ||
		!strings.Contains(err.Error(), "redirect URI is required") {
		t.Fatalf("Login() error = %v, want AuthorizeURL failure", err)
	}
}

func TestLoginExchangeCodeFailurePropagates(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 10, 0, 0, time.UTC))
	svc.exchangeCode = func(context.Context, *http.Client, Config, TokenExchangeRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 400")
	}

	if _, err := svc.Login(context.Background(), t.TempDir(), "openai"); err == nil ||
		!strings.Contains(err.Error(), "status 400") {
		t.Fatalf("Login() error = %v, want token-exchange failure", err)
	}
}

func TestLoginSaveFailurePropagates(t *testing.T) {
	svc := newTestService(time.Date(2026, 5, 8, 0, 15, 0, 0, time.UTC))

	// A regular file where the state dir should be makes SaveAuth's MkdirAll fail.
	stateFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(stateFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := svc.Login(context.Background(), stateFile, "openai"); err == nil {
		t.Fatal("Login() error = nil, want SaveAuth failure")
	}
}

func TestStatusSurfacesCorruptRecordError(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	stateDir := t.TempDir()
	writeCorruptAuthFile(t, stateDir, "openai")

	svc := newTestService(time.Date(2026, 5, 8, 0, 20, 0, 0, time.UTC))
	_, err := svc.Status(stateDir, "openai")
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("Status() error = %v, want ErrAuthCorrupt", err)
	}
}

func TestResolveRuntimeCredentialsSurfacesCorruptRecordError(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	stateDir := t.TempDir()
	writeCorruptAuthFile(t, stateDir, "openai")

	svc := newTestService(time.Date(2026, 5, 8, 0, 25, 0, 0, time.UTC))
	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if !errors.Is(err, ErrAuthCorrupt) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrAuthCorrupt", err)
	}
	if errors.Is(err, ErrLoginRequired) {
		t.Fatalf("corrupt record should not be reported as login-required: %v", err)
	}
}

// TestResolveRuntimeCredentialsBlankRefreshTokenRequiresRelogin covers the
// no-usable-refresh-token arm: a record whose refresh token is only whitespace
// passes Validate on load yet cannot be refreshed.
func TestResolveRuntimeCredentialsBlankRefreshTokenRequiresRelogin(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 5, 8, 0, 30, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.RefreshToken = "   "
	record.Expiry = now.Add(time.Minute) // near expiry so a refresh is attempted
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatalf("SaveAuth() error = %v", err)
	}

	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		t.Fatal("refreshToken must not be called when the stored refresh token is blank")
		return TokenSet{}, nil
	}

	_, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai")
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials() error = %v, want ErrLoginRequired", err)
	}
}

func TestNeedsRefreshTreatsZeroExpiryAsStale(t *testing.T) {
	now := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	if !needsRefresh(now, time.Time{}) {
		t.Fatal("needsRefresh(now, zero) = false, want true")
	}
	if needsRefresh(now, now.Add(time.Hour)) {
		t.Fatal("needsRefresh(now, +1h) = true, want false")
	}
	if !needsRefresh(now, now.Add(refreshSkew-time.Second)) {
		t.Fatal("needsRefresh within skew = false, want true")
	}
}

func TestIsPermanentRefreshError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"invalid_grant", errors.New("token endpoint returned status 400: invalid_grant"), true},
		{"invalid_request", errors.New("status 400: invalid_request"), true},
		{"unauthorized_client", errors.New("status 401: unauthorized_client"), true},
		{"access_denied", errors.New("status 403: access_denied"), true},
		{"transient", errors.New("token endpoint returned status 503"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanentRefreshError(tt.err); got != tt.want {
				t.Fatalf("isPermanentRefreshError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func writeCorruptAuthFile(t *testing.T, stateDir, instanceName string) {
	t.Helper()
	path := AuthFilePath(stateDir, instanceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
