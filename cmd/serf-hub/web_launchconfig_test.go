package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

func TestWeb_CredentialsRoute(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/credentials", nil)
	req.Host = "127.0.0.1:9180"
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/_partials/settings/credentials") {
		t.Errorf("body did not reference the partial: %s", rec.Body.String())
	}
}

func TestWeb_CredentialsPartial(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", Roster: hubcore.NewRoster(t.TempDir(), nil), Past: hubcore.NewPastIndex("")})
	req := httptest.NewRequest(http.MethodGet, "/_partials/credentials", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "instances-root") {
		t.Errorf("partial missing root div")
	}
}
