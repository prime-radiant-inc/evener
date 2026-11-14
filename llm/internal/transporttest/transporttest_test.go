package transporttest

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRoundTripFuncReturnsTheResponseAndClosesTheRequestBody(t *testing.T) {
	body := io.NopCloser(strings.NewReader("payload"))
	closed := false
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Body = &trackedReadCloser{ReadCloser: request.Body, closed: &closed}

	response := &http.Response{StatusCode: http.StatusOK}
	transport := RoundTripFunc(func(got *http.Request) (*http.Response, error) {
		if got != request {
			t.Fatalf("RoundTrip request = %p, want %p", got, request)
		}
		return response, nil
	})
	got, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got != response {
		t.Fatalf("RoundTrip response = %p, want %p", got, response)
	}
	if !closed {
		t.Fatal("RoundTrip did not close the request body")
	}
}

func TestNoHTTPServerStartsAnAddress(t *testing.T) {
	// The server's failure-on-request behavior is exercised by the provider
	// tests that call it; here the helper must at least stand one up.
	if srv := NoHTTPServer(t); srv.URL == "" {
		t.Fatal("NoHTTPServer returned no URL")
	}
}

type trackedReadCloser struct {
	io.ReadCloser
	closed *bool
}

func (r *trackedReadCloser) Close() error {
	*r.closed = true
	return r.ReadCloser.Close()
}
