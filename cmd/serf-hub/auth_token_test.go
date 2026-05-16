package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateAuthToken_PersistsAndReloads(t *testing.T) {
	root := t.TempDir()
	a, err := LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatalf("LoadOrCreateAuthToken: %v", err)
	}
	if len(a) < 40 {
		t.Errorf("token too short: %q", a)
	}
	info, err := os.Stat(filepath.Join(root, authTokenFile))
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
	for _, path := range []string{"/auth", "/api/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code = %d, want 200", path, rec.Code)
		}
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
	if got[0].SameSite != http.SameSiteStrictMode || !got[0].HttpOnly {
		t.Errorf("cookie should be SameSite=Strict and HttpOnly: %+v", got[0])
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
	if !strings.HasSuffix(AuthURLFor("http://x", "y"), "/auth?token=y") {
		t.Errorf("suffix wrong")
	}
}
