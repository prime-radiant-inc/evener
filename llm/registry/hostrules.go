package registry

import (
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
// in /v1. Ported from envvars.ResolveOllamaBaseURL, which step 3 deletes.
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
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
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
		escapedPath := strings.TrimRight(u.EscapedPath(), "/")
		if !strings.HasSuffix(escapedPath, "/v1") {
			escapedPath += "/v1"
		}
		path, err := url.PathUnescape(escapedPath)
		if err != nil {
			return "", fmt.Errorf("invalid Ollama URL %q: invalid escaped path: %w", raw, err)
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

// vertexHost is the vertex-location rule (spec §9.4): the Vertex API host
// for a location.
func vertexHost(location string) string {
	switch location {
	case "global":
		return "https://aiplatform.googleapis.com"
	case "us", "eu":
		return "https://aiplatform." + location + ".rep.googleapis.com"
	}
	return "https://" + location + "-aiplatform.googleapis.com"
}
