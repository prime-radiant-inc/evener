package openai

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
)

var (
	// ErrInvalidRedirectURL is returned (sometimes wrapped) by ParseRedirectURL
	// when the input cannot be parsed as a URL or is missing a scheme or host.
	ErrInvalidRedirectURL = errors.New("invalid redirect URL")
	// ErrMissingCode is returned by ParseRedirectURL when the redirect URL has
	// no "code" query parameter.
	ErrMissingCode = errors.New("missing authorization code")
	// ErrStateMismatch is returned by ValidateState when the returned state is
	// empty or does not equal the expected state, indicating a possible CSRF
	// attempt or a stale callback.
	ErrStateMismatch = errors.New("state mismatch")
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
