package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
)

// TestHandleAPIArchiveProjectEmptyWorkingDir covers the empty-working_dir
// error path for project archive (lines 47-49).
func TestHandleAPIArchiveProjectEmptyWorkingDir(t *testing.T) {
	s := newTestWebServer(t)
	// Create a valid project ID by resolving a real directory.
	checkout := t.TempDir()
	project, err := identifier.ResolveProject(checkout)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"project","id":"` + project.ID + `","working_dir":"","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty working_dir: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "working_dir is required") {
		t.Fatalf("empty working_dir: body = %q", rec.Body.String())
	}
}

// TestHandleAPIArchiveProjectIDMismatch covers the project-ID-doesn't-match
// error path (lines 56-58).
func TestHandleAPIArchiveProjectIDMismatch(t *testing.T) {
	s := newTestWebServer(t)
	dir := t.TempDir()
	body := `{"kind":"project","id":"project-mismatch-0123456789","working_dir":"` + dir + `","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ID mismatch: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "does not match") {
		t.Fatalf("ID mismatch: body = %q", rec.Body.String())
	}
}

// TestHandleAPIArchiveNoStore covers the nil-archive-store path (lines 60-62).
func TestHandleAPIArchiveNoStore(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"session","id":"02wMz5Txv1C3Hut0M8GCeB","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("no store: code = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "archive store not configured") {
		t.Fatalf("no store: body = %q", rec.Body.String())
	}
}

// TestHandleAPIArchiveWrongMethod covers the method check (lines 15-17).
func TestHandleAPIArchiveWrongMethod(t *testing.T) {
	s := newTestWebServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/archive", nil)
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: code = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandleAPIArchiveNoProjectID covers the no-project special case
// (lines 38-40).
func TestHandleAPIArchiveNoProjectID(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"project","id":"no-project","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-project: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "no-project") {
		t.Fatalf("no-project: body = %q", rec.Body.String())
	}
}

// TestHandleAPIArchiveInvalidKind covers the invalid-kind path (lines 29-31).
func TestHandleAPIArchiveInvalidKind(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"widget","id":"x","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleAPIArchiveEmptyID covers the empty-ID path (lines 33-35).
func TestHandleAPIArchiveEmptyID(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"session","id":"","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty ID: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleAPIArchiveResolveError covers the ResolveProject error path
// (lines 51-53) by passing a working_dir that doesn't exist.
func TestHandleAPIArchiveResolveError(t *testing.T) {
	s := newTestWebServer(t)
	missing := filepath.Join(t.TempDir(), "nonexistent")
	body := `{"kind":"project","id":"project-test1234567890","working_dir":"` + missing + `","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("resolve error: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestHandleAPIArchiveInvalidProjectID covers the invalid project ID path
// (lines 42-44).
func TestHandleAPIArchiveInvalidProjectID(t *testing.T) {
	s := newTestWebServer(t)
	body := `{"kind":"project","id":"bad-id","working_dir":"/tmp","archived":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAPIArchive(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid project ID: code = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// silence unused import if filepath/os not used in some build
var _ = os.Stat
