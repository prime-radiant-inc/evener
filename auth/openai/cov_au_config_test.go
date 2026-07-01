package openai

import (
	"strings"
	"testing"
)

func TestAuthorizeURLRejectsMissingRequiredFields(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name string
		opts AuthorizeURLOptions
		want string
	}{
		{
			name: "missing redirect uri",
			opts: AuthorizeURLOptions{State: "s", CodeChallenge: "c"},
			want: "redirect URI is required",
		},
		{
			name: "missing state",
			opts: AuthorizeURLOptions{RedirectURI: "http://localhost/cb", CodeChallenge: "c"},
			want: "state is required",
		},
		{
			name: "missing code challenge",
			opts: AuthorizeURLOptions{RedirectURI: "http://localhost/cb", State: "s"},
			want: "PKCE code challenge is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cfg.AuthorizeURL(tt.opts)
			if err == nil {
				t.Fatalf("AuthorizeURL() error = nil, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("AuthorizeURL() error = %q, want to contain %q", err, tt.want)
			}
		})
	}
}

func TestAuthorizeURLRejectsUnparseableIssuer(t *testing.T) {
	cfg := Config{IssuerBaseURL: "http://\x7f-control-char"}
	_, err := cfg.AuthorizeURL(AuthorizeURLOptions{
		RedirectURI:   "http://localhost/cb",
		State:         "s",
		CodeChallenge: "c",
	})
	if err == nil {
		t.Fatal("AuthorizeURL() error = nil, want issuer parse failure")
	}
	if !strings.Contains(err.Error(), "parse issuer base URL") {
		t.Fatalf("AuthorizeURL() error = %v, want issuer parse failure", err)
	}
}

// TestConfigAccessorsFallBackToDefaults exercises the zero-value fallback arm of
// each unexported accessor.
func TestConfigAccessorsFallBackToDefaults(t *testing.T) {
	var zero Config

	if got := zero.issuerBaseURL(); got != IssuerBaseURL {
		t.Fatalf("issuerBaseURL() = %q, want %q", got, IssuerBaseURL)
	}
	if got := zero.clientID(); got != ClientID {
		t.Fatalf("clientID() = %q, want %q", got, ClientID)
	}
	if got := zero.redirectPath(); got != RedirectPath {
		t.Fatalf("redirectPath() = %q, want %q", got, RedirectPath)
	}
	if got := zero.scopes(); len(got) != len(defaultScopes) {
		t.Fatalf("scopes() = %v, want default scopes", got)
	}
	if got := zero.callbackTimeout(); got != DefaultConfig().CallbackTimeout {
		t.Fatalf("callbackTimeout() = %v, want %v", got, DefaultConfig().CallbackTimeout)
	}

	// The non-empty branches return the configured values verbatim.
	custom := Config{
		IssuerBaseURL: "https://issuer.test",
		ClientID:      "client-x",
		RedirectPath:  "/cb",
		Scopes:        []string{"openid"},
	}
	if got := custom.issuerBaseURL(); got != "https://issuer.test" {
		t.Fatalf("issuerBaseURL() = %q, want configured value", got)
	}
	if got := custom.clientID(); got != "client-x" {
		t.Fatalf("clientID() = %q, want configured value", got)
	}
	if got := custom.redirectPath(); got != "/cb" {
		t.Fatalf("redirectPath() = %q, want configured value", got)
	}
	if got := custom.scopes(); len(got) != 1 || got[0] != "openid" {
		t.Fatalf("scopes() = %v, want configured value", got)
	}
}
