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
)

type hubAuthController struct {
	stateDir     string
	authEnv      map[string]string
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
	return &hubAuthController{
		stateDir:     openAIStateDirFromEnv(authEnv),
		authEnv:      authEnv,
		cfg:          cfg,
		client:       client,
		now:          time.Now,
		exchangeCode: authopenai.ExchangeCode,
		flows:        map[string]hubAuthFlow{},
	}
}

func (c *hubAuthController) Status(params appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
	provider := normalizeAuthProvider(params.Provider)
	if provider != "openai" {
		return appwire.AuthStatusResponse{Provider: provider, ActiveSource: authopenai.AuthSourceSignedOut}, nil
	}
	return c.openAIStatus()
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
		return appwire.AuthLogoutResponse{}, appwire.InvalidParams(fmt.Sprintf("auth is not supported for provider %q", provider))
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
