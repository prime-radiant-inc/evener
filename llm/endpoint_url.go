package llm

import "net/url"

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
