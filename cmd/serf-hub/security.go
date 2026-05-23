package main

import "net/http"

// CSPMiddleware sets a Content-Security-Policy that limits resource origins to
// same-origin. Inline scripts are allowed because several templates (app.html,
// settings partials, credentials) use inline IIFEs for page initialisation;
// migrating them all to asset files is tracked separately.
//
// `img-src` allows `https:` for remote AppWire replay images, and `blob:` so
// the composer-attachments helper
// (cmd/serf-hub/assets/composer-attachments.js:reencodeToPng) can decode a
// pasted / dropped / picked image by loading a `URL.createObjectURL(blob)`
// reference into an `Image` element before re-encoding to PNG (kata 1pgw —
// without `blob:` here every image attachment surface renders "Not an image"
// because the Image element refuses the blob URL).
//
// `style-src` allows `https://fonts.googleapis.com` so app.html can load the
// Google Fonts CSS that pulls in Hanken Grotesk + JetBrains Mono. The CSS
// itself references WOFF2 files on `fonts.gstatic.com`, which is allowed via
// the `font-src` directive below. See design language §1.2.
func CSPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src 'self' data: blob: https:; "+
				"connect-src 'self'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
