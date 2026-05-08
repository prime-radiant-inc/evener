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

type TokenExchangeRequest struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
}

type RefreshTokenRequest struct {
	RefreshToken string
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	Scope        string
	Expiry       time.Time
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
