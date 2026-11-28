package githubcopilot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestExchangeCopilotTokenUsesPiCompatibilityHeaders pins the live-proven
// exchange: GET /copilot_internal/v2/token with the GitHub token as bearer and
// the public Pi/VS Code compatibility headers, returning the session token and
// its absolute unix-second expiry.
func TestExchangeCopilotTokenUsesPiCompatibilityHeaders(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	var got struct {
		method, path, authorization, accept    string
		userAgent, editor, plugin, integration string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.authorization = r.Header.Get("Authorization")
		got.accept = r.Header.Get("Accept")
		got.userAgent = r.Header.Get("User-Agent")
		got.editor = r.Header.Get("Editor-Version")
		got.plugin = r.Header.Get("Editor-Plugin-Version")
		got.integration = r.Header.Get("Copilot-Integration-Id")
		writeJSON(w, map[string]any{"token": "copilot-session", "expires_at": at.Add(30 * time.Minute).Unix()})
	}))
	t.Cleanup(server.Close)

	session, err := ExchangeCopilotToken(context.Background(), server.Client(), newTestConfig(server), "github-access")
	if err != nil {
		t.Fatalf("ExchangeCopilotToken() error = %v", err)
	}
	if session.Token != "copilot-session" || !session.Expiry.Equal(at.Add(30*time.Minute)) {
		t.Fatalf("session = %+v, want token and 30-minute expiry", session)
	}
	if got.method != http.MethodGet || got.path != "/copilot_internal/v2/token" {
		t.Fatalf("request = %s %s, want GET /copilot_internal/v2/token", got.method, got.path)
	}
	if got.authorization != "Bearer github-access" || got.accept != "application/json" {
		t.Fatalf("Authorization = %q Accept = %q", got.authorization, got.accept)
	}
	if got.userAgent != PiUserAgent || got.editor != EditorVersion || got.plugin != EditorPluginVersion || got.integration != IntegrationID {
		t.Fatalf("compatibility headers = UA %q editor %q plugin %q integration %q", got.userAgent, got.editor, got.plugin, got.integration)
	}
}

func TestExchangeCopilotTokenRejectsPermanentStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := rawResponseServer(t, status, `{"message":"Bad credentials"}`)
			if _, err := ExchangeCopilotToken(context.Background(), server.Client(), newTestConfig(server), "github-access"); !errors.Is(err, ErrCopilotTokenRejected) {
				t.Fatalf("ExchangeCopilotToken() error = %v, want ErrCopilotTokenRejected", err)
			}
		})
	}
}

func TestExchangeCopilotTokenReportsTransientFailure(t *testing.T) {
	server := rawResponseServer(t, http.StatusBadGateway, `{}`)
	_, err := ExchangeCopilotToken(context.Background(), server.Client(), newTestConfig(server), "github-access")
	if err == nil {
		t.Fatal("ExchangeCopilotToken() error = nil, want a transient failure")
	}
	if errors.Is(err, ErrCopilotTokenRejected) || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("transient failure was classified as permanent: %v", err)
	}
}

func TestExchangeCopilotTokenRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{"invalid json": "{", "missing token": `{"expires_at":1}`, "missing expiry": `{"token":"t"}`} {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, body)
			if _, err := ExchangeCopilotToken(context.Background(), server.Client(), newTestConfig(server), "github-access"); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("ExchangeCopilotToken() error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestRefreshTokenRotatesAndComputesExpiry(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	script := &tokenEndpointScript{}
	server := script.server(t)
	tokens, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old")
	if err != nil {
		t.Fatalf("RefreshToken() error = %v", err)
	}
	if tokens.AccessToken != "access-1" || tokens.RefreshToken != "refresh-1" {
		t.Fatalf("RefreshToken() did not return the rotated pair")
	}
	if !tokens.AccessExpiry.Equal(at.Add(8 * time.Hour)) {
		t.Fatalf("access expiry = %v, want %v", tokens.AccessExpiry, at.Add(8*time.Hour))
	}
	if !tokens.RefreshExpiry.Equal(at.Add(15897600 * time.Second)) {
		t.Fatalf("refresh expiry = %v, want %v", tokens.RefreshExpiry, at.Add(15897600*time.Second))
	}
	form := script.lastForm()
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "refresh-old" {
		t.Fatalf("refresh form carried grant_type = %q refresh_token = %q", form.Get("grant_type"), form.Get("refresh_token"))
	}
	if form.Get("client_id") != ClientID {
		t.Fatalf("refresh form client_id = %q, want %q", form.Get("client_id"), ClientID)
	}
}

func TestRefreshTokenRejectsBadRefreshToken(t *testing.T) {
	script := &tokenEndpointScript{codes: []string{"bad_refresh_token"}}
	server := script.server(t)
	if _, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old"); !errors.Is(err, ErrBadRefreshToken) {
		t.Fatalf("RefreshToken() error = %v, want ErrBadRefreshToken", err)
	}
}

func TestRefreshTokenRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{"invalid json": "{", "missing access token": "{}"} {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, body)
			if _, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old"); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("RefreshToken() error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

// TestLifetimesBeyondDurationAreMalformed pins the time.Duration boundary for
// every lifetime GitHub sends: the longest representable one still yields a
// future expiry, and one second more is a malformed response instead of
// wrapping into an expiry before the request.
func TestLifetimesBeyondDurationAreMalformed(t *testing.T) {
	at := time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)
	swapNow(t, at)
	maxSeconds := math.MaxInt64 / int64(time.Second)
	cases := map[string]struct {
		body   func(seconds int64) string
		expiry func(TokenSet) time.Time
	}{
		"expires_in": {
			body: func(seconds int64) string {
				return fmt.Sprintf(`{"access_token":"at","expires_in":%d}`, seconds)
			},
			expiry: func(tokens TokenSet) time.Time { return tokens.AccessExpiry },
		},
		"refresh_token_expires_in": {
			body: func(seconds int64) string {
				return fmt.Sprintf(`{"access_token":"at","refresh_token":"rt","refresh_token_expires_in":%d}`, seconds)
			},
			expiry: func(tokens TokenSet) time.Time { return tokens.RefreshExpiry },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, tc.body(maxSeconds))
			tokens, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old")
			if err != nil {
				t.Fatalf("RefreshToken() at the boundary error = %v", err)
			}
			if got := tc.expiry(tokens); !got.After(at) {
				t.Fatalf("RefreshToken() at the boundary expiry = %v, want after %v", got, at)
			}

			server = rawResponseServer(t, 0, tc.body(maxSeconds+1))
			if _, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old"); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("RefreshToken() past the boundary error = %v, want ErrMalformedResponse", err)
			}
			if _, _, _, err := PollDeviceOnce(context.Background(), server.Client(), newTestConfig(server), testDeviceCode(at)); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("PollDeviceOnce() past the boundary error = %v, want ErrMalformedResponse", err)
			}
		})
	}
	t.Run("device code expires_in", func(t *testing.T) {
		body := func(seconds int64) string {
			return fmt.Sprintf(`{"device_code":"d","user_code":"u","verification_uri":"https://github.com/login/device","expires_in":%d}`, seconds)
		}
		server := rawResponseServer(t, 0, body(maxSeconds))
		dc, err := RequestDeviceCode(context.Background(), server.Client(), newTestConfig(server))
		if err != nil {
			t.Fatalf("RequestDeviceCode() at the boundary error = %v", err)
		}
		if !dc.Expiry.After(at) {
			t.Fatalf("RequestDeviceCode() at the boundary expiry = %v, want after %v", dc.Expiry, at)
		}

		server = rawResponseServer(t, 0, body(maxSeconds+1))
		if _, err := RequestDeviceCode(context.Background(), server.Client(), newTestConfig(server)); !errors.Is(err, ErrMalformedResponse) {
			t.Fatalf("RequestDeviceCode() past the boundary error = %v, want ErrMalformedResponse", err)
		}
	})
}

func TestRefreshTokenReportsTransientServerFailure(t *testing.T) {
	script := &tokenEndpointScript{statuses: []int{http.StatusBadGateway}}
	server := script.server(t)
	_, err := RefreshToken(context.Background(), server.Client(), newTestConfig(server), "refresh-old")
	if err == nil {
		t.Fatal("RefreshToken() error = nil, want a transient failure")
	}
	if errors.Is(err, ErrBadRefreshToken) || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("transient failure was classified as permanent: %v", err)
	}
}

func TestFetchAccountReturnsStableIdentity(t *testing.T) {
	var mu sync.Mutex
	var authorization, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Errorf("request = %s %s, want GET /user", r.Method, r.URL.Path)
		}
		mu.Lock()
		authorization = r.Header.Get("Authorization")
		accept = r.Header.Get("Accept")
		mu.Unlock()
		writeJSON(w, map[string]any{"id": 4242, "login": "octocat", "email": "octocat@example.com"})
	}))
	t.Cleanup(server.Close)

	account, err := FetchAccount(context.Background(), server.Client(), newTestConfig(server), "access-1")
	if err != nil {
		t.Fatalf("FetchAccount() error = %v", err)
	}
	if account.ID != "4242" || account.Login != "octocat" || account.Email != "octocat@example.com" {
		t.Fatalf("account = %+v, want id 4242, login octocat, email octocat@example.com", account)
	}
	mu.Lock()
	defer mu.Unlock()
	if authorization != "Bearer access-1" {
		t.Error("account lookup did not send the access token as a bearer credential")
	}
	if accept != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", accept)
	}
}

func TestFetchAccountAcceptsNullEmail(t *testing.T) {
	server := rawResponseServer(t, 0, `{"id":4242,"login":"octocat","email":null}`)
	account, err := FetchAccount(context.Background(), server.Client(), newTestConfig(server), "access-1")
	if err != nil {
		t.Fatalf("FetchAccount() error = %v", err)
	}
	if account.Email != "" {
		t.Fatalf("email = %q, want empty for a null email", account.Email)
	}
}

func TestFetchAccountRejectsUnauthorized(t *testing.T) {
	server := rawResponseServer(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`)
	if _, err := FetchAccount(context.Background(), server.Client(), newTestConfig(server), "access-1"); err == nil {
		t.Fatal("FetchAccount() error = nil, want an authorization failure")
	}
}

func TestFetchAccountRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json": "{\"id\":",
		"missing id":   `{"login":"octocat"}`,
		"null id":      `{"id":null,"login":"octocat"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := rawResponseServer(t, 0, body)
			if _, err := FetchAccount(context.Background(), server.Client(), newTestConfig(server), "access-1"); !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("FetchAccount() error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestFetchAccountRequiresAccessToken(t *testing.T) {
	server := rawResponseServer(t, 0, `{"id":4242,"login":"octocat"}`)
	if _, err := FetchAccount(context.Background(), server.Client(), newTestConfig(server), " "); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("FetchAccount() error = %v, want ErrMalformedResponse", err)
	}
}
