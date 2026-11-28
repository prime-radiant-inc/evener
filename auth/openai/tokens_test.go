package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExchangeCodeSuccess(t *testing.T) {
	var capturedMethod string
	var capturedContentType string
	var capturedValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedContentType = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		capturedValues = r.PostForm

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenEndpointResponse{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      "id-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	resp, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{
		Code:         "auth-code",
		RedirectURI:  "http://localhost:1455/auth/callback",
		CodeVerifier: "verifier-123",
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", capturedMethod)
	}
	if capturedContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q, want application/x-www-form-urlencoded", capturedContentType)
	}
	if got := capturedValues.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("grant_type = %q, want authorization_code", got)
	}
	if got := capturedValues.Get("code"); got != "auth-code" {
		t.Fatalf("code = %q, want auth-code", got)
	}
	if got := capturedValues.Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	if got := capturedValues.Get("client_id"); got != cfg.ClientID {
		t.Fatalf("client_id = %q, want %q", got, cfg.ClientID)
	}
	if got := capturedValues.Get("code_verifier"); got != "verifier-123" {
		t.Fatalf("code_verifier = %q, want verifier-123", got)
	}
	if resp.AccessToken != "access-token" || resp.RefreshToken != "refresh-token" || resp.IDToken != "id-token" {
		t.Fatalf("unexpected token response: %+v", resp)
	}
}

func TestRefreshTokenSuccess(t *testing.T) {
	var capturedValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		capturedValues = r.PostForm

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenEndpointResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			IDToken:      "new-id-token",
			TokenType:    "Bearer",
			Scope:        "openid profile email offline_access",
			ExpiresIn:    7200,
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	resp, err := RefreshToken(context.Background(), srv.Client(), cfg, RefreshTokenRequest{
		RefreshToken: "refresh-token",
	})
	if err != nil {
		t.Fatalf("RefreshToken() error = %v", err)
	}

	if got := capturedValues.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", got)
	}
	if got := capturedValues.Get("refresh_token"); got != "refresh-token" {
		t.Fatalf("refresh_token = %q, want refresh-token", got)
	}
	if resp.AccessToken != "new-access-token" || resp.RefreshToken != "new-refresh-token" {
		t.Fatalf("unexpected token response: %+v", resp)
	}
}

func TestTokenEndpointErrorIsCompact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant","error_description":"bad code"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	_, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{
		Code:         "bad-code",
		RedirectURI:  "http://localhost:1455/auth/callback",
		CodeVerifier: "verifier-123",
	})
	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want compact token endpoint error")
	}
	message := err.Error()
	if !strings.Contains(message, "400") || !strings.Contains(message, "invalid_grant") || !strings.Contains(message, "bad code") {
		t.Fatalf("error = %q, want compact status/code/description", message)
	}
}

func TestTokenEndpointInvalidJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	_, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{
		Code:         "auth-code",
		RedirectURI:  "http://localhost:1455/auth/callback",
		CodeVerifier: "verifier-123",
	})
	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want JSON decode error")
	}
}

func TestTokenResponseExpiryTime(t *testing.T) {
	before := time.Now()
	resp := tokenEndpointResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    60,
	}

	tokens, err := resp.intoTokenSet(before)
	if err != nil {
		t.Fatalf("intoTokenSet() error = %v", err)
	}
	want := before.Add(60 * time.Second)
	if !tokens.Expiry.Equal(want) {
		t.Fatalf("Expiry = %s, want %s", tokens.Expiry, want)
	}
}

// TestTokenEndpointRejectsExpiresInBeyondDuration pins the time.Duration
// boundary: the longest representable lifetime still yields a future expiry,
// and one second more is refused instead of wrapping into an expiry before the
// request.
func TestTokenEndpointRejectsExpiresInBeyondDuration(t *testing.T) {
	maxSeconds := math.MaxInt64 / int64(time.Second)
	for name, tc := range map[string]struct {
		expiresIn int64
		wantErr   bool
	}{
		"largest representable": {maxSeconds, false},
		"one second past":       {maxSeconds + 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"access_token":"at","refresh_token":"rt","token_type":"Bearer","expires_in":%d}`, tc.expiresIn)
			}))
			defer srv.Close()

			cfg := DefaultConfig()
			cfg.IssuerBaseURL = srv.URL
			before := time.Now()
			tokens, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{
				Code:         "auth-code",
				RedirectURI:  "http://localhost:1455/auth/callback",
				CodeVerifier: "verifier-123",
			})
			if tc.wantErr {
				if !errors.Is(err, errTokenLifetimeOutOfRange) {
					t.Fatalf("ExchangeCode() error = %v, want errTokenLifetimeOutOfRange", err)
				}
				if tokens != (TokenSet{}) {
					t.Fatalf("ExchangeCode() returned tokens alongside error %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExchangeCode() error = %v", err)
			}
			if !tokens.Expiry.After(before) {
				t.Fatalf("Expiry = %s, want after request time %s", tokens.Expiry, before)
			}
		})
	}
}
