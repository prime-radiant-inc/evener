package envvars

import (
	"net"
	"strings"
)

// ResolveOllamaBaseURL implements Ollama's documented base-URL resolution
// order: OLLAMA_BASE_URL wins outright (used as-is, trailing slash
// stripped); otherwise OLLAMA_HOST is normalized via NormalizeOllamaHost;
// otherwise the provider's registered default applies.
//
// Every caller that turns OLLAMA_BASE_URL / OLLAMA_HOST into a base URL —
// the adapter that dials Ollama and the config materializer that seeds
// providers.json from the environment — must go through this one function.
// They used to each carry their own copy, and the copies drifted: the
// materializer normalized OLLAMA_BASE_URL but passed OLLAMA_HOST through
// unchanged, so `OLLAMA_HOST=localhost` (the documented quickstart value)
// persisted the bare string "localhost" as an instance's base_url. Nothing
// downstream expected a schemeless, portless host there, so the ollama
// provider posted to "localhost/chat/completions" and the transport failed
// with "unsupported protocol scheme" on every attempt.
func ResolveOllamaBaseURL(baseURLEnv, hostEnv string) string {
	if b := strings.TrimSpace(baseURLEnv); b != "" {
		return strings.TrimRight(b, "/")
	}
	if h := strings.TrimSpace(hostEnv); h != "" {
		return NormalizeOllamaHost(h)
	}
	p, _ := Provider("ollama")
	return p.DefaultBaseURL
}

// NormalizeOllamaHost converts an OLLAMA_HOST value (host, host:port, or
// full URL) into a complete base URL ending in /v1. IPv6 hosts are
// bracketed correctly: bare "::1" becomes "[::1]:11434", which a naive
// strings.Contains(":") check would have left as "::1" with the wrong
// scheme syntax. Values whose path already terminates in /v1 are preserved
// so paths like https://proxy.example/ollama/v1 are not double-suffixed.
func NormalizeOllamaHost(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimRight(h, "/")
	if h == "" {
		p, _ := Provider("ollama")
		return p.DefaultBaseURL
	}
	if strings.Contains(h, "://") {
		// Has scheme — append /v1 if not already present.
		if strings.HasSuffix(h, "/v1") {
			return h
		}
		return h + "/v1"
	}
	// No scheme. Determine whether a port is present and whether the host
	// is a bare IPv6 literal that needs brackets.
	if _, _, err := net.SplitHostPort(h); err != nil {
		switch {
		case strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]"):
			// Bracketed IPv6 with no port.
			h += ":11434"
		case strings.Count(h, ":") >= 2:
			// Bare IPv6 with no port: "::1" or "fe80::1".
			h = "[" + h + "]:11434"
		default:
			// Hostname or IPv4 without a port.
			h += ":11434"
		}
	}
	return "http://" + h + "/v1"
}
