package llm

import (
	"net/http"
	"net/url"
	"testing"
)

func TestFinalResponseEndpointURLPrefersSanitizedFinalRequest(t *testing.T) {
	finalURL, err := url.Parse("https://redirect-user:redirect-password@final.example.test/v2/messages?token=secret#fragment")
	if err != nil {
		t.Fatalf("parse final URL: %v", err)
	}
	resp := &http.Response{Request: &http.Request{URL: finalURL}}

	got := FinalResponseEndpointURL(resp, "https://original.example.test/v1/messages?key=original-secret")
	if want := "https://final.example.test/v2/messages"; got != want {
		t.Fatalf("FinalResponseEndpointURL() = %q, want %q", got, want)
	}
}
