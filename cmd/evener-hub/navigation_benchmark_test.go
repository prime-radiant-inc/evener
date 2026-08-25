package hub

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

//go:embed testdata/navigation_legacy_baseline.json
var navigationLegacyBaseline []byte

type navigationBaseline struct {
	ResponseBytes int64 `json:"response_bytes"`
	AllocsBytes   int64 `json:"allocs_bytes_per_op"`
	Projects      int   `json:"projects"`
	Sessions      int   `json:"sessions"`
}

const (
	legacyNavigationProjects           = 20
	legacyNavigationSessionsPerProject = 50
	legacyNavigationNow                = "2026-08-25T20:00:00Z"
)

func TestLegacyNavigationBaselineFixture(t *testing.T) {
	web := newNavigationBenchmarkFixture(t)
	body := requestLegacyTree(t, web)
	if !bytes.Contains(body, []byte(`"projects"`)) {
		t.Fatal("legacy fixture did not exercise project rows")
	}
	if got := countLegacySessions(t, body); got != 1000 {
		t.Fatalf("sessions=%d, want 1000", got)
	}

	var response hubapi.TreeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode legacy tree response: %v", err)
	}
	var want navigationBaseline
	if err := json.Unmarshal(navigationLegacyBaseline, &want); err != nil {
		t.Fatalf("decode navigation baseline: %v", err)
	}
	if got := int64(len(body)); got != want.ResponseBytes {
		t.Fatalf("response bytes=%d, want %d", got, want.ResponseBytes)
	}
	if got := len(response.Projects); got != want.Projects {
		t.Fatalf("projects=%d, want %d", got, want.Projects)
	}
	if got := countTreeProjectSessions(response); got != want.Sessions {
		t.Fatalf("sessions=%d, want %d", got, want.Sessions)
	}
}

func BenchmarkLegacyNavigationBaseline(b *testing.B) {
	web := newNavigationBenchmarkFixture(b)
	_ = requestLegacyTree(b, web)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = requestLegacyTree(b, web)
	}
}

func newNavigationBenchmarkFixture(tb testing.TB) *WebServer {
	tb.Helper()
	root := tb.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		tb.Fatalf("create projects root: %v", err)
	}
	now, err := time.Parse(time.RFC3339, legacyNavigationNow)
	if err != nil {
		tb.Fatalf("parse fixture time: %v", err)
	}
	nameSuffix := strings.Repeat("representative-title-data-", 7)
	metas := make([]schema.SessionMeta, 0, legacyNavigationProjects*legacyNavigationSessionsPerProject)
	for projectIndex := range legacyNavigationProjects {
		projectDir := filepath.Join(projectsRoot, fmt.Sprintf("project-%02d-0000000000", projectIndex))
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			tb.Fatalf("create project directory %q: %v", projectDir, err)
		}
		for sessionIndex := range legacyNavigationSessionsPerProject {
			id := fmt.Sprintf("session-%03d-%03d", projectIndex, sessionIndex)
			timestamp := now.Add(-time.Duration(projectIndex*legacyNavigationSessionsPerProject+sessionIndex) * time.Minute)
			meta := schema.SessionMeta{
				ID:        id,
				Name:      id + nameSuffix,
				CreatedAt: timestamp,
				UpdatedAt: timestamp,
				EnvInfo:   schema.EnvironmentInfo{WorkingDir: projectDir},
			}
			metas = append(metas, meta)
			if err := schema.SaveSessionMeta(projectDir, meta); err != nil {
				tb.Fatalf("save session metadata %q: %v", id, err)
			}
		}
	}
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)
	previousNow := hubNavigationNow
	hubNavigationNow = func() time.Time { return now }
	tb.Cleanup(func() { hubNavigationNow = previousNow })
	return NewWebServer(hubcore.WebConfig{
		HubStateRoot: filepath.Join(root, "hub"),
		Past:         past,
	})
}

func requestLegacyTree(tb testing.TB, web *WebServer) []byte {
	tb.Helper()
	recorder := httptest.NewRecorder()
	web.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/tree", nil))
	if recorder.Code != http.StatusOK {
		tb.Fatalf("GET /api/tree status=%d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	return append([]byte(nil), recorder.Body.Bytes()...)
}

func countLegacySessions(tb testing.TB, body []byte) int {
	tb.Helper()
	var response hubapi.TreeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		tb.Fatalf("decode legacy tree response: %v", err)
	}
	return countTreeProjectSessions(response)
}

func countTreeProjectSessions(response hubapi.TreeResponse) int {
	count := 0
	for _, project := range append(append(append([]hubapi.TreeProject{}, response.Projects...), response.ArchivedProjects...), response.TestRuns...) {
		count += len(project.Sessions)
	}
	return count
}
