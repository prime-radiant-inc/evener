package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// The four trivial auth schemes of spec §8.1 live here so every protocol
// works for API-key providers without importing a second package; the two
// token-minting schemes (gcp-adc, oauth-openai-codex) live in
// llm/providers/tokenauth.
func init() {
	RegisterAuthenticator(registry.AuthBearer, bearerAuth{})
	RegisterAuthenticator(registry.AuthOptionalBearer, optionalBearerAuth{})
	RegisterAuthenticator(registry.AuthHeader, headerAuth{})
	RegisterAuthenticator(registry.AuthNone, noneAuth{})
}

type bearerAuth struct{}

func (bearerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if credentialHeaderWins(res, "Authorization") {
		return nil
	}
	if res.Credential.Value == "" {
		return missingCredential(res)
	}
	req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	return nil
}

type optionalBearerAuth struct{}

func (optionalBearerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if credentialHeaderWins(res, "Authorization") {
		return nil
	}
	if res.Credential.Value != "" {
		req.Header.Set("Authorization", "Bearer "+res.Credential.Value)
	}
	return nil
}

type headerAuth struct{}

func (headerAuth) Apply(_ context.Context, req *http.Request, res registry.Resolved) error {
	if res.Transport.AuthHeader == "" {
		return &ConfigurationError{Message: fmt.Sprintf("instance %q: auth = header needs auth_header", res.Instance)}
	}
	if credentialHeaderWins(res, res.Transport.AuthHeader) {
		return nil
	}
	if res.Credential.Value == "" {
		return missingCredential(res)
	}
	req.Header.Set(res.Transport.AuthHeader, res.Credential.Value)
	return nil
}

type noneAuth struct{}

func (noneAuth) Apply(context.Context, *http.Request, registry.Resolved) error { return nil }

// credentialHeaderWins reports whether res.CredentialHeaders already carries
// the auth header, in which case the header the instance authored wins and
// the scheme derives nothing from the key (spec §10: "when both auth =
// bearer and a credential_headers.Authorization are present, the header wins
// and no bearer is derived from the key"). protocolhttp.Prepare has already
// set it on the request.
func credentialHeaderWins(res registry.Resolved, name string) bool {
	for k, v := range res.CredentialHeaders {
		if v != "" && strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// missingCredential names the instance and repeats the registry's own
// "no credential" warning, which says which variable or login is missing.
func missingCredential(res registry.Resolved) error {
	msg := fmt.Sprintf("instance %q has no credential", res.Instance)
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "no credential") {
			return &ConfigurationError{Message: msg + ": " + w}
		}
	}
	return &ConfigurationError{Message: msg}
}
