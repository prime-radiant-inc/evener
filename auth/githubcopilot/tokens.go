package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrMalformedResponse marks a GitHub response that is not the documented
	// JSON shape; it never includes response bodies.
	ErrMalformedResponse = errors.New("malformed GitHub OAuth response")
	// ErrBadRefreshToken reports that GitHub permanently refused the refresh
	// token, so only a fresh login can recover the instance.
	ErrBadRefreshToken = errors.New("github refresh token rejected")
	// ErrCopilotTokenRejected reports that GitHub permanently refused the GitHub
	// access token during the Copilot session exchange.
	ErrCopilotTokenRejected = errors.New("github copilot session token rejected")
)

// CopilotSession is one exchanged Copilot session token and its absolute
// expiry.
type CopilotSession struct {
	Token  string
	Expiry time.Time
}

// TokenSet is the normalized result of a device authorization or refresh.
type TokenSet struct {
	AccessToken   string
	RefreshToken  string
	TokenType     string
	Scope         string
	AccessExpiry  time.Time
	RefreshExpiry time.Time
}

type tokenResponsePayload struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_token_expires_in"`
	Error            string `json:"error"`
}

// postFormJSON sends GitHub's form-encoded OAuth request and decodes its JSON.
// GitHub reports device and refresh failures with HTTP 200 and an "error"
// field, so callers classify the body rather than the status.
func postFormJSON(ctx context.Context, client *http.Client, cfg Config, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GitHubBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", PiUserAgent)
	resp, err := httpClient(client, cfg).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github OAuth endpoint %s returned status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformedResponse, err)
	}
	return nil
}

// maxLifetimeSeconds is the longest lifetime, in whole seconds, that a
// time.Duration can hold. A larger expires_in would wrap when converted and put
// the computed expiry in the past.
const maxLifetimeSeconds = int64(math.MaxInt64 / int64(time.Second))

// expiryAfter turns a positive lifetime in seconds into an absolute expiry. A
// lifetime too long for time.Duration is a malformed response rather than an
// immediately expired credential.
func expiryAfter(now time.Time, seconds int, field string) (time.Time, error) {
	if int64(seconds) > maxLifetimeSeconds {
		return time.Time{}, fmt.Errorf("%w: %s out of range", ErrMalformedResponse, field)
	}
	return now.Add(time.Duration(seconds) * time.Second), nil
}

func (p tokenResponsePayload) tokenSet(now time.Time) (TokenSet, error) {
	tokens := TokenSet{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken,
		TokenType: p.TokenType, Scope: p.Scope}
	if p.ExpiresIn > 0 {
		expiry, err := expiryAfter(now, p.ExpiresIn, "expires_in")
		if err != nil {
			return TokenSet{}, err
		}
		tokens.AccessExpiry = expiry
	}
	if p.RefreshExpiresIn > 0 {
		expiry, err := expiryAfter(now, p.RefreshExpiresIn, "refresh_token_expires_in")
		if err != nil {
			return TokenSet{}, err
		}
		tokens.RefreshExpiry = expiry
	}
	return tokens, nil
}

// validateTokenSet enforces the GitHub tokens a record needs. The GitHub access
// token is required and may be non-expiring. A refresh token and refresh expiry
// are either both present or both absent.
func validateTokenSet(tokens TokenSet) error {
	if strings.TrimSpace(tokens.AccessToken) == "" ||
		(strings.TrimSpace(tokens.RefreshToken) == "") != tokens.RefreshExpiry.IsZero() {
		return fmt.Errorf("%w: missing or inconsistent GitHub token fields", ErrMalformedResponse)
	}
	return nil
}

// ExchangeCopilotToken redeems a GitHub access token for the Copilot session
// token model requests use. A 401 or 403 is a permanent rejection of the GitHub
// token; transport and server failures stay ordinary errors so callers retry.
func ExchangeCopilotToken(ctx context.Context, client *http.Client, cfg Config, githubToken string) (CopilotSession, error) {
	cfg = mergeConfigDefaults(cfg)
	if strings.TrimSpace(githubToken) == "" {
		return CopilotSession{}, fmt.Errorf("%w: missing GitHub access token", ErrMalformedResponse)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIBaseURL+copilotSessionPath, nil)
	if err != nil {
		return CopilotSession{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("User-Agent", PiUserAgent)
	SetCopilotCompatibilityHeaders(req.Header)
	resp, err := httpClient(client, cfg).Do(req)
	if err != nil {
		return CopilotSession{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return CopilotSession{}, ErrCopilotTokenRejected
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return CopilotSession{}, fmt.Errorf("copilot session exchange returned status %d", resp.StatusCode)
	}
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return CopilotSession{}, fmt.Errorf("%w: %w", ErrMalformedResponse, err)
	}
	if strings.TrimSpace(payload.Token) == "" || payload.ExpiresAt <= 0 {
		return CopilotSession{}, fmt.Errorf("%w: missing session token or expiry", ErrMalformedResponse)
	}
	return CopilotSession{Token: payload.Token, Expiry: time.Unix(payload.ExpiresAt, 0)}, nil
}

// RefreshToken redeems refreshToken for a rotated pair. bad_refresh_token is
// permanent; transport and server failures stay ordinary errors so callers can
// retry without overwriting the record.
func RefreshToken(ctx context.Context, client *http.Client, cfg Config, refreshToken string) (TokenSet, error) {
	cfg = mergeConfigDefaults(cfg)
	if strings.TrimSpace(refreshToken) == "" {
		return TokenSet{}, ErrBadRefreshToken
	}
	var payload tokenResponsePayload
	err := postFormJSON(ctx, client, cfg, "/login/oauth/access_token", url.Values{
		"client_id": {cfg.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	}, &payload)
	if err != nil {
		return TokenSet{}, err
	}
	switch payload.Error {
	case "":
	case "bad_refresh_token":
		return TokenSet{}, ErrBadRefreshToken
	default:
		return TokenSet{}, fmt.Errorf("github token refresh failed: %s", payload.Error)
	}
	tokens, err := payload.tokenSet(nowFunc())
	if err != nil {
		return TokenSet{}, err
	}
	if err := validateTokenSet(tokens); err != nil {
		return TokenSet{}, err
	}
	return tokens, nil
}
