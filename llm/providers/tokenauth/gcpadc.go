package tokenauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// GCPADC sends a bearer token from Google application-default credentials
// (spec §8.1): the credentials are looked up at an instance's first
// request, never at load, and the token source refreshes itself.
type GCPADC struct {
	// FindCredentials is the lookup seam; nil means google.FindDefaultCredentials.
	FindCredentials func(ctx context.Context, scopes ...string) (*google.Credentials, error)

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource
}

// Apply sets Authorization from the instance's cached token source.
func (a *GCPADC) Apply(ctx context.Context, req *http.Request, res registry.Resolved) error {
	ts, err := a.tokenSource(ctx, res.Instance)
	if err != nil {
		return &llm.ConfigurationError{Message: fmt.Sprintf("instance %q: application-default credentials: %v (run `gcloud auth application-default login` or set GOOGLE_APPLICATION_CREDENTIALS)", res.Instance, err), Cause: err}
	}
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("instance %q: gcp-adc token: %w", res.Instance, err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return nil
}

func (a *GCPADC) tokenSource(ctx context.Context, instance string) (oauth2.TokenSource, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ts, ok := a.sources[instance]; ok {
		return ts, nil
	}
	find := a.FindCredentials
	if find == nil {
		find = google.FindDefaultCredentials
	}
	// The source outlives the request that created it, so it must not
	// inherit that request's cancellation.
	creds, err := find(context.WithoutCancel(ctx), cloudPlatformScope)
	if err != nil {
		return nil, err
	}
	ts := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	if a.sources == nil {
		a.sources = map[string]oauth2.TokenSource{}
	}
	a.sources[instance] = ts
	return ts, nil
}
