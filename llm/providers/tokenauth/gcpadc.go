package tokenauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// GCPADC sends a bearer token minted from Google credentials (spec §8.1):
// application-default credentials found on the host, or — when the
// registry resolved a credentials-store entry for the instance — the
// service-account / authorized_user JSON stored there (spec 2026-09-04
// google-vertex-express §4.3). Credentials are looked up at request time,
// never at load: the library's answer names the cached token source, so a
// re-login reaches the next request, and the source refreshes itself.
type GCPADC struct {
	// FindCredentials is the ADC lookup seam; nil means google.FindDefaultCredentials.
	FindCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)
	// CredentialsFromJSON is the stored-credential seam; nil means google.CredentialsFromJSON.
	CredentialsFromJSON func(ctx context.Context, data []byte, scopes ...string) (*google.Credentials, error)

	mu      sync.Mutex
	sources map[string]cachedSource
}

// cachedSource is one instance's token source plus the identity of the
// credential it was built from (credentialIdentity), so a replaced or
// removed credential is noticed on the next request and the old source
// dropped instead of kept alongside the new one.
type cachedSource struct {
	identity string
	tokenSource
}

// tokenSource pairs a cached token source with whether the credential it
// came from is a user credential (authorized_user), decided once when the
// credential is obtained.
type tokenSource struct {
	ts             oauth2.TokenSource
	userCredential bool
}

// isUserCredential reports whether raw — a credential file's JSON — is an
// authorized_user (user) credential rather than a service account or other
// type. A nil/empty or unparsable raw is not a user credential.
func isUserCredential(raw []byte) bool {
	return registry.CredentialJSONType(raw) == "authorized_user"
}

// Apply sets Authorization from the instance's cached token source and, for
// a user credential whose base URL named a project, x-goog-user-project.
func (a *GCPADC) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	src, err := a.tokenSource(ctx, res)
	if err != nil {
		if res.Credential.Source == "store" {
			return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: stored credential JSON: %v", res.Instance, err), Cause: err}
		}
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: application-default credentials: %v (run `gcloud auth application-default login`, set GOOGLE_APPLICATION_CREDENTIALS, or store a credential JSON for the instance)", res.Instance, err), Cause: err}
	}
	tok, err := src.ts.Token()
	if err != nil {
		// A refresh the token endpoint refused is a verdict on the credential,
		// not on the endpoint: report it as an authentication failure so the
		// classes layered on this error (the hub's credential test, the CLI's
		// probe) offer "sign in again" instead of "check the network". Any
		// other failure — a deadline, a transport error — keeps its own
		// meaning.
		if code := oauthRefusal(err); code != "" {
			if res.Credential.Source == "store" {
				return llm.NewAuthenticationError(res.Instance, fmt.Sprintf("instance %q: stored credential JSON was refused (%s)", res.Instance, code), err)
			}
			return llm.NewAuthenticationError(res.Instance, fmt.Sprintf("instance %q: application-default credentials were refused (%s) (run `gcloud auth application-default login`, set GOOGLE_APPLICATION_CREDENTIALS, or store a credential JSON for the instance)", res.Instance, code), err)
		}
		return fmt.Errorf("instance %q: gcp-adc token: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	// User credentials are not attributed to a project, and the publisher
	// listing has no project in its path and 403s without this header
	// naming one. Service accounts carry their own project and must not
	// name one here: Google requires serviceusage.services.use on any
	// project this header names, which a least-privilege service account
	// may lack, turning a working request into a 403 (spec §2.2, ruling R6).
	if project := res.Transport.Vars["GOOGLE_VERTEX_PROJECT"]; src.userCredential && project != "" {
		req.Header.Set("x-goog-user-project", project)
	}
	return nil
}

// oauthRefusal returns the OAuth error code when err is the token endpoint's
// refusal of the credential itself — invalid_grant for an expired, revoked, or
// replaced refresh token, invalid_client for a bad secret. Anything else (a
// deadline, an unreachable endpoint) carries no code: nothing about the
// credential was decided.
func oauthRefusal(err error) string {
	if refused, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		return refused.ErrorCode
	}
	return ""
}

// ValidateCredentialJSON reports whether data is a credential JSON the
// gcp-adc scheme can mint tokens from: a service_account key or an
// authorized_user file (spec §4; other types, such as external_account, are
// refused by registry.CheckCredentialJSON). The hub calls it when a
// credential is pasted so a bad paste fails at set time, not at the first
// request (spec §4.4).
func ValidateCredentialJSON(data []byte) error {
	if err := registry.CheckCredentialJSON(data); err != nil {
		return err
	}
	_, err := google.CredentialsFromJSON(context.Background(), data, cloudPlatformScope) //nolint:staticcheck // deprecated upstream in favour of typed parsers; this scheme must accept both authorized_user and service_account JSON (spec §4), and the cloud.google.com/go/auth migration is out of scope
	return err
}

// credentialIdentity names the credential a source was built from: the
// credential's own bytes, digested, so a replaced one — a rotated stored
// value, or an ADC file a re-login rewrote — is noticed on the next request
// and the source rebuilt rather than kept alongside the new one. The source
// name keeps an ADC credential and a stored one distinguishable even when
// their bytes coincide.
func credentialIdentity(source string, raw []byte) string {
	sum := sha256.Sum256(raw)
	return source + "\x00" + hex.EncodeToString(sum[:])
}

func (a *GCPADC) tokenSource(ctx context.Context, res registry.Resolved) (tokenSource, error) {
	// The source outlives the request that created it, so it must not
	// inherit that request's cancellation.
	bg := context.WithoutCancel(ctx)
	if res.Credential.Source == "store" {
		// A stored credential is named by the value resolution handed us, so
		// the cache answers without parsing anything.
		identity := credentialIdentity(res.Credential.Source, []byte(res.Credential.Value))
		a.mu.Lock()
		defer a.mu.Unlock()
		if c, ok := a.sources[res.Instance]; ok && c.identity == identity {
			return c.tokenSource, nil
		}
		if err := registry.CheckCredentialJSON([]byte(res.Credential.Value)); err != nil {
			return tokenSource{}, err
		}
		fromJSON := a.CredentialsFromJSON
		if fromJSON == nil {
			fromJSON = google.CredentialsFromJSON //nolint:staticcheck // deprecated upstream in favour of typed parsers; this scheme must accept both authorized_user and service_account JSON (spec §4), and the cloud.google.com/go/auth migration is out of scope
		}
		creds, err := fromJSON(bg, []byte(res.Credential.Value), cloudPlatformScope)
		if err != nil {
			return tokenSource{}, err
		}
		return a.storeSource(res, identity, creds, []byte(res.Credential.Value)), nil
	}
	// Application-default credentials carry no value in the resolution: the
	// library finds them, and the JSON it read is what names them. Reading
	// them per request is what lets a re-login — which rewrites the ADC file
	// — reach the next request instead of the next process (spec §4.3).
	find := a.FindCredentials
	if find == nil {
		find = google.FindDefaultCredentials
	}
	creds, err := find(bg, cloudPlatformScope)
	if err != nil {
		return tokenSource{}, err
	}
	identity := credentialIdentity(res.Credential.Source, creds.JSON)
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.sources[res.Instance]; ok && c.identity == identity {
		return c.tokenSource, nil
	}
	return a.storeSource(res, identity, creds, creds.JSON), nil
}

// storeSource caches a freshly built source for the instance and returns it.
// a.mu is held throughout, so the cache and the map it lives in are written
// under one lock.
func (a *GCPADC) storeSource(res registry.Resolved, identity string, creds *google.Credentials, credentialJSON []byte) tokenSource {
	src := tokenSource{ts: oauth2.ReuseTokenSource(nil, creds.TokenSource), userCredential: isUserCredential(credentialJSON)}
	if a.sources == nil {
		a.sources = map[string]cachedSource{}
	}
	a.sources[res.Instance] = cachedSource{identity: identity, tokenSource: src}
	return src
}
