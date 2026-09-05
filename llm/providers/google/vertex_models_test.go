package google

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// vertexRes is a resolved google-vertex instance pointed at srv: the
// vertex-location host rule with the project-scoped v1 base URL the overlay
// builds. Header auth keeps the test offline; the listing code never reads
// the scheme.
func vertexRes(srv *httptest.Server) registry.Resolved {
	res := protoLive(srv)
	res.Instance = "vertex"
	res.Transport = registry.Transport{
		Auth: registry.AuthHeader, AuthHeader: "x-goog-api-key",
		HostRule:            registry.HostRuleVertexLocation,
		BaseURL:             srv.URL + "/v1/projects/my-project/locations/global",
		Endpoint:            "/publishers/google/models/{model}:generateContent",
		StreamEndpoint:      "/publishers/google/models/{model}:streamGenerateContent?alt=sse",
		ModelsEndpoint:      "/publishers/google/models",
		CountTokensEndpoint: registry.EndpointUnsupported,
		Vars:                map[string]string{"GOOGLE_VERTEX_PROJECT": "my-project", "GOOGLE_VERTEX_LOCATION": "global"},
	}
	return res
}

// vertexExpectedIDs is the spec's §2.4 expected filter output for the
// 2026-09-04 fixture, in listing order.
var vertexExpectedIDs = []string{
	"gemini-1.5-pro-002", "gemini-2.5-flash-preview-04-17", "gemini-2.5-pro",
	"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-3-flash-preview",
	"gemini-3.1-pro-preview", "gemini-3.5-flash", "gemini-3.1-flash-lite",
	"gemini-3.5-flash-lite", "gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash",
}

func TestVertexListModelsFiltersTheFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/vertex_publisher_models.json")
	if err != nil {
		t.Fatal(err)
	}
	srv, got := protoServer(t, 200, string(fixture))
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), vertexRes(srv))
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1beta1/publishers/google/models?pageSize=200" {
		t.Fatalf("path = %s; the listing is host-relative on v1beta1, never under the project", got.path)
	}
	if got.header.Get("x-goog-api-key") != "k-1" {
		t.Fatalf("auth header missing: %v", got.header)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.Caps.MaxInputTokens != nil || row.Caps.ContextWindow != nil {
			t.Fatalf("%s: the listing carries no capability data; caps must stay empty: %+v", row.ID, row.Caps)
		}
	}
	if strings.Join(ids, ",") != strings.Join(vertexExpectedIDs, ",") {
		t.Fatalf("ids =\n  %s\nwant\n  %s", strings.Join(ids, ", "), strings.Join(vertexExpectedIDs, ", "))
	}
}

func TestVertexListModelsFollowsPagination(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}],"nextPageToken":"p2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-2.5-pro","launchStage":"GA"}]}`))
	}))
	defer srv.Close()
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), vertexRes(srv))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "gemini-3.8-flash" || rows[1].ID != "gemini-2.5-pro" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(paths) != 2 || paths[1] != "/v1beta1/publishers/google/models?pageSize=200&pageToken=p2" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestVertexListModelsStopsOnARepeatedPageToken(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}],"nextPageToken":"stuck"}`))
	}))
	defer srv.Close()
	// The deadline only bounds a regression; the listing must give up on
	// its own the first time a page token repeats.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := (&Protocol{Client: srv.Client()}).ListModels(ctx, vertexRes(srv))
	if err == nil || !strings.Contains(err.Error(), "repeated page token") {
		t.Fatalf("err = %v, want the listing to refuse a repeated page token", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (the first page and the one that repeated its token)", requests)
	}
}

func TestVertexListModelsUnsupportedWhenEndpointIsDash(t *testing.T) {
	srv, _ := protoServer(t, 200, `{}`)
	res := vertexRes(srv)
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	_, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if !errors.Is(err, llm.ErrModelListingUnsupported) {
		t.Fatalf("err = %v, want ErrModelListingUnsupported (the express row)", err)
	}
}

func TestVertexTextModelFilterRules(t *testing.T) {
	for _, tc := range []struct {
		id, stage string
		want      bool
	}{
		{"gemini-3.8-flash", "GA", true},
		{"gemini-3-flash-preview", "PUBLIC_PREVIEW", true},
		{"gemini-2.5-pro-exp-03-25", "EXPERIMENTAL", false},
		{"gemini-2.5-flash-tts", "GA", false},
		{"gemini-embedding-2", "GA", false},
		{"gemini-3-pro-image", "GA", false},
		{"gemini-live-2.5-flash-native-audio", "GA", false},
		{"gemini-3.5-transcribe-preview", "PUBLIC_PREVIEW", false},
		{"gemini-3.5-live-translate-preview", "PUBLIC_PREVIEW", false},
		{"gemini-omni-1.1-flash-preview", "PUBLIC_PREVIEW", false},
		{"spicy-mayo", "GA", false},
		{"gemini-3.8-flash", "", false},
	} {
		if got := vertexTextModel(tc.id, tc.stage); got != tc.want {
			t.Errorf("vertexTextModel(%q, %q) = %v, want %v", tc.id, tc.stage, got, tc.want)
		}
	}
	var page vertexPublisherModelsPage
	if err := json.Unmarshal([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}]}`), &page); err != nil {
		t.Fatal(err)
	}
	if rows := filterVertexModels(page.PublisherModels); len(rows) != 1 || rows[0].ID != "gemini-3.8-flash" {
		t.Fatalf("filterVertexModels = %+v", rows)
	}
}

// TestVertexListModelsOnACustomPresetProviderWithoutHostRule covers a user
// provider built on the vertex-gemini preset with its own base URL: no host
// rule, but the preset's publisher-model endpoints, so it must list the
// Vertex way — host-relative on v1beta1 — rather than fall through to the
// Gemini API listing.
func TestVertexListModelsOnACustomPresetProviderWithoutHostRule(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/google/models/gemini-3.8-flash","launchStage":"GA"}]}`))
	}))
	defer srv.Close()
	res := vertexRes(srv)
	res.Instance = "myvertex"
	res.Transport.HostRule = ""
	res.Transport.BaseURL = srv.URL + "/v1/projects/p/locations/us-central1"
	res.Transport.Vars = nil
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/v1beta1/publishers/google/models?pageSize=200" {
		t.Fatalf("paths = %v, want the host-relative v1beta1 publisher listing", paths)
	}
	if len(rows) != 1 || rows[0].ID != "gemini-3.8-flash" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestVertexTransportDiscriminator(t *testing.T) {
	gemini := registry.Resolved{Transport: registry.Transport{Endpoint: "/models/{model}:generateContent"}}
	if isVertexTransport(gemini) {
		t.Error("the Gemini API serves models under /models/: that is not Vertex")
	}
	vertex := registry.Resolved{Transport: registry.Transport{Endpoint: "/publishers/google/models/{model}:generateContent"}}
	if !isVertexTransport(vertex) {
		t.Error("a publisher-model endpoint is Vertex even with no host rule")
	}
}
