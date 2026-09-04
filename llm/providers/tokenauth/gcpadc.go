package tokenauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// google-vertex-express §4.3). Credentials are looked up at an instance's
// first request, never at load, and the token source refreshes itself.
type GCPADC struct {
	// FindCredentials is the ADC lookup seam; nil means google.FindDefaultCredentials.
	FindCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)
	// CredentialsFromJSON is the stored-credential seam; nil means google.CredentialsFromJSON.
	CredentialsFromJSON func(ctx context.Context, data []byte, scopes ...string) (*google.Credentials, error)

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource
}

// Apply sets Authorization from the instance's cached token source and, for
// an instance whose base URL named a project, x-goog-user-project.
func (a *GCPADC) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	ts, err := a.tokenSource(ctx, res)
	if err != nil {
		if res.Credential.Source == "store" {
			return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: stored credential JSON: %v", res.Instance, err), Cause: err}
		}
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: application-default credentials: %v (run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS)", res.Instance, err), Cause: err}
	}
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("instance %q: gcp-adc token: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	// User credentials must name the project to bill and count quota
	// against; the publisher-model listing has no project in its path and
	// 403s without this. It is harmless where the path already carries the
	// project (spec §2.2).
	if project := res.Transport.Vars["GOOGLE_VERTEX_PROJECT"]; project != "" {
		req.Header.Set("x-goog-user-project", project)
	}
	return nil
}

// ValidateCredentialJSON reports whether data is a credential JSON the
// gcp-adc scheme can mint tokens from (service_account, authorized_user,
// external_account, …). The hub calls it when a credential is pasted so a
// bad paste fails at set time, not at the first request (spec §4.4).
func ValidateCredentialJSON(data []byte) error {
	_, err := google.CredentialsFromJSON(context.Background(), data, cloudPlatformScope)
	return err
}

// sourceKey is the token-source cache key: the instance alone for ADC, the
// instance plus a digest of the stored JSON otherwise, so replacing the
// stored credential rebuilds the source.
func sourceKey(res registry.Resolved) string {
	if res.Credential.Source != "store" {
		return res.Instance
	}
	sum := sha256.Sum256([]byte(res.Credential.Value))
	return res.Instance + "\x00" + hex.EncodeToString(sum[:])
}

func (a *GCPADC) tokenSource(ctx context.Context, res registry.Resolved) (oauth2.TokenSource, error) {
	key := sourceKey(res)
	a.mu.Lock()
	defer a.mu.Unlock()
	if ts, ok := a.sources[key]; ok {
		return ts, nil
	}
	// The source outlives the request that created it, so it must not
	// inherit that request's cancellation.
	bg := context.WithoutCancel(ctx)
	var creds *google.Credentials
	var err error
	if res.Credential.Source == "store" {
		fromJSON := a.CredentialsFromJSON
		if fromJSON == nil {
			fromJSON = google.CredentialsFromJSON
		}
		creds, err = fromJSON(bg, []byte(res.Credential.Value), cloudPlatformScope)
	} else {
		find := a.FindCredentials
		if find == nil {
			find = google.FindDefaultCredentials
		}
		creds, err = find(bg, cloudPlatformScope)
	}
	if err != nil {
		return nil, err
	}
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	if a.sources == nil {
		a.sources = map[string]oauth2.TokenSource{}
	}
	a.sources[key] = ts
	return ts, nil
}
