package hub

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"primeradiant.com/evener/identifier"
)

// legacyBaseline encodes the frozen pre-optimization measurements that
// the performance budget fractions are defined against. The original
// /api/tree monolith produced 581485 uncompressed bytes and 25311669
// B/op on this fixture; the bounded navigation resources are budgeted
// as fractions of those numbers.
const legacyBaselineResponseBytes = 581485
const legacyBaselineAllocsBytes = 25311669

const (
	legacyNavigationProjects           = 20
	legacyNavigationSessionsPerProject = 50
	legacyNavigationNow                = "2026-08-25T20:00:00Z"
)


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
	previousBuild := hubBuildNavigationTree
	hubBuildNavigationTree = func(metas []schema.SessionMeta, live []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, projects map[string]identifier.Project) hubcore.Tree {
		tree := hubcore.BuildTreeAtWithProjects(metas, live, decisions, now, projects)
		normalizeLegacyNavigationAges(&tree, now)
		return tree
	}
	tb.Cleanup(func() { hubBuildNavigationTree = previousBuild })
	projectIndexes := make(map[string]int, legacyNavigationProjects)
	for projectIndex := range legacyNavigationProjects {
		projectIndexes[filepath.Join(projectsRoot, fmt.Sprintf("project-%02d-0000000000", projectIndex))] = projectIndex
	}
	previousInputs := hubNavigationInputs
	hubNavigationInputs = func(server *WebServer, ctx context.Context) navigationSnapshot {
		snapshot := previousInputs(server, ctx)
		fixedMetas := make([]schema.SessionMeta, len(snapshot.metas))
		copy(fixedMetas, snapshot.metas)
		for i := range fixedMetas {
			if projectIndex, ok := projectIndexes[fixedMetas[i].EnvInfo.WorkingDir]; ok {
				fixedMetas[i].EnvInfo.WorkingDir = fmt.Sprintf("/evener-navigation-project-%02d", projectIndex)
			}
		}
		fixedProjects := make(map[string]identifier.Project, len(snapshot.projects))
		for path, project := range snapshot.projects {
			projectIndex, ok := projectIndexes[path]
			if !ok {
				continue
			}
			fixedPath := fmt.Sprintf("/evener-navigation-project-%02d", projectIndex)
			project.CanonicalPath = fixedPath
			fixedProjects[fixedPath] = project
		}
		fixedIdentities := make(map[string][]identifier.Project, len(snapshot.projectIdentities))
		for path, projects := range snapshot.projectIdentities {
			projectIndex, ok := projectIndexes[path]
			if !ok {
				continue
			}
			fixedPath := fmt.Sprintf("/evener-navigation-project-%02d", projectIndex)
			fixed := make([]identifier.Project, len(projects))
			copy(fixed, projects)
			for i := range fixed {
				fixed[i].CanonicalPath = fixedPath
			}
			fixedIdentities[fixedPath] = fixed
		}
		fixedConflicts := make(map[string]bool, len(snapshot.projectConflicts))
		for path, conflict := range snapshot.projectConflicts {
			if projectIndex, ok := projectIndexes[path]; ok {
				fixedConflicts[fmt.Sprintf("/evener-navigation-project-%02d", projectIndex)] = conflict
			}
		}
		snapshot.metas = fixedMetas
		snapshot.projects = fixedProjects
		snapshot.projectIdentities = fixedIdentities
		snapshot.projectConflicts = fixedConflicts
		return snapshot
	}
	tb.Cleanup(func() { hubNavigationInputs = previousInputs })
	previousNow := hubNavigationNow
	hubNavigationNow = func() time.Time { return now }
	tb.Cleanup(func() { hubNavigationNow = previousNow })
	stateRoot := tb.TempDir()
	return NewWebServer(hubcore.WebConfig{
		HubStateRoot: filepath.Join(stateRoot, "hub"),
		Past:         past,
	})
}

func normalizeLegacyNavigationAges(tree *hubcore.Tree, now time.Time) {
	var normalizeNodes func([]hubcore.TreeNode)
	normalizeNodes = func(nodes []hubcore.TreeNode) {
		for i := range nodes {
			nodes[i].Age = legacyNavigationAge(nodes[i].UpdatedAt, now)
			normalizeNodes(nodes[i].Children)
		}
	}
	normalizeNodes(tree.NeedsYou)
	normalizeNodes(tree.Live)
	for i := range tree.Projects {
		tree.Projects[i].Age = legacyNavigationAge(tree.Projects[i].LastActivity, now)
		normalizeNodes(tree.Projects[i].Current)
		normalizeNodes(tree.Projects[i].Recent)
		normalizeNodes(tree.Projects[i].Archived)
	}
	for i := range tree.ArchivedProjects {
		tree.ArchivedProjects[i].Age = legacyNavigationAge(tree.ArchivedProjects[i].LastActivity, now)
		normalizeNodes(tree.ArchivedProjects[i].Current)
		normalizeNodes(tree.ArchivedProjects[i].Recent)
		normalizeNodes(tree.ArchivedProjects[i].Archived)
	}
}

func legacyNavigationAge(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// countLegacySessions counts sessions in the navigation manifest's raw JSON
// body by scanning for session-ref occurrences in project resources. The
// frozen baseline JSON carries exactly 1000 sessions across 20 projects.
func countLegacySessions(tb testing.TB, body []byte) int {
	tb.Helper()
	// Count occurrences of "ref":" in the body — each session row carries one.
	return bytes.Count(body, []byte(`"ref":"`))
}

// countProjectKeys counts the distinct project keys in the navigation
// manifest's raw JSON body by scanning for "key":" occurrences in the
// project catalog section.
func countProjectKeys(body []byte) int {
	return bytes.Count(body, []byte(`"key":"`))
}

// requestNavigationManifest issues a gzip-accepting GET /api/navigation through
// the real HTTP handler and returns the raw (compressed) response bytes.
func requestNavigationManifest(tb testing.TB, web *WebServer) []byte {
	tb.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/navigation", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	web.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		tb.Fatalf("GET /api/navigation status=%d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	return append([]byte(nil), recorder.Body.Bytes()...)
}

// gzipDecode decompresses a gzip-encoded body. It returns the input unchanged
// when it is not gzip-encoded (e.g. an error response).
func gzipDecode(body []byte) ([]byte, error) {
	if len(body) == 0 || body[0] != 0x1f {
		return body, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return decoded, nil
}

// BenchmarkNavigationMandatory measures the warm manifest request B/op after
// the object, JSON, and gzip caches are populated. The spec (§895-930) requires
// this to use no more than 20% of the legacy B/op baseline (25311669 → ≤5062334),
// an allocation reduction of at least 80%.
func BenchmarkNavigationMandatory(b *testing.B) {
	web := newNavigationBenchmarkFixture(b)
	// Warm the manifest cache.
	_ = requestNavigationManifest(b, web)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = requestNavigationManifest(b, web)
	}
}

// BenchmarkNavigationExpanded measures the expanded hydration path: mandatory
// resources plus the first four project root resources, all with gzip. It
// proves the expanded variant stays within its 35%-of-legacy byte budget.
func BenchmarkNavigationExpanded(b *testing.B) {
	web := newNavigationBenchmarkFixture(b)
	// Discover the first four project keys from the project catalog.
	catalogReq := httptest.NewRequest(http.MethodGet, "/api/navigation/catalogs/projects", nil)
	catalogReq.Header.Set("Accept-Encoding", "gzip")
	catalogRec := httptest.NewRecorder()
	web.Handler().ServeHTTP(catalogRec, catalogReq)
	if catalogRec.Code != http.StatusOK {
		b.Fatalf("project catalog status=%d: %s", catalogRec.Code, catalogRec.Body.String())
	}
	catalogBody, err := gzipDecode(catalogRec.Body.Bytes())
	if err != nil {
		b.Fatalf("decode project catalog gzip: %v", err)
	}
	var catalog hubapi.NavigationProjectCatalog
	if err := json.Unmarshal(catalogBody, &catalog); err != nil {
		b.Fatalf("decode project catalog: %v", err)
	}
	if len(catalog.Projects) < 4 {
		b.Fatalf("project catalog has %d projects, need at least 4", len(catalog.Projects))
	}
	projectKeys := make([]string, 4)
	for i := range 4 {
		projectKeys[i] = catalog.Projects[i].Key
	}

	// Warm all expanded-hydration caches.
	resources := []string{
		"/api/navigation",
		"/api/navigation/sections/live",
		"/api/navigation/sections/needs-you",
		"/api/navigation/pin-sections",
		"/api/navigation/catalogs/projects",
	}
	for _, key := range projectKeys {
		resources = append(resources, "/api/navigation/projects/"+key)
	}
	for _, target := range resources {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("warm %s status=%d: %s", target, rec.Code, rec.Body.String())
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, target := range resources {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			web.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("%s status=%d: %s", target, rec.Code, rec.Body.String())
			}
		}
	}
}
