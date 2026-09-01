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
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Capability-URL authentication for the hub web edge.
//
// The hub generates (or loads) a long-lived random token at startup and
// prints an auth URL with the token as a path segment. A user visits
// the URL once per browser; the /auth/<token> handler validates the token
// and sets a httpOnly + SameSite=Lax cookie, named per-hub (cookieName) so
// hubs sharing a host don't clobber each other's cookie. Every subsequent
// request must carry the cookie (or an "Authorization: Bearer" header for
// scripted clients).
//
// The token rides in the path, not a query parameter, because the iOS
// system QR-code scanner truncates a scanned URL's query string before
// handing off to Safari, silently dropping a query-string token; a path
// segment survives that truncation. The query form is gone outright (no
// backward compatibility): HandleAuth reads the path only.
//
// The token lives in $hub_state_root/auth-token (mode 0600). Delete the
// file (or use --rotate-auth-token) to invalidate existing sessions.

const (
	// authCookiePrefix begins the cookie key set after a successful /auth
	// visit. The full name is per-hub (cookieName): a stable suffix derived
	// from the hub's own token. Cookies are not isolated by port (RFC 6265),
	// so two hubs sharing a host (a persistent hub plus an ephemeral test
	// one, a restart with a rotated token) would otherwise collide on one
	// shared cookie slot — the later hub's /auth overwrites the earlier
	// hub's cookie, 401ing the earlier hub on its next reload until the user
	// re-visits its /auth/<token> URL. A per-token name gives each hub its
	// own slot in the browser's by-name jar.
	authCookiePrefix = "evener_hub_auth"

	// TokenFileName is the basename inside hub_state_root for the token.
	TokenFileName = "auth-token"

	// authCookieMaxAgeSeconds is one year — the token is the secret, not
	// the cookie, so long expiry is fine.
	authCookieMaxAgeSeconds = 365 * 24 * 60 * 60
)

// cookieName is the auth cookie's per-hub name: authCookiePrefix plus a short
// hash of the hub's token. Distinct tokens yield distinct names, so hubs on
// the same host never clobber each other's cookie. The name is only a jar-slot
// key, never an authentication input — the guard still compares the cookie
// *value* against the expected token in constant time (tokensEqual). So a
// fast non-cryptographic hash (fnv, as hubcore/roster.go and past.go already
// use for namespacing) is right-sized here; the suffix is a hash, never the
// token itself, and reveals no more than the token value it sits beside in
// the Cookie header.
func cookieName(token string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	return authCookiePrefix + "_" + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// LoadOrCreateAuthToken returns the existing token at
// $hubStateRoot/auth-token, or generates a fresh 256-bit token and
// persists it. The file is created with mode 0600.
func LoadOrCreateAuthToken(hubStateRoot string) (string, error) {
	return loadOrCreateAuthToken(hubStateRoot, rand.Reader, os.Rename)
}

// isWellFormedToken reports whether tok has the exact shape
// loadOrCreateAuthToken generates below: the unpadded base64url encoding
// (RFC 4648 §5) of 32 random bytes. A persisted token that doesn't decode
// to that shape can't be trusted — RawURLEncoding's alphabet excludes "."
// and "/", so this also catches a dot-segment (".", "..") or path
// separator smuggled into the token file, which url.PathEscape
// (AuthURLFor) leaves unescaped and which browsers and http.ServeMux
// normalize out of a URL path before HandleAuth ever sees it.
func isWellFormedToken(tok string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(tok)
	return err == nil && len(decoded) == 32
}

func loadOrCreateAuthToken(hubStateRoot string, random io.Reader, rename func(string, string) error) (string, error) {
	if strings.TrimSpace(hubStateRoot) == "" {
		return "", errors.New("hub state root not configured")
	}
	if err := os.MkdirAll(hubStateRoot, 0o700); err != nil {
		return "", fmt.Errorf("auth token: mkdir %s: %w", hubStateRoot, err)
	}
	path := filepath.Join(hubStateRoot, TokenFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		// A malformed persisted token (wrong shape, or empty) is never
		// returned: fall through and regenerate, the same as a missing
		// file. This is a load-time normalize, not a validation error,
		// matching how an empty file is already handled below.
		if tok := strings.TrimSpace(string(data)); isWellFormedToken(tok) {
			return tok, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("auth token: read %s: %w", path, err)
	}

	var buf [32]byte
	if _, err := io.ReadFull(random, buf[:]); err != nil {
		return "", fmt.Errorf("auth token: rand: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(buf[:])
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("auth token: write %s: %w", tmp, err)
	}
	if err := rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("auth token: rename %s: %w", path, err)
	}
	return tok, nil
}

// tokenFromRequest extracts the presented token for the hub whose token is
// `expected`, from that hub's own cookie (cookieName) or an Authorization:
// Bearer header. Empty string means "not presented." The cookie name is
// per-hub so a co-located hub's cookie is never mistaken for this one's.
func tokenFromRequest(r *http.Request, expected string) string {
	if c, err := r.Cookie(cookieName(expected)); err == nil {
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
// authentication. /auth/<token> is the bootstrap — the token in the path
// IS the credential, checked by HandleAuth itself, so the guard must let
// the request through unchecked to reach it; /api/health is for liveness
// checks; the PWA icons are a non-sensitive logo that the OS may fetch
// without credentials when installing to the home screen (the manifest
// stays gated — it carries the capability token).
func isAuthExempt(path string) bool {
	if strings.HasPrefix(path, "/auth/") {
		return true
	}
	switch path {
	case "/api/health",
		"/assets/icon.svg", "/assets/icon-192.png",
		"/assets/icon-512.png", "/assets/icon-maskable-512.png":
		return true
	}
	return false
}

// AuthGuard returns middleware that requires a valid token (via cookie
// or bearer header) for every route except /auth/<token> and /api/health.
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
			if !tokensEqual(tokenFromRequest(r, token), token) {
				// Self-heal: accept the capability token in the query string on
				// any GET other than /auth itself, set the cookie, and redirect
				// with the token stripped. An iOS standalone (home-screen)
				// relaunch restores the last-viewed URL into a cookie jar that
				// may have lost the cookie; this lets any other tokened URL
				// recover. /auth is excluded deliberately: its own token now
				// lives in the path (see HandleAuth), and JESSE'S RULING
				// (2026-09-01) replaces the query form outright, with no
				// backward compatibility — accepting ?token= here for /auth
				// would resurrect exactly the form that was removed.
				if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/auth") && tokensEqual(r.URL.Query().Get("token"), token) {
					setAuthCookie(w, token)
					q := r.URL.Query()
					q.Del("token")
					clean := *r.URL
					clean.RawQuery = q.Encode()
					// This redirect processes a capability URL. Never let a
					// shared or browser cache retain either the redirect or its
					// token-bearing request context.
					w.Header().Set("Cache-Control", "no-store")
					http.Redirect(w, r, clean.RequestURI(), http.StatusFound)
					return
				}
				// A cached 401 wall would keep a re-authorized client stuck.
				w.Header().Set("Cache-Control", "no-store")
				if strings.Contains(r.Header.Get("Accept"), "text/html") {
					http.Error(w,
						"Unauthorized.\n\n"+
							"This browser hasn't been authorized for this hub. Get the\n"+
							"auth URL from the hub operator (logged at startup) or read\n"+
							"the auth token from "+TokenFileName+" in the hub state\n"+
							"directory, then visit /auth/<value>.",
						http.StatusUnauthorized)
					return
				}
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Slide the cookie's expiry forward on every cookie-authed
			// request so an installed PWA's jar never ages out while the
			// app is in use. Bearer (scripted) clients get no cookie.
			if c, err := r.Cookie(cookieName(token)); err == nil && tokensEqual(c.Value, token) {
				setAuthCookie(w, token)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// setAuthCookie writes the long-lived auth cookie. SameSite=Lax, not Strict:
// iOS treats a standalone (home-screen) web-app launch as an externally
// initiated top-level navigation and omits Strict cookies on it, which sent
// every PWA relaunch to the 401 wall. Lax still withholds the cookie from
// cross-site subresource and POST requests.
func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName(token),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure is intentionally false: hub may serve plain HTTP on
		// loopback or a Tailscale-only address. Set Secure=true via
		// reverse proxy if you front the hub with TLS.
		Secure: false,
		MaxAge: authCookieMaxAgeSeconds,
	})
}

// HandleAuth implements GET /auth/<t>: validates the token carried in the
// path (mounted as a subtree, e.g. "/auth/", so the full remainder of the
// path after the prefix is the token — matching the existing /s/ and
// /thread/ prefix-route convention), sets the auth cookie, and redirects
// to /. The token lives in the path, not a query parameter, because the
// iOS system QR-code scanner truncates a scanned URL's query string before
// handoff to Safari, silently dropping a query-string token.
func HandleAuth(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.URL.Path, "/auth/")
		if !tokensEqual(presented, token) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		setAuthCookie(w, token)
		next := r.URL.Query().Get("next")
		if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
			next = "/"
		}
		// This redirect processes a capability URL. Never let a shared or
		// browser cache retain either the redirect or its token-bearing
		// request context.
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, next, http.StatusFound)
	}
}

// AuthURLFor constructs the visible auth URL for a given external base
// (e.g., "http://magic-kingdom.tailnet.ts.net:9180"). The base should
// NOT include a trailing slash. The token is escaped for a path segment
// (not a query parameter) so it survives the iOS system QR scanner's
// query-string truncation before handoff to Safari.
func AuthURLFor(base, token string) string {
	base = strings.TrimRight(base, "/")
	return base + "/auth/" + url.PathEscape(token)
}
