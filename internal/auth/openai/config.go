package openai

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	IssuerBaseURL       = "https://auth.openai.com"
	ClientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectPath        = "/auth/callback"
	DefaultCallbackPort = 1455
)

var defaultScopes = []string{
	"openid",
	"profile",
	"email",
	"offline_access",
}

// Config contains the stable OpenAI OAuth settings owned by Serf.
type Config struct {
	IssuerBaseURL   string
	ClientID        string
	Scopes          []string
	RedirectPath    string
	HTTPTimeout     time.Duration
	CallbackTimeout time.Duration
}

type AuthorizeURLOptions struct {
	RedirectURI   string
	State         string
	CodeChallenge string
	OpenBrowser   bool
}

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
		return "", fmt.Errorf("redirect URI is required")
	}
	if strings.TrimSpace(options.State) == "" {
		return "", fmt.Errorf("state is required")
	}
	if strings.TrimSpace(options.CodeChallenge) == "" {
		return "", fmt.Errorf("PKCE code challenge is required")
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
