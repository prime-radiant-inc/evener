package anthropic

import (
	"net/http"
	"testing"
)

func TestCredentialHeadersRemainSeparateAndReachRequest(t *testing.T) {
	a, err := NewForInstance(AnthropicInstanceParams{
		APIKey:            "provider-key",
		Headers:           map[string]string{"X-Visible": "visible"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.setAnthropicHeaders(req, nil)
	if req.Header.Get("X-Visible") != "visible" || req.Header.Get("X-Gateway-Key") != "secret" {
		t.Fatalf("request headers = %#v", req.Header)
	}
	if _, merged := a.DefaultHeaders["X-Gateway-Key"]; merged {
		t.Fatalf("credential headers merged into DefaultHeaders: %#v", a.DefaultHeaders)
	}
}
