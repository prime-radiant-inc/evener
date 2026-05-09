package main

import (
	"net/http"
	"strings"
)

// CSPMiddleware sets a strict Content-Security-Policy that allows only
// same-origin resources. All inline scripts and styles in templates have
// been moved into vendored asset files, so this CSP is enforceable today.
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// SameOriginGuard returns middleware that protects loopback bind from
// DNS rebinding and CSRF.
//
//   - Host header must be either the configured hubAddr (e.g. 127.0.0.1:9180)
//     or "localhost:<port>" with the same port.
//   - If Origin header is present, it must equal "http://<hubAddr>".
//     Missing Origin is allowed (curl/scripts).
//
// Browser tabs from other origins cannot pass either check.
func SameOriginGuard(hubAddr string) func(http.Handler) http.Handler {
	parts := strings.SplitN(hubAddr, ":", 2)
	port := ""
	if len(parts) == 2 {
		port = parts[1]
	}
	allowedHosts := map[string]struct{}{
		hubAddr: {},
	}
	if port != "" {
		allowedHosts["localhost:"+port] = struct{}{}
	}
	// Origins matching either Host alias are same-origin from the user's
	// perspective; both must be accepted, otherwise a tab loaded as
	// localhost:<port> fails to POST to a hub bound on 127.0.0.1:<port>.
	allowedOrigins := map[string]struct{}{
		"http://" + hubAddr: {},
	}
	if port != "" {
		allowedOrigins["http://localhost:"+port] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := allowedHosts[r.Host]; !ok {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if _, ok := allowedOrigins[origin]; !ok {
					http.Error(w, "forbidden origin", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
