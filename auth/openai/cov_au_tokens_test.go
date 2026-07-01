package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExchangeCodeCreatesClientWhenNil covers the nil-client arm of
// exchangeTokenRequest: with no client supplied, one is built from the config
// timeout and used for the real HTTP round-trip against a local server.
func TestExchangeCodeCreatesClientWhenNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, tokenEndpointResponse{
			AccessToken:  "access-nil-client",
			RefreshToken: "refresh-nil-client",
			TokenType:    "Bearer",
			Scope:        "openid",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	tokens, err := ExchangeCode(context.Background(), nil, cfg, TokenExchangeRequest{
		Code:         "code",
		RedirectURI:  "http://localhost/cb",
		CodeVerifier: "verifier",
	})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if tokens.AccessToken != "access-nil-client" {
		t.Fatalf("AccessToken = %q, want access-nil-client", tokens.AccessToken)
	}
	if tokens.Expiry.IsZero() {
		t.Fatal("Expiry = zero, want computed from expires_in")
	}
}

// TestExchangeCodeErrorPayloadWithoutJSON covers compactTokenError's arm where
// the error body is not decodable JSON, so only the status is reported.
func TestExchangeCodeErrorPayloadWithoutJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.IssuerBaseURL = srv.URL

	_, err := ExchangeCode(context.Background(), srv.Client(), cfg, TokenExchangeRequest{Code: "c"})
	if err == nil {
		t.Fatal("ExchangeCode() error = nil, want status error")
	}
	if want := "status 400"; !strings.Contains(err.Error(), want) {
		t.Fatalf("ExchangeCode() error = %v, want to contain %q", err, want)
	}
}
