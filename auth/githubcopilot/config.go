package githubcopilot

import (
	"net/http"
	"strings"
	"time"
)

const (
	// ClientID is the public identity of Evener's GitHub OAuth App. The device
	// flow needs no client secret, and Evener stores none.
	ClientID = "Ov23li2maTXwKrULBNZw"
	// AuthSourceOAuth is derived from AuthScheme; the record stores no source.
	AuthSourceOAuth     = "oauth"
	AuthSourceSignedOut = "signed-out"
	defaultGitHubURL    = "https://github.com"
	defaultAPIURL       = "https://api.github.com"
	defaultScope        = "read:user offline_access"
	refreshSkew         = 5 * time.Minute
	deviceInterval      = 5 * time.Second
	deviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
)

// Config holds Evener's GitHub OAuth identity and endpoints. Zero values use
// DefaultConfig; tests point the base URLs at a scripted server.
type Config struct {
	ClientID      string
	Scopes        []string
	GitHubBaseURL string
	APIBaseURL    string
	HTTPTimeout   time.Duration
}

// DefaultConfig returns the public OAuth App identity and github.com endpoints.
func DefaultConfig() Config {
	return Config{ClientID: ClientID, Scopes: []string{"read:user", "offline_access"},
		GitHubBaseURL: defaultGitHubURL, APIBaseURL: defaultAPIURL, HTTPTimeout: 10 * time.Second}
}

func mergeConfigDefaults(cfg Config) Config {
	defaults := DefaultConfig()
	defaults.ClientID = firstNonEmpty(cfg.ClientID, defaults.ClientID)
	if len(cfg.Scopes) > 0 {
		defaults.Scopes = cfg.Scopes
	}
	defaults.GitHubBaseURL = firstNonEmpty(cfg.GitHubBaseURL, defaults.GitHubBaseURL)
	defaults.APIBaseURL = firstNonEmpty(cfg.APIBaseURL, defaults.APIBaseURL)
	if cfg.HTTPTimeout != 0 {
		defaults.HTTPTimeout = cfg.HTTPTimeout
	}
	return defaults
}

func (c Config) scope() string { return strings.Join(c.Scopes, " ") }

func httpClient(client *http.Client, cfg Config) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: cfg.HTTPTimeout}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
