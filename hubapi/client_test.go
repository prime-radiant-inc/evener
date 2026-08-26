package hubapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/task"
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
	got := client.URL("/api/sessions/local:01ABC?include=details")
	want := "http://127.0.0.1:9180/api/sessions/local:01ABC?include=details"
	if got != want {
		t.Fatalf("URL()=%q, want %q", got, want)
	}
}

func TestNavigationConditionalGET(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/navigation/projects/a%2Fb" {
			t.Errorf("path: got %q", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("tier") != "recent" || r.URL.Query().Get("offset") != "2" || r.URL.Query().Get("limit") != "5" {
			t.Errorf("query: got %v", r.URL.Query())
		}
		if got := r.Header.Get("If-None-Match"); got != `"old"` {
			t.Errorf("If-None-Match: got %q", got)
		}
		w.Header().Set("ETag", `"new"`)
		_ = json.NewEncoder(w).Encode(hubapi.NavigationProjectPage{Key: "a/b", Tier: "recent"})
	})
	defer srv.Close()

	got, err := client.NavigationProjectPage(context.Background(), "a/b", "recent", 2, 5, `"old"`)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotModified || got.ETag != `"new"` || got.Value.Key != "a/b" {
		t.Fatalf("result = %+v", got)
	}
}

func TestNavigationRoutesAndBasePrefix(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/hub/api/navigation/pin-sections/p%2F+%252F" {
			t.Errorf("path: got %q", r.URL.EscapedPath())
		}
		_ = json.NewEncoder(w).Encode(hubapi.NavigationSectionResource{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}})
	})
	defer srv.Close()
	// Recreate with a path prefix to ensure navigation joining does not drop it.
	client, err := hubapi.NewClient(srv.URL+"/hub", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.NavigationPinSection(context.Background(), "p/+%2F", 0, 0, "")
	if err != nil || got.Value.Sessions == nil {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

func TestNavigationLimitValidation(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") })
	defer srv.Close()
	if _, err := client.NavigationSection(context.Background(), "live", 0, 51, ""); err == nil {
		t.Fatal("expected section limit rejection")
	}
	if _, err := client.NavigationPinSections(context.Background(), 0, 101, ""); err == nil {
		t.Fatal("expected catalog limit rejection")
	}
}

func TestNavigationOversizeResponse(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"generation_id":"%s"}`, strings.Repeat("x", 2<<20))
	})
	defer srv.Close()
	_, err := client.NavigationManifest(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error=%v, want bounded response error", err)
	}
}

func TestNavigationDecodeBoundary(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
		want string
	}{
		{"exact", 2 << 20, ""},
		{"oversize", (2 << 20) + 1, "exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body := `{"generation_id":"` + strings.Repeat("x", test.size-len(`{"generation_id":""}`)-1) + `"}`
				if len(body) < test.size {
					body += strings.Repeat(" ", test.size-len(body))
				} else if len(body) > test.size {
					body = body[:test.size]
				}
				_, _ = fmt.Fprint(w, body)
			})
			defer srv.Close()
			_, err := client.NavigationManifest(context.Background(), "")
			if test.want == "" && err != nil {
				t.Fatalf("exact boundary error: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestNavigationAllRoutesAndWireTypes(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paged := strings.Contains(r.URL.Path, "/sections/") || strings.Contains(r.URL.Path, "/pin-sections") || strings.Contains(r.URL.Path, "/catalogs/") || r.URL.Query().Get("tier") != ""
		if paged && (r.URL.Query().Get("offset") != "2" || r.URL.Query().Get("limit") != "5") {
			t.Errorf("query for %s: %v", r.URL.Path, r.URL.Query())
		}
		w.Header().Set("ETag", `"matrix"`)
		switch {
		case r.URL.Path == "/api/navigation":
			_ = json.NewEncoder(w).Encode(hubapi.NavigationManifest{Sources: hubapi.NavigationArray[hubapi.Source]{}})
		case strings.Contains(r.URL.Path, "/sections/") || strings.Contains(r.URL.Path, "/pin-sections/"):
			_ = json.NewEncoder(w).Encode(hubapi.NavigationSectionResource{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}})
		case r.URL.Path == "/api/navigation/pin-sections":
			_ = json.NewEncoder(w).Encode(hubapi.NavigationPinSectionCatalog{PinSections: hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor]{}})
		case strings.Contains(r.URL.Path, "/catalogs/"):
			_ = json.NewEncoder(w).Encode(hubapi.NavigationProjectCatalog{Projects: hubapi.NavigationArray[hubapi.NavigationProjectSummary]{}})
		case strings.Contains(r.URL.Path, "/projects/") && r.URL.Query().Get("tier") != "":
			_ = json.NewEncoder(w).Encode(hubapi.NavigationProjectPage{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}})
		case strings.Contains(r.URL.Path, "/projects/"):
			_ = json.NewEncoder(w).Encode(hubapi.NavigationProjectResource{})
		case strings.Contains(r.URL.Path, "/sessions/"):
			_ = json.NewEncoder(w).Encode(hubapi.NavigationSessionLocation{})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	})
	defer srv.Close()
	ctx := context.Background()
	if got, err := client.NavigationManifest(ctx, ""); err != nil || got.Value.Sources == nil {
		t.Fatalf("manifest: %+v %v", got, err)
	}
	if got, err := client.NavigationSection(ctx, "needs-you", 2, 5, ""); err != nil || got.Value.Sessions == nil {
		t.Fatalf("section: %+v %v", got, err)
	}
	if got, err := client.NavigationPinSection(ctx, "p", 2, 5, ""); err != nil || got.Value.Sessions == nil {
		t.Fatalf("pin section: %+v %v", got, err)
	}
	if got, err := client.NavigationPinSections(ctx, 2, 5, ""); err != nil || got.Value.PinSections == nil {
		t.Fatalf("pin catalog: %+v %v", got, err)
	}
	if got, err := client.NavigationCatalog(ctx, "projects", 2, 5, ""); err != nil || got.Value.Projects == nil {
		t.Fatalf("catalog: %+v %v", got, err)
	}
	if got, err := client.NavigationProject(ctx, "a", ""); err != nil {
		t.Fatalf("project: %+v %v", got, err)
	}
	if got, err := client.NavigationProjectPage(ctx, "a", "recent", 2, 5, ""); err != nil {
		t.Fatalf("project page: %+v %v", got, err)
	}
	if got, err := client.NavigationSessionLocation(ctx, "local:a", ""); err != nil {
		t.Fatalf("location: %+v %v", got, err)
	}
}

func TestNavigationExactRoutesAndDefaultQueries(t *testing.T) {
	type expectedRequest struct {
		path, query string
	}
	want := []expectedRequest{
		{"/api/navigation", ""},
		{"/api/navigation/sections/needs-you+%252F%21", ""},
		{"/api/navigation/pin-sections", ""},
		{"/api/navigation/pin-sections/p%2F+%252F", ""},
		{"/api/navigation/catalogs/archived+%252F%21", ""},
		{"/api/navigation/projects/a%2Fb+%25252F", ""},
		{"/api/navigation/projects/a%2Fb+%25252F", "tier=recent"},
		{"/api/navigation/sessions/local:a%2Fb+%252F", ""},
	}
	seen := 0
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if seen >= len(want) {
			t.Errorf("unexpected request %s", r.URL.RequestURI())
		} else {
			got := expectedRequest{r.URL.EscapedPath(), r.URL.RawQuery}
			if got != want[seen] {
				t.Errorf("request %d = %+v, want %+v", seen, got, want[seen])
			}
		}
		seen++
		_ = json.NewEncoder(w).Encode(map[string]any{})
	})
	defer srv.Close()
	ctx := context.Background()
	_, _ = client.NavigationManifest(ctx, "")
	_, _ = client.NavigationSection(ctx, "needs-you+%2F!", 0, 0, "")
	_, _ = client.NavigationPinSections(ctx, 0, 0, "")
	_, _ = client.NavigationPinSection(ctx, "p/+%2F", 0, 0, "")
	_, _ = client.NavigationCatalog(ctx, "archived+%2F!", 0, 0, "")
	_, _ = client.NavigationProject(ctx, "a/b+%252F", "")
	_, _ = client.NavigationProjectPage(ctx, "a/b+%252F", "recent", 0, 0, "")
	_, _ = client.NavigationSessionLocation(ctx, "local:a/b+%2F", "")
	if seen != len(want) {
		t.Fatalf("saw %d requests, want %d", seen, len(want))
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

func TestNavigationMalformedErrorRetainsTypedStatusAndZeroPayload(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "not json")
	})
	defer srv.Close()
	got, err := client.NavigationManifest(context.Background(), "")
	var httpErr *hubapi.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadGateway {
		t.Fatalf("error=%v, want typed 502", err)
	}
	if httpErr.Response != (hubapi.ErrorResponse{}) || !reflect.DeepEqual(got.Value, hubapi.NavigationManifest{}) {
		t.Fatalf("payload leaked: error=%+v result=%+v", httpErr.Response, got.Value)
	}
}

func TestNavigationLimitMatrixAndDefaults(t *testing.T) {
	tests := []struct {
		name string
		max  uint32
		call func(uint32) error
	}{
		{"section", 50, func(limit uint32) error {
			c, s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if limit == 0 && r.URL.RawQuery != "" {
					t.Errorf("default query=%q", r.URL.RawQuery)
				}
				w.Write([]byte(`{}`))
			})
			defer s.Close()
			_, err := c.NavigationSection(context.Background(), "live", 0, limit, "")
			return err
		}},
		{"pin-section", 50, func(limit uint32) error {
			c, s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
			defer s.Close()
			_, err := c.NavigationPinSection(context.Background(), "p", 0, limit, "")
			return err
		}},
		{"pin-list", 100, func(limit uint32) error {
			c, s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
			defer s.Close()
			_, err := c.NavigationPinSections(context.Background(), 0, limit, "")
			return err
		}},
		{"catalog", 100, func(limit uint32) error {
			c, s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
			defer s.Close()
			_, err := c.NavigationCatalog(context.Background(), "projects", 0, limit, "")
			return err
		}},
		{"project-page", 50, func(limit uint32) error {
			c, s := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
			defer s.Close()
			_, err := c.NavigationProjectPage(context.Background(), "p", "current", 0, limit, "")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(test.max); err != nil {
				t.Fatalf("max accepted: %v", err)
			}
			if err := test.call(test.max + 1); err == nil {
				t.Fatal("over-max accepted")
			}
		})
	}
}

func TestNavigationConditionalGETNotModifiedIsBodyless(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	defer srv.Close()

	got, err := client.NavigationManifest(context.Background(), `"same"`)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NotModified || got.ETag != `"same"` {
		t.Fatalf("result = %+v", got)
	}
}

func TestNavigationHTTPErrorIsTyped(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"error":"missing","code":404,"evener_error_info":"detail"}`)
	})
	defer srv.Close()

	_, err := client.NavigationSessionLocation(context.Background(), "local:missing", "")
	var httpErr *hubapi.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound || httpErr.Response.Error != "missing" || httpErr.Response.Code != 404 || httpErr.Response.EvenerErrorInfo != "detail" {
		t.Fatalf("error = %v, want typed 404", err)
	}
}

func TestNavigationMalformedAndCancelled(t *testing.T) {
	started := make(chan struct{})
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.NavigationManifest(ctx, ""); done <- err }()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled request returned nil error")
	}

	client, srv = newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `{"revision":`) })
	defer srv.Close()
	if _, err := client.NavigationManifest(context.Background(), ""); err == nil {
		t.Fatal("malformed success returned nil error")
	}
}

func TestPinSectionRESTTypesJSONRoundTrip(t *testing.T) {
	want := hubapi.SessionPinMutationResponse{
		OK:      true,
		Changed: true,
		Assignment: hubapi.SessionPinAssignment{
			SessionRef: "local:session-a",
			Section: hubapi.PinSection{
				ID:          "section-1",
				Name:        "Research",
				MemberCount: 2,
			},
		},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"ok":true,"changed":true,"assignment":{"session_ref":"local:session-a","section":{"id":"section-1","name":"Research","member_count":2}}}` {
		t.Fatalf("JSON = %s", got)
	}
	var got hubapi.SessionPinMutationResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestClientHealth(t *testing.T) {
	want := hubapi.HealthResponse{
		Version:   "1.0.0",
		StartedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		HubAddr:   "127.0.0.1:9180",
		Capabilities: hubapi.HealthCapabilities{
			Spawn: true,
		},
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
	if !got.Capabilities.Spawn {
		t.Error("expected Spawn capability")
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("started_at: got %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestClientHealth_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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

func TestClientSession(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.SessionDetail{
		Ref:   "local:test",
		Title: "Test Session",
		State: "idle",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		wantPath := "/api/sessions/local:test"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.Title != want.Title {
		t.Errorf("title: got %q, want %q", got.Title, want.Title)
	}
	if got.State != want.State {
		t.Errorf("state: got %q, want %q", got.State, want.State)
	}
}

func TestClientSession_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(hubapi.SessionDetail{})
	})
	defer srv.Close()

	_, err := client.Session(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should report status code 404, got %v", err)
	}
}

func TestClientSpawnSchema(t *testing.T) {
	want := hubapi.SpawnSchema{
		Fields: []hubapi.SpawnField{
			{Name: "prompt", Type: "string", Required: true},
		},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/spawn-schema" {
			t.Errorf("path: got %s, want /api/spawn-schema", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.SpawnSchema(context.Background())
	if err != nil {
		t.Fatalf("SpawnSchema: %v", err)
	}
	if len(got.Fields) != 1 || got.Fields[0].Name != "prompt" {
		t.Errorf("fields: got %+v", got.Fields)
	}
}

func TestClientSpawnSchema_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(hubapi.SpawnSchema{})
	})
	defer srv.Close()

	_, err := client.SpawnSchema(context.Background())
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should report status code 503, got %v", err)
	}
}

func TestClientSpawn(t *testing.T) {
	want := hubapi.SpawnResponse{
		Ref:       "local:abc123",
		HostID:    "local",
		SessionID: "abc123",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/spawn" {
			t.Errorf("path: got %s, want /api/spawn", r.URL.Path)
		}
		var req hubapi.SpawnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if req.Prompt != "do something" {
			t.Errorf("prompt: got %q, want %q", req.Prompt, "do something")
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	req := hubapi.SpawnRequest{Prompt: "do something"}
	got, err := client.Spawn(context.Background(), req)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientSpawn_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	_, err := client.Spawn(context.Background(), hubapi.SpawnRequest{})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClientModels(t *testing.T) {
	want := []hubapi.ModelOption{
		{Provider: "openai", Model: "gpt-4o"},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/models" {
			t.Errorf("path: got %s, want /api/models", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(got) != 1 || got[0].Model != "gpt-4o" {
		t.Errorf("models: got %+v", got)
	}
}

func TestClientModels_Error(t *testing.T) {
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode([]hubapi.ModelOption{})
	})
	defer srv.Close()

	_, err := client.Models(context.Background())
	if err == nil {
		t.Fatal("expected error for 502 response")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should report status code 502, got %v", err)
	}
}

func TestClientSend(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/send"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["text"] != "hello" {
			t.Errorf("text: got %q, want %q", body["text"], "hello")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Send(context.Background(), ref, "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestClientSend_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	defer srv.Close()

	err := client.Send(context.Background(), ref, "hello")
	if err == nil {
		t.Fatal("expected error for 409 response")
	}
}

func TestClientTasks(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := []task.Task{
		{ID: 1, Description: "task one", Status: task.TaskOpen},
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s, want GET", r.Method)
		}
		wantPath := "/api/sessions/local:test/tasks"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Tasks(context.Background(), ref)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("tasks: got %+v", got)
	}
}

func TestClientTasks_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode([]task.Task{})
	})
	defer srv.Close()

	_, err := client.Tasks(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should report status code 404, got %v", err)
	}
}

func TestClientInterrupt(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/interrupt"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Interrupt(context.Background(), ref)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

func TestClientInterrupt_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()

	err := client.Interrupt(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
}

func TestClientCompact(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/compact"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.Compact(context.Background(), ref)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestClientCompact_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	err := client.Compact(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestClientClear(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.RefResponse{
		Ref:       "local:test",
		HostID:    "local",
		SessionID: "test",
	}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/clear"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Clear(context.Background(), ref)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientClear_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(hubapi.RefResponse{})
	})
	defer srv.Close()

	_, err := client.Clear(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for 409 response")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Errorf("error should report status code 409, got %v", err)
	}
}

func TestClientFork(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	want := hubapi.RefResponse{
		Ref:       "local:fork123",
		HostID:    "local",
		SessionID: "fork123",
	}
	req := hubapi.ForkRequest{Turn: 5, EditedMessage: "edited", Label: "fork-label"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/fork"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var gotReq hubapi.ForkRequest
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if gotReq.Turn != req.Turn {
			t.Errorf("turn: got %d, want %d", gotReq.Turn, req.Turn)
		}
		if gotReq.EditedMessage != req.EditedMessage {
			t.Errorf("edited_message: got %q, want %q", gotReq.EditedMessage, req.EditedMessage)
		}
		if gotReq.Label != req.Label {
			t.Errorf("label: got %q, want %q", gotReq.Label, req.Label)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	defer srv.Close()

	got, err := client.Fork(context.Background(), ref, req)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if got.Ref != want.Ref {
		t.Errorf("ref: got %q, want %q", got.Ref, want.Ref)
	}
	if got.HostID != want.HostID {
		t.Errorf("host_id: got %q, want %q", got.HostID, want.HostID)
	}
	if got.SessionID != want.SessionID {
		t.Errorf("session_id: got %q, want %q", got.SessionID, want.SessionID)
	}
}

func TestClientFork_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(hubapi.RefResponse{})
	})
	defer srv.Close()

	_, err := client.Fork(context.Background(), ref, hubapi.ForkRequest{})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should report status code 400, got %v", err)
	}
}

func TestClientSetModel(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s, want POST", r.Method)
		}
		wantPath := "/api/sessions/local:test/model"
		if r.URL.Path != wantPath {
			t.Errorf("path: got %s, want %s", r.URL.Path, wantPath)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["model"] != "gpt-4o" {
			t.Errorf("model: got %q, want %q", body["model"], "gpt-4o")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer srv.Close()

	err := client.SetModel(context.Background(), ref, "gpt-4o")
	if err != nil {
		t.Fatalf("SetModel: %v", err)
	}
}

func TestClientSetModel_Error(t *testing.T) {
	ref := hubapi.Ref{HostID: "local", SessionID: "test"}
	client, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	defer srv.Close()

	err := client.SetModel(context.Background(), ref, "gpt-4o")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
}
