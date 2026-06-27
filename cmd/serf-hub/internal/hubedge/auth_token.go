// Package hubedge implements capability-URL / cookie authentication for the
// hub web edge: token load/create, the AuthGuard + HandleAuth middleware,
// bearer/cookie extraction, and constant-time comparison. It is the
// authentication half of the hub's HTTP security edge (sibling of httpsec).
package hubedge

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Capability-URL authentication for the hub web edge.
//
// The hub generates (or loads) a long-lived random token at startup and
// prints an auth URL with the token in a query parameter. A user visits
// the URL once per browser; the /auth handler validates the token and
// sets a httpOnly + SameSite=Strict cookie. Every subsequent request
// must carry the cookie (or an "Authorization: Bearer" header for
// scripted clients).
//
// The token lives in $hub_state_root/auth-token (mode 0600). Delete the
// file (or use --rotate-auth-token) to invalidate existing sessions.

const (
	// authCookieName is the cookie key set after a successful /auth visit.
	authCookieName = "serf_hub_auth"

	// TokenFileName is the basename inside hub_state_root for the token.
	TokenFileName = "auth-token"

	// authCookieMaxAgeSeconds is one year — the token is the secret, not
	// the cookie, so long expiry is fine.
	authCookieMaxAgeSeconds = 365 * 24 * 60 * 60
)

// LoadOrCreateAuthToken returns the existing token at
// $hubStateRoot/auth-token, or generates a fresh 256-bit token and
// persists it. The file is created with mode 0600.
func LoadOrCreateAuthToken(hubStateRoot string) (string, error) {
	if strings.TrimSpace(hubStateRoot) == "" {
		return "", errors.New("hub state root not configured")
	}
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		return "", fmt.Errorf("auth token: mkdir %s: %w", hubStateRoot, err)
	}
	path := filepath.Join(hubStateRoot, TokenFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("auth token: read %s: %w", path, err)
	}

	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("auth token: rand: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(buf[:])
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("auth token: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("auth token: rename %s: %w", path, err)
	}
	return tok, nil
}

// tokenFromRequest extracts the presented token from cookie or
// Authorization: Bearer. Empty string means "not presented."
func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(authCookieName); err == nil {
		return c.Value
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

// tokensEqual compares in constant time. Both empty returns false.
func tokensEqual(presented, expected string) bool {
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// isAuthExempt reports whether a path is reachable without
// authentication. /auth itself is the bootstrap; /api/health is for
// liveness checks; the PWA icons are a non-sensitive logo that the OS may
// fetch without credentials when installing to the home screen (the manifest
// stays gated — it carries the capability token).
func isAuthExempt(path string) bool {
	switch path {
	case "/auth", "/api/health",
		"/assets/icon.svg", "/assets/icon-192.png",
		"/assets/icon-512.png", "/assets/icon-maskable-512.png":
		return true
	}
	return false
}

// AuthGuard returns middleware that requires a valid token (via cookie
// or bearer header) for every route except /auth and /api/health.
//
// An empty token disables the guard entirely. This is a testing-only
// escape hatch — main.go always calls LoadOrCreateAuthToken before
// constructing the WebConfig, so a live hub never sees an empty token.
func AuthGuard(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			if isAuthExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if !tokensEqual(tokenFromRequest(r), token) {
				if strings.Contains(r.Header.Get("Accept"), "text/html") {
					http.Error(w,
						"Unauthorized.\n\n"+
							"This browser hasn't been authorized for this hub. Get the\n"+
							"auth URL from the hub operator (logged at startup) or read\n"+
							"the auth token from "+TokenFileName+" in the hub state\n"+
							"directory, then visit /auth?token=<value>.",
						http.StatusUnauthorized)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HandleAuth implements GET /auth?token=<t>: validates the token in the
// query, sets the auth cookie, and redirects to /.
func HandleAuth(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := r.URL.Query().Get("token")
		if !tokensEqual(presented, token) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     authCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			// Secure is intentionally false: hub may serve plain HTTP on
			// loopback or a Tailscale-only address. Set Secure=true via
			// reverse proxy if you front the hub with TLS.
			Secure: false,
			MaxAge: authCookieMaxAgeSeconds,
		})
		next := r.URL.Query().Get("next")
		if next == "" || !strings.HasPrefix(next, "/") {
			next = "/"
		}
		http.Redirect(w, r, next, http.StatusFound)
	}
}

// AuthURLFor constructs the visible auth URL for a given external base
// (e.g., "http://magic-kingdom.tailnet.ts.net:9180"). The base should
// NOT include a trailing slash.
func AuthURLFor(base, token string) string {
	base = strings.TrimRight(base, "/")
	return base + "/auth?token=" + token
}
