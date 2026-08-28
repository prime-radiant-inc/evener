package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
)

// --- web.go: lockForSession ---

// TestCovLockForSessionCreatesRegistry covers the path where ResumeLocks is
// already initialized by NewWebServer (web.go:96).
func TestCovLockForSessionCreatesRegistry(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	if web.cfg.ResumeLocks == nil {
		t.Fatal("ResumeLocks should be initialized by NewWebServer")
	}
	lock := web.lockForSession("s1")
	if lock == nil {
		t.Fatal("expected non-nil lock")
	}
	// Second call returns the same registry's lock.
	lock2 := web.lockForSession("s1")
	if lock2 != lock {
		t.Fatal("second call should return the same lock")
	}
}

// TestCovLockForSessionWithExistingRegistry covers the path where ResumeLocks
// is already set (web.go:96).
func TestCovLockForSessionWithExistingRegistry(t *testing.T) {
	registry := hubcore.NewResumeLocks()
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:     "127.0.0.1:9180",
		ResumeLocks: registry,
	})
	lock := web.lockForSession("s1")
	if web.cfg.ResumeLocks != registry {
		t.Fatal("NewWebServer replaced the configured resume-lock registry")
	}
	if want := registry.For("s1"); lock != want {
		t.Fatalf("lockForSession = %p, configured registry lock = %p", lock, want)
	}
}

// --- web_api_tree.go: mergeFavoriteAuthorityQuality ---

// TestCovMergeFavoriteAuthorityQualityAmbiguous covers the ambiguous branch
// (web_api_tree.go:1499-1500).
func TestCovMergeFavoriteAuthorityQualityAmbiguous(t *testing.T) {
	for _, left := range []hubcore.FavoriteAuthorityQuality{
		hubcore.FavoriteAuthorityComplete,
		hubcore.FavoriteAuthorityIncomplete,
		hubcore.FavoriteAuthorityAmbiguous,
	} {
		if got := mergeFavoriteAuthorityQuality(left, hubcore.FavoriteAuthorityAmbiguous); got != hubcore.FavoriteAuthorityAmbiguous {
			t.Errorf("merge with ambiguous right = %v, want ambiguous", got)
		}
		if got := mergeFavoriteAuthorityQuality(hubcore.FavoriteAuthorityAmbiguous, left); got != hubcore.FavoriteAuthorityAmbiguous {
			t.Errorf("merge with ambiguous left = %v, want ambiguous", got)
		}
	}
}

// TestCovMergeFavoriteAuthorityQualityIncomplete covers the incomplete branch
// (web_api_tree.go:1502-1503).
func TestCovMergeFavoriteAuthorityQualityIncomplete(t *testing.T) {
	if got := mergeFavoriteAuthorityQuality(hubcore.FavoriteAuthorityIncomplete, hubcore.FavoriteAuthorityComplete); got != hubcore.FavoriteAuthorityIncomplete {
		t.Errorf("merge incomplete+complete = %v, want incomplete", got)
	}
	if got := mergeFavoriteAuthorityQuality(hubcore.FavoriteAuthorityComplete, hubcore.FavoriteAuthorityIncomplete); got != hubcore.FavoriteAuthorityIncomplete {
		t.Errorf("merge complete+incomplete = %v, want incomplete", got)
	}
}

// TestCovMergeFavoriteAuthorityQualityBothComplete covers the both-complete
// branch (web_api_tree.go:1505-1506).
func TestCovMergeFavoriteAuthorityQualityBothComplete(t *testing.T) {
	if got := mergeFavoriteAuthorityQuality(hubcore.FavoriteAuthorityComplete, hubcore.FavoriteAuthorityComplete); got != hubcore.FavoriteAuthorityComplete {
		t.Errorf("merge complete+complete = %v, want complete", got)
	}
}

// TestCovMergeFavoriteAuthorityQualityFallback covers the fallback incomplete
// branch (web_api_tree.go:1508).
func TestCovMergeFavoriteAuthorityQualityFallback(t *testing.T) {
	// The only remaining combination where neither is ambiguous, neither is
	// incomplete, and they aren't both complete would require a zero-value.
	// But FavoriteAuthorityQuality values are Complete, Incomplete, Ambiguous.
	// So the fallback covers a default (zero-value) quality.
	var zero hubcore.FavoriteAuthorityQuality
	if got := mergeFavoriteAuthorityQuality(zero, zero); got != hubcore.FavoriteAuthorityIncomplete {
		t.Errorf("merge zero+zero = %v, want incomplete (fallback)", got)
	}
}

// --- web_api_tree.go: favoriteProjectSourceClaim ---

// TestCovFavoriteProjectSourceClaimRemote covers the remote ownership path
// (web_api_tree.go:1512-1515).
func TestCovFavoriteProjectSourceClaimRemote(t *testing.T) {
	snap := navigationSnapshot{
		remoteOwnership: map[string]favoriteRemoteOwnership{
			"remote1:s1": {sourceID: "remote1", complete: true},
		},
	}
	if got := favoriteProjectSourceClaim("remote1:s1", snap); got != "remote1" {
		t.Fatalf("expected remote1, got %q", got)
	}
}

// TestCovFavoriteProjectSourceClaimRemoteIncomplete covers the remote
// ownership with empty sourceID (web_api_tree.go:1515-1516).
func TestCovFavoriteProjectSourceClaimRemoteIncomplete(t *testing.T) {
	snap := navigationSnapshot{
		remoteOwnership: map[string]favoriteRemoteOwnership{
			"remote1:s1": {sourceID: "", complete: false},
		},
	}
	if got := favoriteProjectSourceClaim("remote1:s1", snap); got != "remote-incomplete" {
		t.Fatalf("expected remote-incomplete, got %q", got)
	}
}

// TestCovFavoriteProjectSourceClaimLocal covers the local fallback
// (web_api_tree.go:1518).
func TestCovFavoriteProjectSourceClaimLocal(t *testing.T) {
	snap := navigationSnapshot{}
	if got := favoriteProjectSourceClaim("local:s1", snap); got != "local" {
		t.Fatalf("expected local, got %q", got)
	}
}

// --- web_api_tree.go: favoriteSessionSourceQuality ---

// TestCovFavoriteSessionSourceQualityEmpty covers the empty ID path
// (web_api_tree.go:1255-1256).
func TestCovFavoriteSessionSourceQualityEmpty(t *testing.T) {
	if got := favoriteSessionSourceQuality("", nil, nil, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete for empty id, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityIncompleteID covers the incompleteIDs
// path (web_api_tree.go:1258-1259).
func TestCovFavoriteSessionSourceQualityIncompleteID(t *testing.T) {
	incomplete := map[string]struct{}{"remote1:s1": {}}
	if got := favoriteSessionSourceQuality("remote1:s1", nil, nil, incomplete); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityLocal covers the non-remote path
// (web_api_tree.go:1262-1263).
func TestCovFavoriteSessionSourceQualityLocal(t *testing.T) {
	if got := favoriteSessionSourceQuality("local:s1", nil, nil, nil); got != hubcore.FavoriteAuthorityComplete {
		t.Fatalf("expected complete for local, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityRemoteIncomplete covers the incomplete
// remote ownership path (web_api_tree.go:1265-1266).
func TestCovFavoriteSessionSourceQualityRemoteIncomplete(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: false},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, nil, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityUnparseableRef covers the unparseable ref
// path (web_api_tree.go:1268-1270).
func TestCovFavoriteSessionSourceQualityUnparseableRef(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"not-a-ref": {sourceID: "remote1", complete: true},
	}
	if got := favoriteSessionSourceQuality("not-a-ref", ownership, nil, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualitySourceMismatch covers the source mismatch
// path (web_api_tree.go:1272-1273).
func TestCovFavoriteSessionSourceQualitySourceMismatch(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote2", complete: true},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, nil, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualitySourceUnknown covers the unknown source
// path (web_api_tree.go:1275-1276).
func TestCovFavoriteSessionSourceQualitySourceUnknown(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: true},
	}
	sources := map[string]hubcore.RemoteSourceSnapshot{}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, sources, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete for unknown source, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualitySourceIncomplete covers the incomplete
// source path (web_api_tree.go:1276-1277).
func TestCovFavoriteSessionSourceQualitySourceIncomplete(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: true},
	}
	sources := map[string]hubcore.RemoteSourceSnapshot{
		"remote1": {Complete: false},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, sources, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete for incomplete source, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityMatchingThread covers the matching thread
// path (web_api_tree.go:1279-1287).
func TestCovFavoriteSessionSourceQualityMatchingThread(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: true},
	}
	sources := map[string]hubcore.RemoteSourceSnapshot{
		"remote1": {
			Complete: true,
			Threads: []appwire.Thread{
				{ID: "s1", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
			},
		},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, sources, nil); got != hubcore.FavoriteAuthorityComplete {
		t.Fatalf("expected complete for matching thread, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityNoMatchingThread covers the no-matching-
// thread path (web_api_tree.go:1288).
func TestCovFavoriteSessionSourceQualityNoMatchingThread(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: true},
	}
	sources := map[string]hubcore.RemoteSourceSnapshot{
		"remote1": {
			Complete: true,
			Threads: []appwire.Thread{
				{ID: "other", Source: "remote1", Evener: appwire.EvenerThread{Ref: "remote1:other"}},
			},
		},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, sources, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("expected incomplete for no matching thread, got %v", got)
	}
}

// TestCovFavoriteSessionSourceQualityThreadWithDifferentSource covers the
// path where a thread's Source differs from the ref SourceID (web_api_tree.go:1280-1281).
func TestCovFavoriteSessionSourceQualityThreadWithDifferentSource(t *testing.T) {
	ownership := map[string]favoriteRemoteOwnership{
		"remote1:s1": {sourceID: "remote1", complete: true},
	}
	sources := map[string]hubcore.RemoteSourceSnapshot{
		"remote1": {
			Complete: true,
			Threads: []appwire.Thread{
				{ID: "s1", Source: "other", Evener: appwire.EvenerThread{Ref: "remote1:s1"}},
			},
		},
	}
	if got := favoriteSessionSourceQuality("remote1:s1", ownership, sources, nil); got != hubcore.FavoriteAuthorityIncomplete {
		t.Fatalf("mismatched thread source quality = %v, want incomplete", got)
	}
}

// --- web_api_tree.go: favoriteSessionAliases ---

// TestCovFavoriteSessionAliasesLocalRef covers the local ref path
// (web_api_tree.go:1246-1247).
func TestCovFavoriteSessionAliasesLocalRef(t *testing.T) {
	aliases := favoriteSessionAliases("local:02wMz5Txv1C3Hut0M8GCeB")
	want := []string{"local:02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv1C3Hut0M8GCeB"}
	if !reflect.DeepEqual(aliases, want) {
		t.Fatalf("aliases = %v, want exact ordered aliases %v", aliases, want)
	}
}

// TestCovFavoriteSessionAliasesBareID covers the bare-ID path
// (web_api_tree.go:1248-1249).
func TestCovFavoriteSessionAliasesBareID(t *testing.T) {
	aliases := favoriteSessionAliases("02wMz5Txv1C3Hut0M8GCeB")
	want := []string{"02wMz5Txv1C3Hut0M8GCeB", "local:02wMz5Txv1C3Hut0M8GCeB"}
	if !reflect.DeepEqual(aliases, want) {
		t.Fatalf("aliases = %v, want exact ordered aliases %v", aliases, want)
	}
}

// TestCovFavoriteSessionAliasesNonLocalRef covers the non-local ref path
// (no local: prefix, has colon).
func TestCovFavoriteSessionAliasesNonLocalRef(t *testing.T) {
	aliases := favoriteSessionAliases("remote1:s1")
	want := []string{"remote1:s1"}
	if !reflect.DeepEqual(aliases, want) {
		t.Fatalf("aliases = %v, want %v", aliases, want)
	}
}

// --- web_api_tree.go: addNavigationProjectCandidate ---

// TestCovAddNavigationProjectCandidateEmptyPath covers the empty path guard
// (web_api_tree.go:651).
func TestCovAddNavigationProjectCandidateEmptyPath(t *testing.T) {
	candidates := map[string]map[string]identifier.Project{}
	addNavigationProjectCandidate(candidates, "", identifier.Project{ID: "p1"})
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates for empty path, got %d", len(candidates))
	}
}

// TestCovAddNavigationProjectCandidateEmptyProjectID covers the empty project ID
// guard (web_api_tree.go:651).
func TestCovAddNavigationProjectCandidateEmptyProjectID(t *testing.T) {
	candidates := map[string]map[string]identifier.Project{}
	addNavigationProjectCandidate(candidates, "/path", identifier.Project{ID: ""})
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates for empty project ID, got %d", len(candidates))
	}
}

// TestCovAddNavigationProjectCandidateNewPath covers the new-path branch
// (web_api_tree.go:654-655).
func TestCovAddNavigationProjectCandidateNewPath(t *testing.T) {
	candidates := map[string]map[string]identifier.Project{}
	project := identifier.Project{ID: "p1", CanonicalPath: "/path"}
	addNavigationProjectCandidate(candidates, "/path", project)
	want := map[string]map[string]identifier.Project{
		"/path": {"p1\x00/path": project},
	}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("candidates = %#v, want exact insertion %#v", candidates, want)
	}
}

// --- web_api_tree.go: selectNavigationProjects ---

// TestCovSelectNavigationProjectsConflict covers the conflict path
// (web_api_tree.go:678-679).
func TestCovSelectNavigationProjectsConflict(t *testing.T) {
	candidates := map[string]map[string]identifier.Project{
		"/path": {
			"a\x00/path": identifier.Project{ID: "a", CanonicalPath: "/path"},
			"b\x00/path": identifier.Project{ID: "b", CanonicalPath: "/path"},
		},
	}
	projects, identities, conflicts := selectNavigationProjects(candidates)
	wantA := identifier.Project{ID: "a", CanonicalPath: "/path"}
	wantB := identifier.Project{ID: "b", CanonicalPath: "/path"}
	if want := map[string]identifier.Project{"/path": wantA}; !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects = %#v, want %#v", projects, want)
	}
	if want := map[string][]identifier.Project{"/path": {wantA, wantB}}; !reflect.DeepEqual(identities, want) {
		t.Fatalf("identities = %#v, want sorted identities %#v", identities, want)
	}
	if want := map[string]bool{"/path": true}; !reflect.DeepEqual(conflicts, want) {
		t.Fatalf("conflicts = %#v, want %#v", conflicts, want)
	}
}

// TestCovSelectNavigationProjectsEmpty covers the empty-candidates path
// (web_api_tree.go:674-675).
func TestCovSelectNavigationProjectsEmpty(t *testing.T) {
	candidates := map[string]map[string]identifier.Project{
		"/path": {},
	}
	projects, identities, conflicts := selectNavigationProjects(candidates)
	if !reflect.DeepEqual(projects, map[string]identifier.Project{}) {
		t.Fatalf("projects = %#v, want empty map", projects)
	}
	wantIdentities := map[string][]identifier.Project{}
	if !reflect.DeepEqual(identities, wantIdentities) {
		t.Fatalf("identities = %#v, want %#v", identities, wantIdentities)
	}
	if !reflect.DeepEqual(conflicts, map[string]bool{}) {
		t.Fatalf("conflicts = %#v, want empty map", conflicts)
	}
}

// --- web.go: splitProviderModel ---

// TestCovSplitProviderModelWithSlash covers the provider/model split path.
func TestCovSplitProviderModelWithSlash(t *testing.T) {
	provider, model := splitProviderModel("openai/gpt-4")
	if provider != "openai" || model != "gpt-4" {
		t.Fatalf("expected openai/gpt-4, got %q/%q", provider, model)
	}
}

// TestCovSplitProviderModelNoSlash covers the no-slash path.
func TestCovSplitProviderModelNoSlash(t *testing.T) {
	provider, model := splitProviderModel("  gpt-4  ")
	if provider != "" || model != "gpt-4" {
		t.Fatalf("expected /gpt-4, got %q/%q", provider, model)
	}
}

// --- doc_serve.go: docRawTotalSize ---

// TestCovDocRawTotalSizeStatError covers the fallback to read size when stat
// fails (doc_serve.go:230-231).
func TestCovDocRawTotalSizeStatError(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing", "document.txt")
	got := docRawTotalSize(missingPath, 42)
	if got != 42 {
		t.Fatalf("expected 42 (read fallback), got %d", got)
	}
}

// TestCovDocRawTotalSizeStatOK covers the stat-success path (doc_serve.go:229).
func TestCovDocRawTotalSizeStatOK(t *testing.T) {
	payload := []byte("known document bytes")
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := docRawTotalSize(path, 1), int64(len(payload)); got != want {
		t.Fatalf("docRawTotalSize = %d, want stat size %d", got, want)
	}
}

// --- web_api.go: handleAPIClear (method check) ---

// TestCovHandleAPIClearMethodNotAllowed covers the method-not-allowed branch
// (web_api.go:261-262).
func TestCovHandleAPIClearMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/clear", nil)
	rec := httptest.NewRecorder()
	web.handleAPIClear(rec, req, "s1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- web_api.go: handleAPIModel (method check) ---

// TestCovHandleAPIModelMethodNotAllowed covers the method-not-allowed branch
// (web_api.go:306-307).
func TestCovHandleAPIModelMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/model", nil)
	rec := httptest.NewRecorder()
	web.handleAPIModel(rec, req, "s1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestCovHandleAPIModelNotLive covers the not-live branch
// (web_api.go:310-311).
func TestCovHandleAPIModelNotLive(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/02wMz5Txv1C3Hut0M8GCeB/model", strings.NewReader(`{"model":"test"}`))
	rec := httptest.NewRecorder()
	web.handleAPIModel(rec, req, "02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- web_api.go: handleAPIReasoningEffort (method check) ---

// TestCovHandleAPIReasoningEffortMethodNotAllowed covers the method-not-allowed
// branch (web_api.go:350-351).
func TestCovHandleAPIReasoningEffortMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/reasoning", nil)
	rec := httptest.NewRecorder()
	web.handleAPIReasoningEffort(rec, req, "s1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestCovHandleAPIReasoningEffortNotLive covers the not-live branch
// (web_api.go:354-355).
func TestCovHandleAPIReasoningEffortNotLive(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/02wMz5Txv1C3Hut0M8GCeB/reasoning", strings.NewReader(`{"reasoning_effort":"high"}`))
	rec := httptest.NewRecorder()
	web.handleAPIReasoningEffort(rec, req, "02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- web_api_pin_section.go: handleAPIPinSections (method/config check) ---

// TestCovHandleAPIPinSectionsMethodNotAllowed covers the method-not-allowed
// branch (web_api_pin_section.go:28-29).
func TestCovHandleAPIPinSectionsMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/pin-sections", nil)
	rec := httptest.NewRecorder()
	web.handleAPIPinSections(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestCovHandleAPIPinSectionsNotConfigured covers the not-configured branch
// (web_api_pin_section.go:32-33).
func TestCovHandleAPIPinSectionsNotConfigured(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/pin-sections", nil)
	rec := httptest.NewRecorder()
	web.handleAPIPinSections(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// --- web_api_rename.go: handleAPIRename ---

// TestCovHandleAPIRenameMethodNotAllowed covers the method-not-allowed branch
// (web_api_rename.go:26-27).
func TestCovHandleAPIRenameMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/rename", nil)
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "s1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestCovHandleAPIRenameInvalidJSON covers the invalid-JSON branch
// (web_api_rename.go:33-34).
func TestCovHandleAPIRenameInvalidJSON(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/rename", strings.NewReader("bad json"))
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "s1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// TestCovHandleAPIRenameEmptyName covers the empty-name branch
// (web_api_rename.go:38-39).
func TestCovHandleAPIRenameEmptyName(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	body := `{"name":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/s1/rename", strings.NewReader(body))
	rec := httptest.NewRecorder()
	web.handleAPIRename(rec, req, "s1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- web_api_session_delete.go: handleAPISessionDelete ---

// TestCovHandleAPISessionDeleteMethodNotAllowed covers the method-not-allowed
// branch (web_api_session_delete.go:38-39).
func TestCovHandleAPISessionDeleteMethodNotAllowed(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/delete", nil)
	rec := httptest.NewRecorder()
	web.handleAPISessionDelete(rec, req, "s1")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestCovHandleAPISessionDeleteNonLocal covers the non-local-id branch
// (web_api_session_delete.go:42-43).
func TestCovHandleAPISessionDeleteNonLocal(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/remote:s1/delete", nil)
	rec := httptest.NewRecorder()
	web.handleAPISessionDelete(rec, req, "remote:s1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// TestCovHandleAPISessionDeleteInvalidIDShortCircuit pins the route guard: an
// invalid bare ID is rejected before the Past-index configuration is read.
func TestCovHandleAPISessionDeleteInvalidIDShortCircuit(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/invalid-id/delete", nil)
	rec := httptest.NewRecorder()
	web.handleAPISessionDelete(rec, req, "invalid-id")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var got hubapi.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode invalid-ID response: %v", err)
	}
	want := hubapi.ErrorResponse{Error: "only local sessions can be deleted"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-ID response = %#v, want %#v", got, want)
	}
}

// TestCovHandleAPISessionDeleteNoPast covers the no-past-index branch
// (web_api_session_delete.go:51-52).
func TestCovHandleAPISessionDeleteNoPast(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/02wMz5Txv1C3Hut0M8GCeB/delete", nil)
	rec := httptest.NewRecorder()
	web.handleAPISessionDelete(rec, req, "02wMz5Txv1C3Hut0M8GCeB")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// --- web_api_pin_section.go: topLevelFavoriteSessionID ---

// TestCovTopLevelFavoriteSessionIDClusterPrefix covers the cluster-prefix
// rejection (web_api_pin_section.go).
func TestCovTopLevelFavoriteSessionIDClusterPrefix(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	if _, ok := web.topLevelFavoriteSessionID(context.TODO(), "cluster:foo"); ok {
		t.Fatal("cluster: prefix should return false")
	}
}

// --- web_api_tree.go: apiTreeSources ---

// TestCovAPITreeSourcesLocalOnly covers the sources path when only the local
// source is configured (web_api_tree.go:970-977).
func TestCovAPITreeSourcesLocalOnly(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	sources := web.apiTreeSources()
	want := []hubapi.Source{{ID: "local", Label: "this host", Kind: "local", Online: true}}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %#v, want %#v", sources, want)
	}
}

// --- web_api_project_delete.go: removeProjectSessionRendezvous ---

// TestCovRemoveProjectSessionRendezvousEmptyRunDir covers the empty runDir
// guard (web_api_project_delete.go:477-478).
func TestCovRemoveProjectSessionRendezvousEmptyRunDir(t *testing.T) {
	if err := removeProjectSessionRendezvous("", "s1"); err != nil {
		t.Fatalf("expected nil error for empty runDir, got %v", err)
	}
}

// --- web_api_project_delete.go: removeProjectSessionDaemonLog ---

// TestCovRemoveProjectSessionDaemonLogEmptyRunDir covers the empty runDir
// guard (web_api_project_delete.go:511-512).
func TestCovRemoveProjectSessionDaemonLogEmptyRunDir(t *testing.T) {
	if err := removeProjectSessionDaemonLog("", "s1"); err != nil {
		t.Fatalf("expected nil error for empty runDir, got %v", err)
	}
}

// --- web_api_project_delete.go: resumeProjectDeletions ---

// TestCovResumeProjectDeletionsNoStore covers the nil-store path
// (web_api_project_delete.go:254-255).
func TestCovResumeProjectDeletionsNoStore(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180"})
	if err := web.resumeProjectDeletions(); err != nil {
		t.Fatalf("expected nil error for no deletion store, got %v", err)
	}
}

// TestCovResumeProjectDeletionsWithEmptyStore covers the path where the store
// has no in-progress deletions (web_api_project_delete.go:258).
func TestCovResumeProjectDeletionsWithEmptyStore(t *testing.T) {
	dir := t.TempDir()
	store, err := hubcore.NewDeletionStore(dir)
	if err != nil {
		t.Fatalf("NewDeletionStore: %v", err)
	}
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		DeletionStore: store,
	})
	if err := web.resumeProjectDeletions(); err != nil {
		t.Fatalf("expected nil error for empty store, got %v", err)
	}
}

// --- web_api_tree.go: hubDetailFromAppThread ---

// TestCovHubDetailFromAppThreadEmptyTitle covers the title fallback chain
// (web_api_tree.go:1040-1046).
func TestCovHubDetailFromAppThreadEmptyTitle(t *testing.T) {
	thread := appwire.Thread{
		ID:        "02wMz5Txv1C3Hut0M8GCeB",
		SessionID: "02wMz5Txv1C3Hut0M8GCeB",
		Status:    appwire.ThreadStatus{Type: "idle"},
		CWD:       "/projects/test",
	}
	detail := hubDetailFromAppThread(thread)
	if detail.Title != "02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("expected session ID as title, got %q", detail.Title)
	}
	if detail.Project != "test" {
		t.Fatalf("expected project 'test', got %q", detail.Project)
	}
}

// TestCovHubDetailFromAppThreadPreviewTitle covers the preview title fallback
// (web_api_tree.go:1042-1043).
func TestCovHubDetailFromAppThreadPreviewTitle(t *testing.T) {
	thread := appwire.Thread{
		ID:      "02wMz5Txv1C3Hut0M8GCeB",
		Preview: "some preview",
		Status:  appwire.ThreadStatus{Type: "active"},
		CWD:     "/projects/test",
	}
	detail := hubDetailFromAppThread(thread)
	if detail.Title != "some preview" {
		t.Fatalf("expected preview as title, got %q", detail.Title)
	}
}

// TestCovHubDetailFromAppThreadNoProject covers the no-project path
// (web_api_tree.go:1048-1049).
func TestCovHubDetailFromAppThreadNoProject(t *testing.T) {
	thread := appwire.Thread{
		ID:     "02wMz5Txv1C3Hut0M8GCeB",
		Name:   "named",
		Status: appwire.ThreadStatus{Type: "active"},
	}
	detail := hubDetailFromAppThread(thread)
	if detail.Project != "(no project)" {
		t.Fatalf("expected (no project), got %q", detail.Project)
	}
}

// TestCovHubDetailFromAppThreadEmptyState covers the empty-state fallback
// (web_api_tree.go:1037-1038).
func TestCovHubDetailFromAppThreadEmptyState(t *testing.T) {
	thread := appwire.Thread{
		ID:   "02wMz5Txv1C3Hut0M8GCeB",
		Name: "named",
		CWD:  "/projects/test",
	}
	detail := hubDetailFromAppThread(thread)
	if detail.State != "idle" {
		t.Fatalf("expected idle state, got %q", detail.State)
	}
}

// --- webnext.go: webassetsHandler ---

// TestCovWebassetsHandlerTraversal covers the path-traversal guard
// (webnext.go:68-69).
func TestCovWebassetsHandlerTraversal(t *testing.T) {
	handler := webassetsHandler(distFS())
	req := httptest.NewRequest(http.MethodGet, "/webassets/../secret", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for path traversal, got %d", rec.Code)
	}
}

// --- web_api_project_delete.go: appendProjectDeleteLiveSkip ---

// TestCovAppendProjectDeleteLiveSkip covers the helper.
func TestCovAppendProjectDeleteLiveSkip(t *testing.T) {
	result := appendProjectDeleteLiveSkip(nil, "s1")
	want := []projectDeleteSkip{{ID: "s1", Reason: "resumed live"}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
}
