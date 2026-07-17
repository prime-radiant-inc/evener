package llm

import (
	"net/http"
	"net/url"
)

// SanitizeEndpointURL retains only non-credential URL components suitable for
// durable endpoint provenance. Invalid endpoint text has no durable value.
func SanitizeEndpointURL(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return ""
	}
	return (&url.URL{
		Scheme:  parsed.Scheme,
		Host:    parsed.Host,
		Path:    parsed.Path,
		RawPath: parsed.RawPath,
	}).String()
}

// FinalResponseEndpointURL returns the endpoint that produced an HTTP response.
// Synthetic responses without request provenance fall back to the constructed
// request endpoint.
func FinalResponseEndpointURL(resp *http.Response, fallback string) string {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return SanitizeEndpointURL(resp.Request.URL.String())
	}
	return SanitizeEndpointURL(fallback)
}
