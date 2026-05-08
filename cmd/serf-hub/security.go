package main

import (
	"net/http"
	"strings"
)

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
	want := "http://" + hubAddr
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

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := allowedHosts[r.Host]; !ok {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != want {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
