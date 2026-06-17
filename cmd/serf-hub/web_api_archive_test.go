package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestArchiveEndpointSetsDecision(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"session","id":"s1","archived":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	d, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions() error: %v", err)
	}
	if !d[hubcore.ArchiveKey{Kind: "session", ID: "s1"}] {
		t.Fatalf("decision not persisted; decisions=%v", d)
	}
}

func TestArchiveEndpointRejectsBadKind(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"bogus","id":"p1","archived":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveEndpointRejectsNonPost(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodGet, "/api/archive", nil)
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveEndpointRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveEndpointRejectsEmptyID(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"session","id":"","archived":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestArchiveEndpointProjectKind(t *testing.T) {
	dir := t.TempDir()
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(`{"kind":"project","id":"my-proj","archived":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	d, err := store.Decisions()
	if err != nil {
		t.Fatalf("Decisions() error: %v", err)
	}
	if _, ok := d[hubcore.ArchiveKey{Kind: "project", ID: "my-proj"}]; !ok {
		t.Fatalf("project decision not persisted; decisions=%v", d)
	}
	if d[hubcore.ArchiveKey{Kind: "project", ID: "my-proj"}] {
		t.Fatalf("archived should be false; decisions=%v", d)
	}
}
