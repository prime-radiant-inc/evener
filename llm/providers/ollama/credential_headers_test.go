package ollama

import "testing"

func TestNewForInstanceKeepsCredentialHeadersSeparate(t *testing.T) {
	a := newForInstance(InstanceParams{
		Headers:           map[string]string{"X-Visible": "visible"},
		CredentialHeaders: map[string]string{"X-Gateway-Key": "secret"},
	})
	if a.DefaultHeaders["X-Visible"] != "visible" || a.CredentialHeaders["X-Gateway-Key"] != "secret" {
		t.Fatalf("headers = %#v credentials = %#v", a.DefaultHeaders, a.CredentialHeaders)
	}
}
