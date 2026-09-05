package tokenauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// google-vertex-express §4.3). Credentials are looked up at an instance's
// first request, never at load, and the token source refreshes itself.
type GCPADC struct {
	// FindCredentials is the ADC lookup seam; nil means google.FindDefaultCredentials.
	FindCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)
	// CredentialsFromJSON is the stored-credential seam; nil means google.CredentialsFromJSON.
	CredentialsFromJSON func(ctx context.Context, data []byte, scopes ...string) (*google.Credentials, error)

	mu      sync.Mutex
	sources map[string]tokenSource
}

// tokenSource pairs a cached token source with whether the credential it
// came from is a user credential (authorized_user), decided once when the
// credential is obtained.
type tokenSource struct {
	ts             oauth2.TokenSource
	userCredential bool
}

// credentialJSONType returns raw's declared "type" field, or "" when raw is
// not valid JSON, has no "type" field, or "type" is not a string.
func credentialJSONType(raw []byte) string {
	var f struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return ""
	}
	return f.Type
}

// isUserCredential reports whether raw — a credential file's JSON — is an
// authorized_user (user) credential rather than a service account or other
// type. A nil/empty or unparsable raw is not a user credential.
func isUserCredential(raw []byte) bool {
	return credentialJSONType(raw) == "authorized_user"
}

// allowedCredentialTypes are the two documented pasteable shapes (spec §4):
// a service-account key and an authorized_user file. external_account and
// its siblings (workload identity federation, impersonated service
// accounts, …) can name a local file or an executable as their credential
// source, so a pasted one is refused rather than accepted sight-unseen.
var allowedCredentialTypes = map[string]bool{
	"service_account": true,
	"authorized_user": true,
}

// credentialTypeError reports why raw's declared credential type cannot
// mint a gcp-adc token: no "type" field, or a type outside
// allowedCredentialTypes. Assumes raw is already known to be valid JSON;
// callers go through checkCredentialJSON for that.
func credentialTypeError(raw []byte) error {
	t := credentialJSONType(raw)
	if t == "" {
		return errors.New(`credential JSON has no "type" field`)
	}
	if !allowedCredentialTypes[t] {
		return fmt.Errorf("credential type %q is not supported: paste a service-account key or an authorized_user file", t)
	}
	return nil
}

// checkCredentialJSON runs the pre-parse checks a credential JSON must pass
// before anything calls into google.CredentialsFromJSON: valid JSON, then
// an allowed "type". Both ValidateCredentialJSON (the paste path) and the
// stored-credential branch of tokenSource run this exact check, so the two
// paths cannot drift into reporting different things for the same bad
// input.
func checkCredentialJSON(data []byte) error {
	if !json.Valid(data) {
		return errors.New("not valid JSON")
	}
	return credentialTypeError(data)
}

// Apply sets Authorization from the instance's cached token source and, for
// a user credential whose base URL named a project, x-goog-user-project.
func (a *GCPADC) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	src, err := a.tokenSource(ctx, res)
	if err != nil {
		if res.Credential.Source == "store" {
			return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: stored credential JSON: %v", res.Instance, err), Cause: err}
		}
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: application-default credentials: %v (run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS)", res.Instance, err), Cause: err}
	}
	tok, err := src.ts.Token()
	if err != nil {
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

// ValidateCredentialJSON reports whether data is a credential JSON the
// gcp-adc scheme can mint tokens from: a service_account key or an
// authorized_user file (spec §4; other types, such as external_account, are
// refused by checkCredentialJSON). The hub calls it when a credential is
// pasted so a bad paste fails at set time, not at the first request (spec
// §4.4).
func ValidateCredentialJSON(data []byte) error {
	if err := checkCredentialJSON(data); err != nil {
		return err
	}
	_, err := google.CredentialsFromJSON(context.Background(), data, cloudPlatformScope) //nolint:staticcheck // deprecated upstream in favour of typed parsers; this scheme must accept both authorized_user and service_account JSON (spec §4), and the cloud.google.com/go/auth migration is out of scope
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

func (a *GCPADC) tokenSource(ctx context.Context, res registry.Resolved) (tokenSource, error) {
	key := sourceKey(res)
	a.mu.Lock()
	defer a.mu.Unlock()
	if src, ok := a.sources[key]; ok {
		return src, nil
	}
	// The source outlives the request that created it, so it must not
	// inherit that request's cancellation.
	bg := context.WithoutCancel(ctx)
	var creds *google.Credentials
	var err error
	if res.Credential.Source == "store" {
		if err := checkCredentialJSON([]byte(res.Credential.Value)); err != nil {
			return tokenSource{}, err
		}
		fromJSON := a.CredentialsFromJSON
		if fromJSON == nil {
			fromJSON = google.CredentialsFromJSON //nolint:staticcheck // deprecated upstream in favour of typed parsers; this scheme must accept both authorized_user and service_account JSON (spec §4), and the cloud.google.com/go/auth migration is out of scope
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
		return tokenSource{}, err
	}
	src := tokenSource{ts: oauth2.ReuseTokenSource(nil, creds.TokenSource), userCredential: isUserCredential(creds.JSON)}
	if a.sources == nil {
		a.sources = map[string]tokenSource{}
	}
	a.sources[key] = src
	return src, nil
}
