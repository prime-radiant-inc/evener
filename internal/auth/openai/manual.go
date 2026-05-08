package openai

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
)

var (
	ErrInvalidRedirectURL = errors.New("invalid redirect URL")
	ErrMissingCode        = errors.New("missing authorization code")
	ErrStateMismatch      = errors.New("state mismatch")
)

// ParseRedirectURL extracts the authorization code and state from a pasted redirect URL.
func ParseRedirectURL(raw string) (string, string, error) {
	redirectURL, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidRedirectURL, err)
	}
	if redirectURL.Scheme == "" || redirectURL.Host == "" {
		return "", "", ErrInvalidRedirectURL
	}

	values := redirectURL.Query()
	code := values.Get("code")
	if code == "" {
		return "", "", ErrMissingCode
	}

	return code, values.Get("state"), nil
}

// ValidateState verifies that the returned OAuth state matches the generated state.
func ValidateState(expected, got string) error {
	if expected == "" || got == "" {
		return ErrStateMismatch
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		return ErrStateMismatch
	}

	return nil
}
