package main

import "net/http"

// CSPMiddleware sets a Content-Security-Policy that limits resource origins to
// same-origin. Inline scripts are allowed because several templates (app.html,
// settings partials, credentials) use inline IIFEs for page initialisation;
// migrating them all to asset files is tracked separately.
//
// `img-src` allows `blob:` so the composer-attachments helper
// (cmd/serf-hub/assets/composer-attachments.js:reencodeToPng) can decode a
// pasted / dropped / picked image by loading a `URL.createObjectURL(blob)`
// reference into an `Image` element before re-encoding to PNG (kata 1pgw —
// without `blob:` here every image attachment surface renders "Not an image"
// because the Image element refuses the blob URL).
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
