package llm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIdleResponseTransportNilHeader(t *testing.T) {
	encoding := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding <- r.Header.Get("Accept-Encoding")
		_, _ = io.WriteString(w, "response")
	}))
	defer server.Close()
	base := &http.Transport{DisableCompression: true}
	defer base.CloseIdleConnections()
	transport := &idleResponseTransport{base: base, timeout: time.Minute, compression: true}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = nil
	originalContext := req.Context()
	defer func() {
		if req.Header != nil {
			t.Error("caller header mutated")
		}
		if req.Context() != originalContext || req.Context().Err() != nil {
			t.Error("caller context mutated or canceled")
		}
	}()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "response" {
		t.Fatalf("body = %q", body)
	}
	if got := <-encoding; got != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want gzip", got)
	}
}
