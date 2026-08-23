package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// TestWritePinSectionErrorName covers the ErrPinSectionName path.
func TestWritePinSectionErrorName(t *testing.T) {
	rec := httptest.NewRecorder()
	writePinSectionError(rec, hubcore.ErrPinSectionName)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestWritePinSectionErrorNotFound covers the ErrPinSectionNotFound path.
func TestWritePinSectionErrorNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	writePinSectionError(rec, hubcore.ErrPinSectionNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestWritePinSectionErrorConflict covers the ErrPinSectionConflict path.
func TestWritePinSectionErrorConflict(t *testing.T) {
	rec := httptest.NewRecorder()
	writePinSectionError(rec, hubcore.ErrPinSectionConflict)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

// TestWritePinSectionErrorDefault covers the default (500) path.
func TestWritePinSectionErrorDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	writePinSectionError(rec, errors.New("some other error"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestApiPinSection covers the apiPinSection conversion function.
func TestApiPinSection(t *testing.T) {
	section := hubcore.PinSection{ID: "abc", Name: "Test", MemberCount: 3}
	got := apiPinSection(section)
	if got.ID != "abc" || got.Name != "Test" || got.MemberCount != 3 {
		t.Fatalf("apiPinSection = %+v, want {abc Test 3}", got)
	}
}

// TestHandleAPIPinSectionsNilStore covers the nil-store guard.
func TestHandleAPIPinSectionsNilStore(t *testing.T) {
	s := &WebServer{}
	rec := httptest.NewRecorder()
	s.handleAPIPinSections(rec, httptest.NewRequest("GET", "/api/pin-sections", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestHandleAPIPinSectionNilStore covers the nil-store guard.
func TestHandleAPIPinSectionNilStore(t *testing.T) {
	s := &WebServer{}
	rec := httptest.NewRecorder()
	s.handleAPIPinSection(rec, httptest.NewRequest("PATCH", "/api/pin-sections/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestHandleAPISessionPinNilStore covers the nil-store guard.
func TestHandleAPISessionPinNilStore(t *testing.T) {
	s := &WebServer{}
	rec := httptest.NewRecorder()
	s.handleAPISessionPin(rec, httptest.NewRequest("POST", "/api/session-pin", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// TestHandleAPIPinSectionBadMethod covers the method-not-allowed guard.
func TestHandleAPIPinSectionBadMethod(t *testing.T) {
	s := &WebServer{}
	rec := httptest.NewRecorder()
	s.handleAPIPinSection(rec, httptest.NewRequest("GET", "/api/pin-sections/x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestHandleAPISessionPinBadMethod covers the method-not-allowed guard.
func TestHandleAPISessionPinBadMethod(t *testing.T) {
	s := &WebServer{}
	rec := httptest.NewRecorder()
	s.handleAPISessionPin(rec, httptest.NewRequest("GET", "/api/session-pin", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestHandleAPIPinSectionBadPath covers the path validation guard.
func TestHandleAPIPinSectionBadPath(t *testing.T) {
	s := &WebServer{cfg: hubcoreConfigWithPinSections()}
	rec := httptest.NewRecorder()
	// Empty sectionID after trimming.
	s.handleAPIPinSection(rec, httptest.NewRequest("PATCH", "/api/pin-sections/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestHandleAPIPinSectionBadPathWithSlash covers the slash-in-sectionID guard.
func TestHandleAPIPinSectionBadPathWithSlash(t *testing.T) {
	s := &WebServer{cfg: hubcoreConfigWithPinSections()}
	rec := httptest.NewRecorder()
	s.handleAPIPinSection(rec, httptest.NewRequest("PATCH", "/api/pin-sections/a/b", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func hubcoreConfigWithPinSections() hubcore.WebConfig {
	store := hubcore.NewPinSectionStore("")
	return hubcore.WebConfig{PinSections: store}
}

// TestHandleAPIPinSectionsWithStore covers the happy path of listing sections.
func TestHandleAPIPinSectionsWithStore(t *testing.T) {
	store := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "index.db"))
	s := &WebServer{cfg: hubcore.WebConfig{PinSections: store}}
	rec := httptest.NewRecorder()
	s.handleAPIPinSections(rec, httptest.NewRequest("GET", "/api/pin-sections", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestPinSectionDeleteResponse covers the response struct.
func TestPinSectionDeleteResponse(t *testing.T) {
	resp := pinSectionDeleteResponse{OK: true, Changed: true, MemberCount: 5}
	if !resp.OK || !resp.Changed || resp.MemberCount != 5 {
		t.Fatalf("pinSectionDeleteResponse = %+v", resp)
	}
}

// TestPinSectionMutationResponse covers the response struct.
func TestPinSectionMutationResponse(t *testing.T) {
	resp := pinSectionMutationResponse{OK: true, Changed: false}
	if !resp.OK || resp.Changed {
		t.Fatalf("pinSectionMutationResponse = %+v", resp)
	}
}

// TestSessionPinMutationResponse covers the response struct.
func TestSessionPinMutationResponse(t *testing.T) {
	resp := hubapi.SessionPinMutationResponse{OK: true, Changed: true}
	if !resp.OK || !resp.Changed {
		t.Fatalf("SessionPinMutationResponse = %+v", resp)
	}
}

// TestHandleAPIPinSectionDecodeError covers the JSON decode error path.
func TestHandleAPIPinSectionDecodeError(t *testing.T) {
	s := &WebServer{cfg: hubcoreConfigWithPinSections()}
	rec := httptest.NewRecorder()
	s.handleAPIPinSection(rec, httptest.NewRequest("PATCH", "/api/pin-sections/x", errorReader{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleAPISessionPinDecodeError covers the JSON decode error path.
func TestHandleAPISessionPinDecodeError(t *testing.T) {
	s := &WebServer{cfg: hubcoreConfigWithPinSections()}
	rec := httptest.NewRecorder()
	s.handleAPISessionPin(rec, httptest.NewRequest("POST", "/api/session-pin", errorReader{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHandleAPISessionPinDeleteMethod covers the DELETE method dispatch.
func TestHandleAPISessionPinDeleteMethod(t *testing.T) {
	s := &WebServer{cfg: hubcoreConfigWithPinSections()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/session-pin?ref=nonexistent", nil)
	s.handleAPISessionPin(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// errorReader is an io.Reader that always returns a read error.
type errorReader struct{}

func (errorReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
