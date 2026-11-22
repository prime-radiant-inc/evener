package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

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
	resp, err := httpClient(client, cfg).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github OAuth endpoint %s returned status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	return nil
}

func postOAuthForm(ctx context.Context, client *http.Client, cfg Config, form url.Values) (tokenResponsePayload, error) {
	var payload tokenResponsePayload
	err := postFormJSON(ctx, client, cfg, "/login/oauth/access_token", form, &payload)
	return payload, err
}

func (p tokenResponsePayload) tokenSet(now time.Time) TokenSet {
	tokens := TokenSet{AccessToken: p.AccessToken, RefreshToken: p.RefreshToken,
		TokenType: p.TokenType, Scope: p.Scope}
	if p.ExpiresIn > 0 {
		tokens.AccessExpiry = now.Add(time.Duration(p.ExpiresIn) * time.Second)
	}
	if p.RefreshExpiresIn > 0 {
		tokens.RefreshExpiry = now.Add(time.Duration(p.RefreshExpiresIn) * time.Second)
	}
	return tokens
}

// RefreshToken redeems refreshToken for a rotated pair. bad_refresh_token is
// permanent; transport and server failures stay ordinary errors so callers can
// retry without overwriting the record.
func RefreshToken(ctx context.Context, client *http.Client, cfg Config, refreshToken string) (TokenSet, error) {
	cfg = mergeConfigDefaults(cfg)
	if strings.TrimSpace(refreshToken) == "" {
		return TokenSet{}, ErrBadRefreshToken
	}
	payload, err := postOAuthForm(ctx, client, cfg, url.Values{
		"client_id": {cfg.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	})
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
	if payload.AccessToken == "" {
		return TokenSet{}, fmt.Errorf("%w: missing access token", ErrMalformedResponse)
	}
	return payload.tokenSet(nowFunc()), nil
}
