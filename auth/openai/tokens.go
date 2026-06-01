package openai

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

// TokenExchangeRequest holds the authorization-code grant parameters passed to
// ExchangeCode. RedirectURI and CodeVerifier must match the values used when
// the authorization URL was built.
type TokenExchangeRequest struct {
	// Code is the authorization code returned on the OAuth callback.
	Code string
	// RedirectURI is the redirect URI presented during authorization.
	RedirectURI string
	// CodeVerifier is the PKCE verifier paired with the code challenge.
	CodeVerifier string
}

// RefreshTokenRequest holds the refresh-token grant parameter passed to
// RefreshToken.
type RefreshTokenRequest struct {
	// RefreshToken is the stored refresh token to exchange for fresh tokens.
	RefreshToken string
}

// TokenSet is the normalized result of a token-endpoint exchange or refresh.
type TokenSet struct {
	// AccessToken is the OAuth access (bearer) token.
	AccessToken string
	// RefreshToken is the token used to obtain new access tokens; it may be
	// empty on a refresh response if the server does not rotate it.
	RefreshToken string
	// IDToken is the OpenID Connect ID token, when granted (the openid scope is
	// requested by default).
	IDToken string
	// TokenType is the token type reported by the server (typically "Bearer").
	TokenType string
	// Scope is the space-delimited list of granted scopes.
	Scope string
	// Expiry is the absolute access-token expiry, computed from the server's
	// expires_in; it is zero when the server omits expires_in.
	Expiry time.Time
}

type tokenEndpointResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

type tokenEndpointError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeCode performs the authorization-code grant against the issuer's token
// endpoint, exchanging req.Code (with its PKCE verifier and redirect URI) for a
// TokenSet. If client is nil, one is created from cfg.HTTPTimeout. A non-2xx
// response is returned as an error built from the endpoint's error payload.
func ExchangeCode(ctx context.Context, client *http.Client, cfg Config, req TokenExchangeRequest) (TokenSet, error) {
	values := url.Values{
		"grant_type":    []string{"authorization_code"},
		"code":          []string{req.Code},
		"redirect_uri":  []string{req.RedirectURI},
		"client_id":     []string{cfg.clientID()},
		"code_verifier": []string{req.CodeVerifier},
	}
	return exchangeTokenRequest(ctx, client, cfg, values)
}

// RefreshToken performs the refresh-token grant against the issuer's token
// endpoint, exchanging req.RefreshToken for a fresh TokenSet. If client is nil,
// one is created from cfg.HTTPTimeout. A non-2xx response is returned as an
// error built from the endpoint's error payload (e.g. "invalid_grant"), which
// callers can inspect to decide whether re-login is required.
func RefreshToken(ctx context.Context, client *http.Client, cfg Config, req RefreshTokenRequest) (TokenSet, error) {
	values := url.Values{
		"grant_type":    []string{"refresh_token"},
		"refresh_token": []string{req.RefreshToken},
		"client_id":     []string{cfg.clientID()},
	}
	return exchangeTokenRequest(ctx, client, cfg, values)
}

func exchangeTokenRequest(ctx context.Context, client *http.Client, cfg Config, values url.Values) (TokenSet, error) {
	if client == nil {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}

	endpoint := strings.TrimRight(cfg.issuerBaseURL(), "/") + "/oauth/token"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return TokenSet{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(httpReq)
	if err != nil {
		return TokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TokenSet{}, compactTokenError(resp)
	}

	var payload tokenEndpointResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return TokenSet{}, fmt.Errorf("decode token response: %w", err)
	}
	return payload.intoTokenSet(time.Now()), nil
}

func compactTokenError(resp *http.Response) error {
	var payload tokenEndpointError
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	parts := []string{fmt.Sprintf("token endpoint returned status %d", resp.StatusCode)}
	if payload.Error != "" {
		parts = append(parts, payload.Error)
	}
	if payload.ErrorDescription != "" {
		parts = append(parts, payload.ErrorDescription)
	}
	return errors.New(strings.Join(parts, ": "))
}

func (r tokenEndpointResponse) intoTokenSet(now time.Time) TokenSet {
	tokens := TokenSet{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		IDToken:      r.IDToken,
		TokenType:    r.TokenType,
		Scope:        r.Scope,
	}
	if r.ExpiresIn > 0 {
		tokens.Expiry = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return tokens
}
