package openai

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// IssuerBaseURL is the OpenAI OAuth issuer. The authorize and token
	// endpoints are derived from it (e.g. IssuerBaseURL + "/oauth/authorize").
	IssuerBaseURL = "https://auth.openai.com"
	// ClientID is the public OAuth client identifier Serf registers as with
	// OpenAI. It is sent on the authorize, token-exchange, and refresh requests.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// RedirectPath is the path component of the localhost redirect URI that the
	// callback server listens on and that is registered with OpenAI.
	RedirectPath = "/auth/callback"
	// DefaultCallbackPort is the localhost port the browser-login callback
	// server tries first. If it is unavailable, the server falls back to
	// FallbackCallbackPort.
	DefaultCallbackPort = 1455
)

var defaultScopes = []string{
	"openid",
	"profile",
	"email",
	"offline_access",
}

const (
	defaultOriginator = "pi"
	// FallbackCallbackPort is the localhost port the browser-login callback
	// server binds to when DefaultCallbackPort is already in use.
	FallbackCallbackPort = 1457
)

// Config contains the stable OpenAI OAuth settings owned by Serf.
type Config struct {
	IssuerBaseURL   string
	ClientID        string
	Scopes          []string
	RedirectPath    string
	HTTPTimeout     time.Duration
	CallbackTimeout time.Duration
}

// AuthorizeURLOptions are the per-login parameters Config.AuthorizeURL needs to
// build the OpenAI authorization URL. RedirectURI, State, and CodeChallenge are
// required.
type AuthorizeURLOptions struct {
	// RedirectURI is the localhost callback URI OpenAI redirects back to.
	RedirectURI string
	// State is the random anti-CSRF token echoed back on the callback and
	// checked with ValidateState.
	State string
	// CodeChallenge is the PKCE S256 code challenge derived from the verifier
	// returned by GeneratePKCE.
	CodeChallenge string
	// OpenBrowser is recorded on the options but does not affect the URL; the
	// caller decides whether to open a browser.
	OpenBrowser bool
}

// DefaultConfig returns the standard Serf-owned OpenAI OAuth configuration: the
// public issuer, client ID, redirect path, default scopes, and the HTTP and
// callback timeouts used by NewService when the caller does not override them.
func DefaultConfig() Config {
	return Config{
		IssuerBaseURL:   IssuerBaseURL,
		ClientID:        ClientID,
		Scopes:          append([]string(nil), defaultScopes...),
		RedirectPath:    RedirectPath,
		HTTPTimeout:     10 * time.Second,
		CallbackTimeout: 2 * time.Minute,
	}
}

func (c Config) RedirectURI(port int) string {
	return (&url.URL{
		Scheme: "http",
		Host:   "localhost:" + strconv.Itoa(port),
		Path:   c.redirectPath(),
	}).String()
}

func (c Config) AuthorizeURL(options AuthorizeURLOptions) (string, error) {
	if strings.TrimSpace(options.RedirectURI) == "" {
		return "", errors.New("redirect URI is required")
	}
	if strings.TrimSpace(options.State) == "" {
		return "", errors.New("state is required")
	}
	if strings.TrimSpace(options.CodeChallenge) == "" {
		return "", errors.New("PKCE code challenge is required")
	}

	issuer, err := url.Parse(strings.TrimRight(c.issuerBaseURL(), "/"))
	if err != nil {
		return "", fmt.Errorf("parse issuer base URL: %w", err)
	}
	issuer.Path = strings.TrimRight(issuer.Path, "/") + "/oauth/authorize"

	query := issuer.Query()
	query.Set("response_type", "code")
	query.Set("client_id", c.clientID())
	query.Set("redirect_uri", options.RedirectURI)
	query.Set("scope", strings.Join(c.scopes(), " "))
	query.Set("code_challenge", options.CodeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", defaultOriginator)
	query.Set("state", options.State)
	issuer.RawQuery = query.Encode()

	return issuer.String(), nil
}

func (c Config) issuerBaseURL() string {
	if c.IssuerBaseURL == "" {
		return IssuerBaseURL
	}
	return c.IssuerBaseURL
}

func (c Config) clientID() string {
	if c.ClientID == "" {
		return ClientID
	}
	return c.ClientID
}

func (c Config) redirectPath() string {
	if c.RedirectPath == "" {
		return RedirectPath
	}
	return c.RedirectPath
}

func (c Config) scopes() []string {
	if len(c.Scopes) == 0 {
		return defaultScopes
	}
	return c.Scopes
}
