package protocolhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestDoDropsCredentialHeadersOnCrossHostRedirect(t *testing.T) {
	var targetHeaders http.Header
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/target", http.StatusFound)
	}))
	defer source.Close()

	res := testRes(source.URL, registry.AuthHeader)
	res.Transport.AuthHeader = "x-api-key"
	res.CredentialHeaders = map[string]string{"X-Gateway-Key": "gateway-secret"}
	call := &Call{
		Operation: "responses.create",
		Method:    http.MethodGet,
		URL:       source.URL + "/redirect",
		Req:       llm.Request{Model: "m"},
		Res:       res,
		Client:    source.Client(),
	}
	if err := Do(context.Background(), call, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"X-Api-Key", "X-Gateway-Key"} {
		if got := targetHeaders.Get(name); got != "" {
			t.Errorf("cross-host redirect sent %s = %q, want no credential", name, got)
		}
	}

	// A redirect within the same host must retain both configured credentials.
	var sameHostHeaders http.Header
	sameHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		sameHostHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))
	defer sameHost.Close()
	res.Transport.BaseURL = sameHost.URL
	call.URL = sameHost.URL + "/redirect"
	call.Client = sameHost.Client()
	if err := Do(context.Background(), call, nil); err != nil {
		t.Fatal(err)
	}
	if got := sameHostHeaders.Get("X-Api-Key"); got != res.Credential.Value {
		t.Errorf("same-host redirect x-api-key = %q, want %q", got, res.Credential.Value)
	}
	if got := sameHostHeaders.Get("X-Gateway-Key"); got != "gateway-secret" {
		t.Errorf("same-host redirect X-Gateway-Key = %q, want gateway-secret", got)
	}
}
