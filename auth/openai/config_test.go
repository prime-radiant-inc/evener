package openai

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.IssuerBaseURL != "https://auth.openai.com" {
		t.Fatalf("IssuerBaseURL = %q, want %q", cfg.IssuerBaseURL, "https://auth.openai.com")
	}
	if cfg.ClientID == "" {
		t.Fatal("ClientID is empty")
	}
	if cfg.RedirectPath != "/auth/callback" {
		t.Fatalf("RedirectPath = %q, want %q", cfg.RedirectPath, "/auth/callback")
	}
	if cfg.HTTPTimeout <= 0 {
		t.Fatalf("HTTPTimeout = %s, want positive duration", cfg.HTTPTimeout)
	}
	if cfg.CallbackTimeout <= 0 {
		t.Fatalf("CallbackTimeout = %s, want positive duration", cfg.CallbackTimeout)
	}
	if cfg.HTTPTimeout > 30*time.Second {
		t.Fatalf("HTTPTimeout = %s, want a short network timeout", cfg.HTTPTimeout)
	}
	if cfg.CallbackTimeout < time.Minute {
		t.Fatalf("CallbackTimeout = %s, want enough time to finish browser login", cfg.CallbackTimeout)
	}

	wantScopes := []string{
		"openid",
		"profile",
		"email",
		"offline_access",
	}
	for _, want := range wantScopes {
		if !contains(cfg.Scopes, want) {
			t.Fatalf("Scopes = %v, missing %q", cfg.Scopes, want)
		}
	}
}

func TestAuthorizeURLContainsRequiredQueryParams(t *testing.T) {
	cfg := DefaultConfig()
	redirectURI := cfg.RedirectURI(1455)

	authURL, err := cfg.AuthorizeURL(AuthorizeURLOptions{
		RedirectURI:   redirectURI,
		State:         "state-123",
		CodeChallenge: "challenge-456",
		OpenBrowser:   true,
	})
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", authURL, err)
	}

	if parsed.Scheme != "https" || parsed.Host != "auth.openai.com" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("authorize endpoint = %s, want https://auth.openai.com/oauth/authorize", parsed.String())
	}

	query := parsed.Query()
	required := map[string]string{
		"response_type":              "code",
		"client_id":                  cfg.ClientID,
		"redirect_uri":               redirectURI,
		"scope":                      strings.Join(cfg.Scopes, " "),
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 "pi",
	}
	for key, want := range required {
		if got := query.Get(key); got != want {
			t.Fatalf("query[%s] = %q, want %q", key, got, want)
		}
	}
}

func TestAuthorizeURLUsesLocalhostCallbackRedirect(t *testing.T) {
	cfg := DefaultConfig()

	redirectURI := cfg.RedirectURI(17777)
	if redirectURI != "http://localhost:17777/auth/callback" {
		t.Fatalf("RedirectURI(17777) = %q, want localhost callback", redirectURI)
	}

	authURL, err := cfg.AuthorizeURL(AuthorizeURLOptions{
		RedirectURI:   redirectURI,
		State:         "state-123",
		CodeChallenge: "challenge-456",
	})
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", authURL, err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != redirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, redirectURI)
	}
}

func TestAuthorizeURLPropagatesStateAndPKCEChallenge(t *testing.T) {
	cfg := DefaultConfig()

	authURL, err := cfg.AuthorizeURL(AuthorizeURLOptions{
		RedirectURI:   cfg.RedirectURI(1455),
		State:         "csrf-state",
		CodeChallenge: "pkce-code-challenge",
	})
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", authURL, err)
	}
	query := parsed.Query()
	if got := query.Get("state"); got != "csrf-state" {
		t.Fatalf("state = %q, want %q", got, "csrf-state")
	}
	if got := query.Get("code_challenge"); got != "pkce-code-challenge" {
		t.Fatalf("code_challenge = %q, want %q", got, "pkce-code-challenge")
	}
}

func TestAuthorizeURLIgnoresBrowserOpenFlag(t *testing.T) {
	cfg := DefaultConfig()
	options := AuthorizeURLOptions{
		RedirectURI:   cfg.RedirectURI(1455),
		State:         "csrf-state",
		CodeChallenge: "pkce-code-challenge",
	}

	closedURL, err := cfg.AuthorizeURL(options)
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}

	options.OpenBrowser = true
	openURL, err := cfg.AuthorizeURL(options)
	if err != nil {
		t.Fatalf("AuthorizeURL returned error: %v", err)
	}

	if openURL != closedURL {
		t.Fatalf("AuthorizeURL changed when OpenBrowser changed:\nopen:   %s\nclosed: %s", openURL, closedURL)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
