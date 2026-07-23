package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
)

// FuzzProviderRenderPass4 drives the display-only provider/settings surface
// with a fully contained filesystem. It intentionally calls no ordinary tests.
func FuzzProviderRenderPass4(f *testing.F) {
	f.Add(uint8(0), "alpha", "useful skill")
	f.Add(uint8(1), "../odd", "")
	f.Add(uint8(2), "local/model-20251101", "description")
	f.Add(uint8(3), "source/thread", "<tag>")
	f.Fuzz(func(t *testing.T, mode uint8, raw, desc string) {
		_ = desc
		root := t.TempDir()
		xdg := filepath.Join(root, "config")
		t.Setenv("XDG_CONFIG_HOME", xdg)

		file := filepath.Join(root, "sized")
		sizes := []int{3, 2048, 2 << 20, 2 << 30}
		if err := os.WriteFile(file, bytes.Repeat([]byte{'x'}, min(sizes[int(mode)%len(sizes)], 2<<20)), 0o600); err != nil {
			t.Fatal(err)
		}
		ages := []time.Duration{time.Second, 10 * time.Minute, 3 * time.Hour, 72 * time.Hour}
		_ = os.Chtimes(file, time.Now().Add(-ages[int(mode)%len(ages)]), time.Now().Add(-ages[int(mode)%len(ages)]))
		_ = fileAgeHuman(file)
		_ = fileAgeHuman(filepath.Join(root, "missing"))
		_ = fileSizeHuman(file)
		_ = fileSizeHuman(filepath.Join(root, "missing"))
		_ = tildeHome(file)
		_ = defaultMCPConfigPath()

		web := NewWebServer(hubcore.WebConfig{AuthToken: raw})
		_ = serfUsageFromCumulative(structToCumulative(mode))

		for _, id := range []string{raw, "local/" + raw, "remote/" + raw} {
			_ = localAppRef(id)
			_ = appRefFromRouteID(id)
			_ = isLocalRouteID(id)
			_ = canonicalRouteID(id)
		}
		_, _ = splitProviderModel(raw)
		_, _ = splitProviderModel("provider/" + raw)
		_ = isDatedSnapshotModelID(raw)

		requests := []*http.Request{
			httptest.NewRequest(http.MethodGet, "/", nil),
			httptest.NewRequest(http.MethodGet, "/new?dir="+filepath.ToSlash(root)+"&prompt=x", nil),
			httptest.NewRequest(http.MethodGet, "/settings/launch", nil),
			httptest.NewRequest(http.MethodGet, "/settings/project?cwd="+filepath.ToSlash(root), nil),
		}
		for _, req := range requests {
			web.handleIndex(httptest.NewRecorder(), req)
			web.handleSettings(httptest.NewRecorder(), req)
		}
		web.handleManifest(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
		web.handleCredentials(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/credentials", nil))

		var modelOut httptest.ResponseRecorder
		modelOut = *httptest.NewRecorder()
		writeModelsResponse(&modelOut, nil, nil, nil, true)
		modelOut = *httptest.NewRecorder()
		writeModelsResponse(&modelOut, []map[string]any{{"id": raw}}, nil, nil, false)
		writeSpawnError(httptest.NewRecorder(), appwire.InvalidParams(raw))
		writeSpawnError(httptest.NewRecorder(), appwire.Unavailable(raw))
		writeSpawnError(httptest.NewRecorder(), errors.New(raw))
	})
}

func structToCumulative(mode uint8) (u schema.CumulativeUsage) {
	if mode&1 != 0 {
		u.InputTokens = int64(mode)
	}
	if mode&2 != 0 {
		u.OutputTokens = int64(mode)
	}
	if mode&4 != 0 {
		u.CacheReadTokens = int64(mode)
	}
	u.TotalTokens = u.InputTokens + u.OutputTokens
	return u
}
