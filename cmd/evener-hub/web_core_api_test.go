package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func webCoreRequest(t *testing.T, web *WebServer, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)
	return rec
}

func TestWebCoreAPIHelpersAndRoutes(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{})
	firstLock := web.lockForSession("a")
	if firstLock != web.lockForSession("a") {
		t.Fatal("session lock was not stable")
	}
	for _, target := range []string{"/ok", "/%ff"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		validAssetPath(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(httptest.NewRecorder(), req)
	}
	for _, id := range []string{"bare", "local:thread", "remote:thread"} {
		_ = appRefFromRouteID(id)
		_ = canonicalRouteID(id)
	}

	// Pure wire/diagnostic helpers.
	for _, tc := range []struct {
		code int
		want int
	}{
		{appwire.CodeInvalidParams, 400}, {appwire.CodeInvalidRequest, 400},
		{appwire.CodeMethodNotFound, 404}, {appwire.CodeConflict, 409},
		{appwire.CodeUnavailable, 503}, {appwire.CodeInternalError, 500},
		{12345, 418},
	} {
		if got := statusForWireError(appwire.WireError{Code: tc.code}, 418); got != tc.want {
			t.Fatalf("statusForWireError(%d)=%d, want %d", tc.code, got, tc.want)
		}
	}
	if got := evenerErrorInfoFromData(map[string]any{"evenerErrorInfo": "map"}); got != "map" {
		t.Fatal(got)
	}
	if got := evenerErrorInfoFromData(map[string]any{"evenerErrorInfo": 2}); got != "" {
		t.Fatal(got)
	}
	if got := evenerErrorInfoFromData(nil); got != "" {
		t.Fatal(got)
	}
	if got := evenerErrorInfoFromData(appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInfo("typed")}); got != "typed" {
		t.Fatal(got)
	}
	writeAPIWireError(httptest.NewRecorder(), 502, errors.New("plain"))
	writeAPIWireError(httptest.NewRecorder(), 502, appwire.WireError{Code: appwire.CodeConflict, Message: "wire"})
	if gotP, gotM := splitProviderModel(" openai/model "); gotP != "openai" || gotM != "model" {
		t.Fatalf("split=%q/%q", gotP, gotM)
	}

	// Router and page branches.
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/new?dir=%20/tmp/x%20&prompt=hello"},
		{http.MethodGet, "/missing"},
		{http.MethodPost, "/_partials/workspace/empty"},
		{http.MethodGet, "/_partials/unknown"},
		{http.MethodGet, "/_partials/s/"},
		{http.MethodGet, "/_partials/s/id/unknown"},
		{http.MethodGet, "/manifest.webmanifest"},
		{http.MethodPost, "/api/health"}, {http.MethodGet, "/api/health"},
	} {
		_ = webCoreRequest(t, web, tc.method, tc.target, "")
	}
}
