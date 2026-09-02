package registry

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// defaultOllamaBaseURL is the URL an empty OLLAMA_HOST resolves to.
const defaultOllamaBaseURL = "http://localhost:11434/v1"

// resolveOllamaHost is the ollama-host rule (spec §9.1): OLLAMA_BASE_URL
// wins outright (trailing slash stripped, validated); otherwise the
// OLLAMA_HOST value (host, host:port, or URL) becomes a full base URL ending
// in /v1.
func resolveOllamaHost(baseURL, host string) (string, error) {
	if b := strings.TrimSpace(baseURL); b != "" {
		return validateOllamaURL(strings.TrimRight(b, "/"), false)
	}
	return normalizeOllamaHost(host)
}

func normalizeOllamaHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return defaultOllamaBaseURL, nil
	}
	if h == "ollama.com" {
		return "https://ollama.com:443/v1", nil
	}
	hasScheme := strings.Contains(h, "://")
	if !hasScheme {
		if net.ParseIP(h) != nil {
			h = "[" + h + "]"
		}
		h = "http://" + h
	}
	result, err := validateOllamaURL(h, true)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(result)
	if err != nil {
		return "", err
	}
	if hasScheme {
		if u.Scheme == "https" && u.Hostname() == "ollama.com" && u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "443")
		}
		return u.String(), nil
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "11434")
	}
	return u.String(), nil
}

// RedactURL renders a URL for display without echoing userinfo: a base_url
// may carry a password (OLLAMA_HOST and OLLAMA_BASE_URL routinely do), and
// the strings it reaches — warnings, listings, `evener models inspect` output
// pasted into a bug report — are all places a credential must not appear.
// A URL with no userinfo comes back unchanged.
func RedactURL(raw string) string {
	if !strings.Contains(raw, "@") {
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		return u.Redacted()
	}
	return "***@" + raw[strings.LastIndex(raw, "@")+1:]
}

func validateOllamaURL(raw string, normalizePath bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// A *url.Error repeats the URL it failed on; only its reason is safe.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return "", fmt.Errorf("invalid Ollama URL %q: %w", RedactURL(raw), err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Ollama URL %q: scheme must be http or https", RedactURL(raw))
	}
	if u.Opaque != "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid Ollama URL %q: URL must have an authority without userinfo", RedactURL(raw))
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("invalid Ollama URL %q: query and fragment are not supported", RedactURL(raw))
	}
	host := u.Hostname()
	if host == "" || strings.TrimSpace(host) != host {
		return "", fmt.Errorf("invalid Ollama URL %q: host is empty or contains whitespace", RedactURL(raw))
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("invalid Ollama URL %q: port is empty", RedactURL(raw))
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid Ollama URL %q: port must be between 1 and 65535", RedactURL(raw))
		}
	}
	if normalizePath {
		escapedPath := strings.TrimRight(u.EscapedPath(), "/")
		if !strings.HasSuffix(escapedPath, "/v1") {
			escapedPath += "/v1"
		}
		path, err := url.PathUnescape(escapedPath)
		if err != nil {
			return "", fmt.Errorf("invalid Ollama URL %q: invalid escaped path: %w", RedactURL(raw), err)
		}
		u.Path = path
		if u.RawPath != "" || escapedPath != path {
			u.RawPath = escapedPath
		} else {
			u.RawPath = ""
		}
	}
	return u.String(), nil
}

// validVertexLocation reports whether a Vertex location is shaped like the
// region token vertexHost may compose into a hostname: a single RFC-1123
// label - letters, digits, and interior hyphens, at most 63 bytes. A
// location is us-central1 or global, never a host: a dot, slash, colon, or
// any other byte would let the derivation build an authority outside
// *.googleapis.com (or smuggle a path into it), so the vertex-location
// rule refuses to derive from anything else (resolveBaseURLVia, load.go)
// and the canonical first-party comparison refuses to admit it
// (hostRuleInputAdmissible, instances.go).
func validVertexLocation(loc string) bool {
	if len(loc) == 0 || len(loc) > 63 || loc[0] == '-' || loc[len(loc)-1] == '-' {
		return false
	}
	for i := 0; i < len(loc); i++ {
		c := loc[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// vertexHost is the vertex-location rule (spec §9.4): the Vertex API host
// for a location. Callers validate the location first (validVertexLocation);
// this function only composes.
func vertexHost(location string) string {
	switch location {
	case "global":
		return "https://aiplatform.googleapis.com"
	case "us", "eu":
		return "https://aiplatform." + location + ".rep.googleapis.com"
	}
	return "https://" + location + "-aiplatform.googleapis.com"
}
