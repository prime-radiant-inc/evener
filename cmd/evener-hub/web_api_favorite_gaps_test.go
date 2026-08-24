package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleAPIFavoriteEmptyID covers the empty-ID error path (lines 39-41).
func TestHandleAPIFavoriteEmptyID(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"project","id":"","favorited":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty ID: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "id is required") {
		t.Fatalf("empty ID: body = %q, want 'id is required'", rec.Body.String())
	}
}

// TestHandleAPIFavoriteNoStore covers the nil-favorite-store path (lines 42-44).
func TestHandleAPIFavoriteNoStore(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"project","id":"project-test1234567890","favorited":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nil store: code = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "favorite store not configured") {
		t.Fatalf("nil store: body = %q, want 'favorite store not configured'", rec.Body.String())
	}
}

// TestHandleAPIFavoriteInvalidJSON covers the JSON decode error path (line 27).
func TestHandleAPIFavoriteInvalidJSON(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleAPIFavoriteSessionKind covers the session-kind redirect path
// (lines 30-32).
func TestHandleAPIFavoriteSessionKind(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"session","id":"sess-1","favorited":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("session kind: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "session-pin") {
		t.Fatalf("session kind: body = %q, want 'session-pin'", rec.Body.String())
	}
}

// TestHandleAPIFavoriteUnknownKind covers the unknown-kind path (lines 34-36).
func TestHandleAPIFavoriteUnknownKind(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"widget","id":"x","favorited":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/favorite", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleAPIFavoriteWrongMethod covers the method check (line 17-19).
func TestHandleAPIFavoriteWrongMethod(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/favorite", nil)
	rec := httptest.NewRecorder()
	s.handleAPIFavorite(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
