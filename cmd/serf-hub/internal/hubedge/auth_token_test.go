package hubedge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestLoadOrCreateAuthToken_PersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	a, err := LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatalf("LoadOrCreateAuthToken: %v", err)
	}
	if len(a) < 43 {
		t.Errorf("token too short: %q", a)
	}
	info, err := os.Stat(filepath.Join(root, TokenFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	b, err := LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatalf("LoadOrCreateAuthToken reload: %v", err)
	}
	if a != b {
		t.Errorf("token not stable: %q vs %q", a, b)
	}
}

func TestAuthGuard_AllowsExemptRoutes(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	// /auth + health bootstrap, plus the non-sensitive PWA icons (so the OS can
	// fetch the home-screen icon without credentials at install time).
	for _, path := range []string{"/auth", "/api/health", "/assets/icon.svg", "/assets/icon-192.png", "/assets/icon-512.png", "/assets/icon-maskable-512.png"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", path, rec.Code)
		}
	}
	// The manifest must stay gated — it carries the capability token in start_url.
	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("manifest should require auth (it carries the token), got %d", rec.Code)
	}
}

func TestAuthGuard_RejectsMissingToken(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
}

func TestAuthGuard_AcceptsCookie(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
}

func TestAuthGuard_AcceptsBearer(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
}

func TestAuthGuard_RejectsWrongToken(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "nope"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
}

func TestAuthGuard_EmptyTokenBypassesAuth(t *testing.T) {
	// Empty token is the documented testing escape hatch.
	guard := AuthGuard("")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 (empty token bypasses)", rec.Code)
	}
}

func TestHandleAuth_ValidatesAndSetsCookie(t *testing.T) {
	h := HandleAuth("secret")
	req := httptest.NewRequest(http.MethodGet, "/auth?token=secret", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("code = %d, want 302", rec.Code)
	}
	got := rec.Result().Cookies()
	if len(got) != 1 || got[0].Name != authCookieName || got[0].Value != "secret" {
		t.Errorf("cookie = %+v", got)
	}
	// Lax, not Strict: iOS standalone (home-screen) launches are treated as
	// externally initiated navigations by WebKit, and Strict cookies are not
	// sent on those — the PWA relaunch would land on the 401 wall.
	if got[0].SameSite != http.SameSiteLaxMode || !got[0].HttpOnly {
		t.Errorf("cookie should be SameSite=Lax and HttpOnly: %+v", got[0])
	}
	if got[0].MaxAge != authCookieMaxAgeSeconds {
		t.Errorf("cookie MaxAge = %d, want %d", got[0].MaxAge, authCookieMaxAgeSeconds)
	}
	if got[0].Path != "/" {
		t.Errorf("cookie Path = %q, want /", got[0].Path)
	}
}

func TestHandleAuth_RejectsWrongToken(t *testing.T) {
	h := HandleAuth("secret")
	req := httptest.NewRequest(http.MethodGet, "/auth?token=nope", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
}

func TestHandleAuth_HonorsNextParam(t *testing.T) {
	h := HandleAuth("secret")
	req := httptest.NewRequest(http.MethodGet, "/auth?token=secret&next=/settings/launch", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings/launch" {
		t.Errorf("Location = %q, want /settings/launch", loc)
	}
}

func TestHandleAuth_RejectsExternalNext(t *testing.T) {
	h := HandleAuth("secret")
	req := httptest.NewRequest(http.MethodGet, "/auth?token=secret&next=http://evil.example.com/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want / (external next rejected)", loc)
	}
}

func TestAuthURLFor(t *testing.T) {
	got := AuthURLFor("http://magic-kingdom:9180/", "tok")
	want := "http://magic-kingdom:9180/auth?token=tok"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadOrCreateAuthToken_EmptyRoot(t *testing.T) {
	_, err := LoadOrCreateAuthToken("")
	if err == nil {
		t.Fatal("expected error for empty hubStateRoot")
	}
	if !strings.Contains(err.Error(), "hub state root not configured") {
		t.Errorf("error should be the unconfigured-root branch, got %v", err)
	}
	_, err = LoadOrCreateAuthToken("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only hubStateRoot")
	}
	if !strings.Contains(err.Error(), "hub state root not configured") {
		t.Errorf("error should be the unconfigured-root branch, got %v", err)
	}
}

func TestLoadOrCreateAuthToken_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Create a file where the parent of hubStateRoot should be a directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(blocker, "root")
	_, err := LoadOrCreateAuthToken(root)
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should be from the mkdir path, got %v", err)
	}
}

func TestLoadOrCreateAuthToken_ReadFileError(t *testing.T) {
	root := t.TempDir()
	// Make the token path itself a directory. ReadFile opens it fine but the
	// first Read returns EISDIR ("is a directory"), which is a non-ErrNotExist
	// error — root-proof, since reading a directory fails regardless of uid.
	if err := os.Mkdir(filepath.Join(root, TokenFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateAuthToken(root)
	if err == nil {
		t.Fatal("expected error when read file fails")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error should be from the read path, got %v", err)
	}
}

func TestLoadOrCreateAuthToken_WriteFileError(t *testing.T) {
	root := t.TempDir()
	// The token file doesn't exist yet (ReadFile returns ErrNotExist), so the
	// code proceeds to write the temp file path+".tmp". Pre-create that path as
	// a directory: opening a directory for writing fails with EISDIR even for
	// root, so this exercises the write-error branch regardless of uid.
	if err := os.Mkdir(filepath.Join(root, TokenFileName+".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateAuthToken(root)
	if err == nil {
		t.Fatal("expected error when write file fails")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("error should be from the write path, got %v", err)
	}
}

func TestAuthGuard_ReturnsHTMLForBrowser(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Unauthorized") {
		t.Errorf("body missing 'Unauthorized': %q", body)
	}
}

// A GET to any guarded route with the capability token in the query must
// self-authenticate: set the cookie and redirect to the same URL with the
// token stripped. This is the self-heal for an iOS standalone relaunch that
// restores a deep URL (e.g. /s/<id>) into a cookie jar that lost the cookie.
func TestAuthGuard_AcceptsQueryTokenOnAnyGET(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/s/abc123?token=secret&pane=details", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "secret") {
		t.Errorf("redirect Location leaks the token: %q", loc)
	}
	if !strings.HasPrefix(loc, "/s/abc123") || !strings.Contains(loc, "pane=details") {
		t.Errorf("Location = %q, want same path with token stripped and other params kept", loc)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != authCookieName || cookies[0].Value != "secret" {
		t.Errorf("query-token auth should set the auth cookie, got %+v", cookies)
	}
}

func TestAuthGuard_RejectsWrongQueryToken(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/s/abc123?token=nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("wrong query token must not set a cookie")
	}
}

func TestAuthGuard_IgnoresQueryTokenOnPOST(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/spawn?token=secret", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401 (query token is GET-navigation-only)", rec.Code)
	}
}

// The 401 wall must never be cacheable: a cached 401 page in a PWA/browser
// cache would keep an already re-authorized app stuck on the wall.
func TestAuthGuard_401IsNoStore(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	for _, accept := range []string{"text/html", "application/json"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d, want 401", rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Accept %s: Cache-Control = %q, want no-store", accept, cc)
		}
	}
}

// Every cookie-authenticated request slides the cookie's one-year expiry
// forward, so an installed PWA's jar never ages out while in use.
func TestAuthGuard_RefreshesCookieOnAuthenticatedRequest(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != authCookieName || cookies[0].MaxAge != authCookieMaxAgeSeconds {
		t.Errorf("authenticated request should refresh the auth cookie, got %+v", cookies)
	}
}

// Bearer-authenticated (scripted) requests must not get a Set-Cookie.
func TestAuthGuard_NoCookieRefreshForBearer(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("bearer auth should not set cookies, got %+v", rec.Result().Cookies())
	}
}

func TestAuthGuard_ReturnsPlainForAPI(t *testing.T) {
	guard := AuthGuard("secret")
	h := guard(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "unauthorized") {
		t.Errorf("body missing 'unauthorized': %q", body)
	}
}
