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
		return missingCredential(res, "Authorization")
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
		return missingCredential(res, res.Transport.AuthHeader)
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

// CredentialHeaderShadowsKey reports whether the instance's authored
// credential_headers already supplies the header its scheme derives from a key,
// in which case the authored header wins and the scheme derives nothing
// (spec §10) — the same rule the authenticators above apply on the request,
// asked ahead of time by a caller deciding whether a key would ever be sent.
// It is credentialHeaderWins over the header this instance's scheme uses:
// bearer and optional-bearer derive Authorization, header derives the authored
// auth_header, and the schemes that derive no header from a key at all (none,
// gcp-adc, oauth-openai-codex) cannot be shadowed by one.
//
// The registry resolves a credential source from the Authorization entry alone
// (registry.credential), so an instance whose own auth header is authored under
// another name — or another case — resolves "store"/"none" while this predicate
// is true; that divergence is why callers ask it instead of reading the source
// string.
func CredentialHeaderShadowsKey(res registry.Resolved) bool {
	switch res.Transport.Auth {
	case registry.AuthBearer, registry.AuthOptionalBearer:
		return credentialHeaderWins(res, "Authorization")
	case registry.AuthHeader:
		return credentialHeaderWins(res, res.Transport.AuthHeader)
	}
	return false
}

// missingCredential names the instance and repeats the registry's own
// "no credential" warning, which says which variable or login is missing.
// The resolve path suppresses that warning when the auth header's own
// expression failed — the header form carries the diagnosis — so the second
// look recognizes the auth header's failure wording, case-folded like every
// other header match.
func missingCredential(res registry.Resolved, authHeader string) error {
	msg := fmt.Sprintf("instance %q has no credential", res.Instance)
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "no credential") {
			return &ConfigurationError{Message: msg + ": " + w, Cause: ErrNoCredential}
		}
	}
	prefix := fmt.Sprintf("credential header %q:", authHeader)
	for _, w := range res.Warnings {
		if len(w) >= len(prefix) && strings.EqualFold(w[:len(prefix)], prefix) {
			return &ConfigurationError{Message: msg + ": " + w, Cause: ErrNoCredential}
		}
	}
	return &ConfigurationError{Message: msg, Cause: ErrNoCredential}
}
