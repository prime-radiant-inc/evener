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

// redactURL renders a URL for an error message without echoing userinfo:
// OLLAMA_HOST and OLLAMA_BASE_URL may carry a password, and these errors
// reach warnings, listings, and logs.
func redactURL(raw string) string {
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
		return "", fmt.Errorf("invalid Ollama URL %q: %w", redactURL(raw), err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Ollama URL %q: scheme must be http or https", redactURL(raw))
	}
	if u.Opaque != "" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("invalid Ollama URL %q: URL must have an authority without userinfo", redactURL(raw))
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("invalid Ollama URL %q: query and fragment are not supported", redactURL(raw))
	}
	host := u.Hostname()
	if host == "" || strings.TrimSpace(host) != host {
		return "", fmt.Errorf("invalid Ollama URL %q: host is empty or contains whitespace", redactURL(raw))
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("invalid Ollama URL %q: port is empty", redactURL(raw))
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid Ollama URL %q: port must be between 1 and 65535", redactURL(raw))
		}
	}
	if normalizePath {
		escapedPath := strings.TrimRight(u.EscapedPath(), "/")
		if !strings.HasSuffix(escapedPath, "/v1") {
			escapedPath += "/v1"
		}
		path, err := url.PathUnescape(escapedPath)
		if err != nil {
			return "", fmt.Errorf("invalid Ollama URL %q: invalid escaped path: %w", redactURL(raw), err)
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
