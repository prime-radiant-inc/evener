// Package providertest holds the provider transport fixtures shared by the
// anthropic and google wire tests.
package providertest

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// StreamingRequiredAuth is a stub authenticator whose transport answers every
// request as a stream: it is what routes Complete through CompleteViaStream
// (protocolhttp.RequiresStreamingComplete). Providers register it under their
// own test-only scheme so the transport can be driven through the
// streaming-complete path without a real backend. Authorization, when set, is
// applied as the test's asserted bearer header.
type StreamingRequiredAuth struct {
	Authorization string
}

func (a StreamingRequiredAuth) Apply(_ context.Context, req *http.Request, _ registry.Resolved) error {
	if a.Authorization != "" {
		req.Header.Set("Authorization", a.Authorization)
	}
	return nil
}

func (StreamingRequiredAuth) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}

func (StreamingRequiredAuth) RequiresStreamingComplete() bool { return true }

var registered sync.Map // scheme -> *sync.Once

// RegisterStreamingRequiredAuth registers the stub once per scheme. Providers
// share the helper but keep their own scheme constant.
func RegisterStreamingRequiredAuth(t *testing.T, scheme string, auth StreamingRequiredAuth) {
	t.Helper()
	once, _ := registered.LoadOrStore(scheme, &sync.Once{})
	once.(*sync.Once).Do(func() { llm.RegisterAuthenticator(scheme, auth) })
}
