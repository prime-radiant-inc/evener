package envvars

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
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
func ResolveOllamaBaseURL(baseURLEnv, hostEnv string) (string, error) {
	if b := strings.TrimSpace(baseURLEnv); b != "" {
		return validateOllamaURL(strings.TrimRight(b, "/"), false)
	}
	if h := strings.TrimSpace(hostEnv); h != "" {
		return NormalizeOllamaHost(h)
	}
	p, _ := Provider("ollama")
	return p.DefaultBaseURL, nil
}

// NormalizeOllamaHost converts an OLLAMA_HOST value (host, host:port, or
// full URL) into a complete base URL ending in /v1. IPv6 hosts are
// bracketed correctly: bare "::1" becomes "[::1]:11434", which a naive
// strings.Contains(":") check would have left as "::1" with the wrong
// scheme syntax. Values whose path already terminates in /v1 are preserved
// so paths like https://proxy.example/ollama/v1 are not double-suffixed.
func NormalizeOllamaHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		p, _ := Provider("ollama")
		return p.DefaultBaseURL, nil
	}

	// Ollama's cloud endpoint is the one documented bare host with different
	// scheme/port semantics from a local host.
	if h == "ollama.com" {
		return "https://ollama.com:443/v1", nil
	}

	hasScheme := strings.Contains(h, "://")
	if !hasScheme {
		// A bare IPv6 literal is not accepted by url.Parse after adding a
		// scheme; bracket it before parsing. Anything else is an authority
		// (possibly with a path) and is parsed by the same URL machinery.
		if net.ParseIP(h) != nil {
			h = "[" + h + "]"
		}
		h = "http://" + h
	}

	result, err := validateOllamaURL(h, true)
	if err != nil {
		return result, err
	}
	if hasScheme {
		u, err := url.Parse(result)
		if err != nil {
			return "", err
		}
		if u.Scheme == "https" && u.Hostname() == "ollama.com" && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "443")
		}
		return u.String(), nil
	}
	// Bare hosts use Ollama's local default port. An explicit port (including
	// bracketed IPv6 with a port) was already preserved by validation.
	u, err := url.Parse(result)
	if err != nil {
		return "", err
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "11434")
	}
	return u.String(), nil
}

// validateOllamaURL parses and validates the endpoint instead of treating it
// as a string. normalizeHost adds /v1; a base URL is deliberately left at its
// caller-supplied path, matching OLLAMA_BASE_URL's existing semantics.
func validateOllamaURL(raw string, normalizePath bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Ollama URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Ollama URL %q: scheme must be http or https", raw)
	}
	if u.Opaque != "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid Ollama URL %q: URL must have an authority without userinfo", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("invalid Ollama URL %q: query and fragment are not supported", raw)
	}
	host := u.Hostname()
	if host == "" || strings.TrimSpace(host) != host {
		return "", fmt.Errorf("invalid Ollama URL %q: host is empty or contains whitespace", raw)
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("invalid Ollama URL %q: port is empty", raw)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid Ollama URL %q: port must be between 1 and 65535", raw)
		}
	}

	if normalizePath {
		path := strings.TrimRight(u.Path, "/")
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		u.Path, u.RawPath = path, ""
	}
	return u.String(), nil
}
