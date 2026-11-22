package githubcopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Account is the stable GitHub identity of a credential. ID is GitHub's
// numeric user ID as a string; it survives a login rename.
type Account struct {
	ID    string
	Login string
	Email string
}

// FetchAccount reads GET /user with the access token. It requires the numeric
// ID because the login name can change; a null email is an empty string.
func FetchAccount(ctx context.Context, client *http.Client, cfg Config, accessToken string) (Account, error) {
	cfg = mergeConfigDefaults(cfg)
	if strings.TrimSpace(accessToken) == "" {
		return Account{}, fmt.Errorf("%w: missing access token", ErrMalformedResponse)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.APIBaseURL+"/user", nil)
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient(client, cfg).Do(req)
	if err != nil {
		return Account{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Account{}, fmt.Errorf("github account lookup returned status %d", resp.StatusCode)
	}
	var payload struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
		Email string      `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Account{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	if payload.ID == "" {
		return Account{}, fmt.Errorf("%w: missing account id", ErrMalformedResponse)
	}
	account := Account{ID: payload.ID.String(), Login: payload.Login, Email: payload.Email}
	return account, nil
}
