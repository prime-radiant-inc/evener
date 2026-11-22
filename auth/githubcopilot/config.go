package githubcopilot

import (
	"net/http"
	"strings"
	"time"
)

const (
	// ClientID is the public Pi-compatible Copilot client identity Jesse
	// approved for the temporary direct-provider path. The device flow needs no
	// client secret, and Evener stores none.
	ClientID = "Iv1.b507a08c87ecfe98"
	// PiUserAgent is Pi's public Copilot client User-Agent. It is needed only for
	// device-flow and session-exchange compatibility; model requests use
	// Evener's own User-Agent plus SetCopilotCompatibilityHeaders.
	PiUserAgent = "GitHubCopilotChat/0.35.0"
	// EditorVersion, EditorPluginVersion, and IntegrationID are the live-proven
	// VS Code compatibility identity headers for session exchange and model
	// requests.
	EditorVersion       = "vscode/1.107.0"
	EditorPluginVersion = "copilot-chat/0.35.0"
	IntegrationID       = "vscode-chat"
	// AuthSourceOAuth is derived from AuthScheme; the record stores no source.
	AuthSourceOAuth     = "oauth"
	AuthSourceSignedOut = "signed-out"
	defaultGitHubURL    = "https://github.com"
	defaultAPIURL       = "https://api.github.com"
	refreshSkew         = 5 * time.Minute
	deviceInterval      = 5 * time.Second
	deviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	copilotSessionPath  = "/copilot_internal/v2/token"
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

// DefaultConfig returns the Pi-compatible identity and github.com endpoints.
func DefaultConfig() Config {
	return Config{ClientID: ClientID, Scopes: []string{"read:user"},
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

// SetCopilotCompatibilityHeaders adds the live-proven VS Code compatibility
// identity headers to a Copilot request. It never sets User-Agent: device and
// exchange requests use PiUserAgent explicitly, model requests keep Evener's.
func SetCopilotCompatibilityHeaders(header http.Header) {
	header.Set("Editor-Version", EditorVersion)
	header.Set("Editor-Plugin-Version", EditorPluginVersion)
	header.Set("Copilot-Integration-Id", IntegrationID)
}

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
