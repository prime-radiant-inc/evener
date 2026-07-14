package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
)

func TestArchiveEndpointRejectsProjectIDPathMismatchAndNoProject(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	for name, body := range map[string]string{
		"mismatch":   `{"kind":"project","id":"` + project.ID + `","working_dir":"` + filepath.Join(root, "other") + `","archived":true}`,
		"no-project": `{"kind":"project","id":"no-project","working_dir":"` + projectDir + `","archived":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

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
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})

	body := `{"kind":"project","id":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `","archived":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
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
	if _, ok := d[hubcore.ArchiveKey{Kind: "project", ID: project.ID}]; !ok {
		t.Fatalf("project decision not persisted; decisions=%v", d)
	}
	if d[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("archived should be false; decisions=%v", d)
	}
}

func TestArchiveUnarchiveUsesCanonicalProjectID(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	store := hubcore.NewArchiveStore(filepath.Join(dir, "index.db"))
	web := NewWebServer(hubcore.WebConfig{Archive: store, Past: hubcore.NewPastIndex("")})
	if err := store.Set("project", project.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"project","id":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `","archived":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	d, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	if d[hubcore.ArchiveKey{Kind: "project", ID: project.ID}] {
		t.Fatalf("un-archive must clear the row; decisions=%v", d)
	}
}
