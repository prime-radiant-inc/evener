// Package transporttest holds the HTTP seam fixtures shared by the llm
// module's wire tests: a RoundTripper adapter, an always-failing reader, and a
// server that fails a test on any request.
package transporttest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// RoundTripFunc adapts a function to http.RoundTripper. It closes a non-nil
// request body after the function returns, as the RoundTripper contract
// requires.
type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close() //nolint:errcheck // Test transport honors the RoundTripper request-body contract.
	}
	return f(request)
}

// NoHTTPServer starts a server that fails the test the moment any request
// reaches it, so a test proving an error is returned before the wire cannot
// silently start doing network I/O.
func NoHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no HTTP exchange was expected: the error must short-circuit the call")
	}))
	t.Cleanup(srv.Close)
	return srv
}
