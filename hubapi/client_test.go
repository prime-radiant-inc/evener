package hubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/hubapi"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*hubapi.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	client, err := hubapi.NewClient(srv.URL, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, srv
}

func TestClientURLPreservesQueryString(t *testing.T) {
	client, err := hubapi.NewClient("http://127.0.0.1:9180", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := client.URL("/api/health?include=details")
	want := "http://127.0.0.1:9180/api/health?include=details"
	if got != want {
		t.Fatalf("URL()=%q, want %q", got, want)
	}
}

func TestNavigationContractsHaveNoSequenceMember(t *testing.T) {
	values := []any{
		hubapi.NavigationManifest{}, hubapi.NavigationSectionResource{}, hubapi.NavigationPinSectionCatalog{},
		hubapi.NavigationProjectCatalog{}, hubapi.NavigationProjectResource{}, hubapi.NavigationProjectPage{}, hubapi.NavigationSessionLocation{},
	}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if _, ok := object["sequence"]; ok {
			t.Fatalf("%T unexpectedly has sequence member: %s", value, raw)
		}
	}
}

func TestClientHealth(t *testing.T) {
	want := hubapi.HealthResponse{
		Version:   "1.0.0",
		StartedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		HubAddr:   "127.0.0.1:9180",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/health" {
			t.Errorf("path: got %s, want /api/health", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Version != want.Version {
		t.Errorf("version: got %q, want %q", got.Version, want.Version)
	}
	if got.HubAddr != want.HubAddr {
		t.Errorf("hub_addr: got %q, want %q", got.HubAddr, want.HubAddr)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("started_at: got %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestClientHealth_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{})
	})
	defer srv.Close()

	_, err := client.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should report status code 400, got %v", err)
	}
}
