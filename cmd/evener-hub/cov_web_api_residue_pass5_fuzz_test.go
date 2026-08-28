package hub

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func FuzzWebAPIResiduePass5(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Fuzz(func(t *testing.T, variant uint8) {
		web := NewWebServer(hubcore.WebConfig{})
		_ = formatTokenCount(12)
		web.lockForSession("a")
		web.lockForSession("a")
		next := false
		validAssetPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { next = true })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
		bad := httptest.NewRequest(http.MethodGet, "/", nil)
		bad.URL.Path = string([]byte{'/', 0xff})
		validAssetPath(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), bad)
		_ = next
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/new?dir=%20/tmp/x%20&prompt=hi", nil))
		web.handleIndex(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		web.manifestFS = fstest.MapFS{}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		web.manifestFS = fstest.MapFS{"manifest.webmanifest": {Data: []byte("{")}}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		web.manifestFS = fstest.MapFS{"manifest.webmanifest": {Data: []byte(`{"start_url":"/"}`), Mode: fs.FileMode(0o644)}}
		if variant&1 != 0 {
			web.cfg.AuthToken = "a b"
		}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))

		_ = canonicalRouteID("local:x")

		_ = evenerErrorInfoFromData(nil)
		_ = evenerErrorInfoFromData(map[string]any{"evenerErrorInfo": 1})
		webNil := NewWebServer(hubcore.WebConfig{})
		_ = webNil.apiStateGlob()
		_ = warningMessage([]byte(`{"warning":{}}`))
	})
}
