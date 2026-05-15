package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

type sameOriginPolicy struct {
	hosts   map[string]struct{}
	origins map[string]struct{}
}

func newSameOriginPolicy(allowedHost string) sameOriginPolicy {
	allowedHost = strings.TrimSpace(allowedHost)
	if allowedHost == "" {
		return sameOriginPolicy{}
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
	return sameOriginPolicy{hosts: hosts, origins: origins}
}

func (p sameOriginPolicy) rejection(r *http.Request) string {
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

func hubTokenAuthorized(expected, authorization string) bool {
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
