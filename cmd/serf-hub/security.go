package main

import "net/http"

// CSPMiddleware sets a Content-Security-Policy that limits resource origins to
// same-origin. Inline scripts are allowed because several templates (app.html,
// settings partials, credentials) use inline IIFEs for page initialisation;
// migrating them all to asset files is tracked separately.
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
