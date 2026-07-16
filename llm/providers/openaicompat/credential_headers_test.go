package openaicompat

import (
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestCredentialHeadersRemainSeparateAndReachEveryCoreAdapter(t *testing.T) {
	a := NewForInstance(OpenAICompatInstanceParams{
		BaseURL:           "https://example.test/v1",
		Headers:           map[string]string{"X-Visible": "visible"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret"},
	})
	req, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.setChatHeaders(req, llm.Request{})
	if req.Header.Get("X-Visible") != "visible" || req.Header.Get("X-Gateway-Key") != "secret" {
		t.Fatalf("request headers = %#v", req.Header)
	}
	if _, merged := a.DefaultHeaders["X-Gateway-Key"]; merged {
		t.Fatalf("credential headers merged into DefaultHeaders: %#v", a.DefaultHeaders)
	}
	if got := a.responsesAdapter().CredentialHeaders["X-Gateway-Key"]; got != "secret" {
		t.Fatalf("Responses credential header = %q", got)
	}
}
