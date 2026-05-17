package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"primeradiant.com/serf/internal/appwire"
	authopenai "primeradiant.com/serf/internal/auth/openai"
	"primeradiant.com/serf/internal/credentials"
)

type hubAuthController struct {
	stateDir     string
	authEnv      map[string]string
	creds        *credentials.Store
	cfg          authopenai.Config
	client       *http.Client
	now          func() time.Time
	exchangeCode func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error)

	mu    sync.Mutex
	flows map[string]hubAuthFlow
}

type hubAuthFlow struct {
	Provider     string
	State        string
	CodeVerifier string
	RedirectURI  string
}

func newHubAuthController(launchEnv ...map[string]string) *hubAuthController {
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	authEnv := effectiveHubAuthEnv(nil)
	if len(launchEnv) > 0 {
		authEnv = effectiveHubAuthEnv(launchEnv[0])
	}
	stateDir := openAIStateDirFromEnv(authEnv)
	credsPath := filepath.Join(filepath.Dir(stateDir), "credentials.toml")
	store, _ := credentials.LoadStore(credsPath)
	return &hubAuthController{
		stateDir:     stateDir,
		authEnv:      authEnv,
		creds:        store,
		cfg:          cfg,
		client:       client,
		now:          time.Now,
		exchangeCode: authopenai.ExchangeCode,
		flows:        map[string]hubAuthFlow{},
	}
}

// newHubAuthControllerWithStore creates a controller backed by an explicit credentials store.
// The OpenAI OAuth state directory is resolved from the process environment (XDG_STATE_HOME / HOME),
// matching the behaviour of the default constructor but without launch-env overrides.
func newHubAuthControllerWithStore(_ string, store *credentials.Store) *hubAuthController {
	authEnv := effectiveHubAuthEnv(nil)
	cfg := authopenai.DefaultConfig()
	client := &http.Client{Timeout: cfg.HTTPTimeout}
	stateDir := openAIStateDirFromEnv(authEnv)
	return &hubAuthController{
		stateDir:     stateDir,
		authEnv:      authEnv,
		creds:        store,
		cfg:          cfg,
		client:       client,
		now:          time.Now,
		exchangeCode: authopenai.ExchangeCode,
		flows:        map[string]hubAuthFlow{},
	}
}

func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider == "openai" {
		resp, err := c.openAIStatus()
		if err != nil {
			return resp, err
		}
		resp.AuthModes = []string{"apiKey", "oauth"}
		return resp, nil
	}
	modes := credentialAuthModes(provider)
	if modes == nil {
		return appwire.AuthStatusResponse{Provider: provider, Supported: false, ActiveSource: string(credentials.SourceAbsent)}, nil
	}
	v, src := c.creds.Get(provider)
	hasFile, envVar := c.creds.Layers(provider)
	return appwire.AuthStatusResponse{
		Provider:      provider,
		Supported:     true,
		SignedIn:      v != "",
		ActiveSource:  string(src),
		AuthModes:     modes,
		HasStoredFile: hasFile,
		EnvVar:        envVar,
	}, nil
}

func credentialAuthModes(provider string) []string {
	known := map[string][]string{
		"anthropic":            {"apiKey"},
		"google":               {"apiKey"},
		"gemini":               {"apiKey"},
		"minimax":              {"apiKey"},
		"openrouter":           {"apiKey"},
		"openrouter-anthropic": {"apiKey"},
		"kimi":                 {"apiKey"},
		"glm":                  {"apiKey"},
		"openai-compatible":    {"apiKey"},
		"ollama":               {"none"},
	}
	return known[provider]
}

func (c *hubAuthController) LoginStart(params appwire.AuthLoginStartParams) (appwire.AuthLoginStartResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		return appwire.AuthLoginStartResponse{}, appwire.InvalidParams(fmt.Sprintf("auth is not supported for provider %q", provider))
	}

	state, err := authopenai.GenerateState()
	if err != nil {
		return appwire.AuthLoginStartResponse{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, challenge, err := authopenai.GeneratePKCE()
	if err != nil {
		return appwire.AuthLoginStartResponse{}, fmt.Errorf("generate PKCE values: %w", err)
	}
	redirectURI := c.config().RedirectURI(authopenai.DefaultCallbackPort)
	rawURL, err := c.config().AuthorizeURL(authopenai.AuthorizeURLOptions{
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: challenge,
		OpenBrowser:   false,
	})
	if err != nil {
		return appwire.AuthLoginStartResponse{}, err
	}

	c.mu.Lock()
	if c.flows == nil {
		c.flows = map[string]hubAuthFlow{}
	}
	c.flows[state] = hubAuthFlow{
		Provider:     provider,
		State:        state,
		CodeVerifier: verifier,
		RedirectURI:  redirectURI,
	}
	c.mu.Unlock()

	return appwire.AuthLoginStartResponse{Provider: provider, FlowID: state, URL: rawURL}, nil
}

func (c *hubAuthController) LoginComplete(ctx context.Context, params appwire.AuthLoginCompleteParams) (appwire.AuthLoginCompleteResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams(fmt.Sprintf("auth is not supported for provider %q", provider))
	}
	flowID := strings.TrimSpace(params.FlowID)
	if flowID == "" {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login flow is required")
	}

	c.mu.Lock()
	flow, ok := c.flows[flowID]
	c.mu.Unlock()
	if !ok {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login flow not found")
	}
	if flow.Provider != provider {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams("auth login provider does not match flow")
	}

	code, returnedState, err := authopenai.ParseRedirectURL(params.RedirectURL)
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams(err.Error())
	}
	if err := authopenai.ValidateState(flow.State, returnedState); err != nil {
		return appwire.AuthLoginCompleteResponse{}, appwire.InvalidParams(err.Error())
	}

	tokens, err := c.exchangeCode(ctx, c.client, c.config(), authopenai.TokenExchangeRequest{
		Code:         code,
		RedirectURI:  flow.RedirectURI,
		CodeVerifier: flow.CodeVerifier,
	})
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	record := c.authRecordFromTokens(tokens)
	if claims, err := authopenai.ParseIDTokenClaims(tokens.IDToken); err == nil {
		record.Email = firstNonEmpty(claims.Email, record.Email)
		record.AccountID = firstNonEmpty(claims.AccountID, record.AccountID)
		record.WorkspaceID = firstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
	}
	if err := authopenai.SaveAuth(c.stateDir, record); err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}

	c.mu.Lock()
	delete(c.flows, flowID)
	c.mu.Unlock()

	status, err := c.openAIStatus()
	if err != nil {
		return appwire.AuthLoginCompleteResponse{}, err
	}
	return appwire.AuthLoginCompleteResponse{Status: status}, nil
}

func (c *hubAuthController) Logout(params appwire.AuthLogoutParams) (appwire.AuthLogoutResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		if err := c.creds.Clear(provider); err != nil {
			return appwire.AuthLogoutResponse{}, err
		}
		status, _ := c.Status(appwire.AuthStatusParams{Provider: provider})
		return appwire.AuthLogoutResponse{Removed: true, Status: status}, nil
	}
	removed, err := authopenai.DeleteAuth(c.stateDir)
	if err != nil {
		return appwire.AuthLogoutResponse{}, err
	}
	status, err := c.openAIStatus()
	if err != nil {
		return appwire.AuthLogoutResponse{}, err
	}
	return appwire.AuthLogoutResponse{Removed: removed, Status: status}, nil
}

func (c *hubAuthController) List(_ appwire.EmptyParams) (appwire.AuthListResponse, error) {
	out := appwire.AuthListResponse{}
	openaiResp, err := c.Status(appwire.AuthStatusParams{Provider: "openai"})
	if err == nil {
		out.Providers = append(out.Providers, openaiResp)
	}
	for _, p := range c.creds.List() {
		if p.Name == "openai" {
			continue
		}
		hasFile, envVar := c.creds.Layers(p.Name)
		out.Providers = append(out.Providers, appwire.AuthStatusResponse{
			Provider:      p.Name,
			Supported:     true,
			SignedIn:      p.Source == credentials.SourceFile || p.Source == credentials.SourceEnv,
			ActiveSource:  string(p.Source),
			AuthModes:     p.AuthModes,
			HasStoredFile: hasFile,
			EnvVar:        envVar,
		})
	}
	return out, nil
}

func (c *hubAuthController) ApiKeySet(params appwire.AuthApiKeySetParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider == "openai" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("openai api keys must be configured via env or hub.env; use serf/auth/login/start for OAuth")
	}
	if strings.TrimSpace(params.Value) == "" {
		return appwire.AuthStatusResponse{}, appwire.InvalidParams("value is required")
	}
	if err := c.creds.Set(provider, params.Value); err != nil {
		return appwire.AuthStatusResponse{}, err
	}
	return c.Status(appwire.AuthStatusParams{Provider: provider})
}

func (c *hubAuthController) openAIStatus() (appwire.AuthStatusResponse, error) {
	active := authopenai.AuthStatus{Source: authopenai.AuthSourceSignedOut}
	if strings.TrimSpace(c.authEnv["OPENAI_API_KEY"]) != "" {
		active = authopenai.AuthStatus{
			SignedIn: true,
			Source:   authopenai.AuthSourceEnv,
		}
	} else if record, err := authopenai.LoadAuth(c.stateDir); err == nil {
		active = openAIStatusFromRecord(c.now(), record)
	} else if !errors.Is(err, authopenai.ErrAuthNotFound) {
		return appwire.AuthStatusResponse{}, err
	}

	status := appwire.AuthStatusResponse{
		Provider:     "openai",
		Supported:    true,
		SignedIn:     active.SignedIn,
		ActiveSource: active.Source,
		Email:        active.Email,
		AccountID:    active.AccountID,
		WorkspaceID:  active.WorkspaceID,
		NeedsRefresh: active.NeedsRefresh,
		NeedsLogin:   active.NeedsLogin,
	}

	record, err := authopenai.LoadAuth(c.stateDir)
	switch {
	case err == nil:
		status.HasStoredOAuth = true
		status.StoredEmail = record.Email
		if status.ActiveSource == authopenai.AuthSourceOAuth {
			status.Email = firstNonEmpty(status.Email, record.Email)
			status.AccountID = firstNonEmpty(status.AccountID, record.AccountID)
			status.WorkspaceID = firstNonEmpty(status.WorkspaceID, record.WorkspaceID)
		}
	case errors.Is(err, authopenai.ErrAuthNotFound):
		return status, nil
	default:
		return appwire.AuthStatusResponse{}, err
	}

	return status, nil
}

func effectiveHubAuthEnv(launchEnv map[string]string) map[string]string {
	out := envToMap(os.Environ())
	for key, value := range launchEnv {
		out[key] = value
	}
	return out
}

func openAIStateDirFromEnv(env map[string]string) string {
	if stateHome := strings.TrimSpace(env["XDG_STATE_HOME"]); stateHome != "" {
		return filepath.Join(stateHome, "serf")
	}
	if home := strings.TrimSpace(env["HOME"]); home != "" {
		return filepath.Join(home, ".local", "state", "serf")
	}
	return authopenai.DefaultStateDirWithStateHome("")
}

func openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord) authopenai.AuthStatus {
	needsLogin := !record.Expiry.IsZero() && !record.Expiry.After(now)
	return authopenai.AuthStatus{
		SignedIn:     !needsLogin,
		Source:       record.Source,
		Email:        record.Email,
		AccountID:    record.AccountID,
		WorkspaceID:  record.WorkspaceID,
		Expiry:       record.Expiry,
		NeedsRefresh: openAIRecordNeedsRefresh(now, record) && !needsLogin,
		NeedsLogin:   needsLogin,
	}
}

func openAIRecordNeedsRefresh(now time.Time, record authopenai.AuthRecord) bool {
	if record.Expiry.IsZero() {
		return false
	}
	return !record.Expiry.After(now.Add(5 * time.Minute))
}

func (c *hubAuthController) authRecordFromTokens(tokens authopenai.TokenSet) authopenai.AuthRecord {
	return authopenai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		ObtainedAt:   c.now(),
		TokenType:    firstNonEmpty(tokens.TokenType, "Bearer"),
		Scope:        tokens.Scope,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		Expiry:       tokens.Expiry,
	}
}

func (c *hubAuthController) config() authopenai.Config {
	if strings.TrimSpace(c.cfg.IssuerBaseURL) == "" {
		return authopenai.DefaultConfig()
	}
	return c.cfg
}

func normalizeAuthProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return "openai"
	}
	return provider
}
