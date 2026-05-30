// Package httpguard enforces inbound HTTP request guards: same-origin host
// and Origin checks plus bearer-token authorization.
package httpguard

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// SameOriginPolicy validates that requests target an allowed host and, when
// present, carry an allowed Origin header. A zero-value policy permits all
// requests.
type SameOriginPolicy struct {
	hosts   map[string]struct{}
	origins map[string]struct{}
}

// NewSameOriginPolicy builds a SameOriginPolicy from a single allowed host.
// An empty allowedHost yields a permissive policy that accepts every request.
func NewSameOriginPolicy(allowedHost string) SameOriginPolicy {
	allowedHost = strings.TrimSpace(allowedHost)
	if allowedHost == "" {
		return SameOriginPolicy{}
	}
	hosts := map[string]struct{}{}
	addHost := func(host string) {
		if host == "" {
			return
		}
		hosts[host] = struct{}{}
	}
	addHost(allowedHost)
	if host, port, err := net.SplitHostPort(allowedHost); err == nil && port != "" {
		addHost(net.JoinHostPort(host, port))
		switch host {
		case "127.0.0.1":
			addHost(net.JoinHostPort("localhost", port))
		case "localhost":
			addHost(net.JoinHostPort("127.0.0.1", port))
		case "::1":
			addHost(net.JoinHostPort("localhost", port))
		}
	}
	origins := make(map[string]struct{}, len(hosts))
	for host := range hosts {
		origins["http://"+host] = struct{}{}
	}
	return SameOriginPolicy{hosts: hosts, origins: origins}
}

// Rejection returns a non-empty reason when the request violates the policy,
// or "" when the request is allowed.
func (p SameOriginPolicy) Rejection(r *http.Request) string {
	if len(p.hosts) == 0 {
		return ""
	}
	if _, ok := p.hosts[r.Host]; !ok {
		return "forbidden host"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, ok := p.origins[origin]; !ok {
			return "forbidden origin"
		}
	}
	return ""
}

// HubTokenAuthorized reports whether the Authorization header carries a bearer
// token matching expected. An empty expected token authorizes every request.
// The comparison is constant-time to avoid leaking the token via timing.
func HubTokenAuthorized(expected, authorization string) bool {
	if expected == "" {
		return true
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
