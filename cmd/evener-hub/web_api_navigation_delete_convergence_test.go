package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

func assertDeleteNavigation(t *testing.T, web *WebServer, body []byte, kind appwire.NavigationTargetKind, projectKey string) {
	t.Helper()
	var response projectDeleteResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	events := web.navigation.DrainPublications()
	if len(events) != 1 {
		t.Fatalf("typed navigation events=%d, want one: %+v", len(events), events)
	}
	responseTargets := response.Navigation.Targets
	if response.Navigation.GenerationID != events[0].GenerationID || !reflect.DeepEqual(responseTargets, events[0].Targets) {
		t.Fatalf("response navigation=%+v, publication=%+v", response.Navigation, events[0])
	}
	if len(events[0].Targets) != 1 || events[0].Targets[0].Kind != kind || events[0].Targets[0].ProjectKey != projectKey {
		t.Fatalf("targets=%+v, want one %s target for %q", events[0].Targets, kind, projectKey)
	}
	if events[0].Targets[0].Revision == 0 {
		t.Fatalf("scoped target has zero revision: %+v", events[0].Targets[0])
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("second typed event=%+v", replay)
	}
}

func TestRESTDeleteNavigationConvergence(t *testing.T) {
	t.Run("project full partial resumed and no-op", func(t *testing.T) {
		root := t.TempDir()
		projectDir := filepath.Join(root, "project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		for _, id := range projectDeleteCanonicalSessionIDs[:2] {
			writeSession(t, stateDir, id, project.CanonicalPath)
		}
		past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		archive := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
		favorite := hubcore.NewFavoriteStore(filepath.Join(root, "index.db"))
		web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Archive: archive, Favorite: favorite, Roster: hubcore.NewRosterWithEntries()})
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		source.mu.Lock()
		source.inputs.Tree.Projects[0].Key = project.ID
		source.mu.Unlock()
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		source.changeTitle("project-full")
		req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(`{"key":"`+project.ID+`","working_dir":"`+project.CanonicalPath+`"}`))
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("full status=%d body=%s", rec.Code, rec.Body.String())
		}
		var full projectDeleteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
			t.Fatal(err)
		}
		if len(full.Deleted) != 2 || len(full.Skipped) != 0 {
			t.Fatalf("full response=%+v", full)
		}
		assertDeleteNavigation(t, web, rec.Body.Bytes(), appwire.NavigationTargetProject, project.ID)

	})

	t.Run("project empty no-op", func(t *testing.T) {
		root := t.TempDir()
		projectDir := filepath.Join(root, "empty-project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		oldRemove := removeProjectSessionFile
		removeProjectSessionFile = func(path string) error {
			if strings.Contains(path, webTestSessionID) {
				return errors.New("injected no-op cleanup failure")
			}
			return oldRemove(path)
		}
		t.Cleanup(func() { removeProjectSessionFile = oldRemove })
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		source.mu.Lock()
		source.inputs.Tree.Projects[0].Key = project.ID
		source.mu.Unlock()
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(`{"key":"`+project.ID+`","working_dir":"`+project.CanonicalPath+`"}`))
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("no-op status=%d body=%s", rec.Code, rec.Body.String())
		}
		var noop projectDeleteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &noop); err != nil {
			t.Fatal(err)
		}
		if len(noop.Deleted) != 0 || len(noop.Skipped) != 1 || noop.Skipped[0].ID != webTestSessionID || len(noop.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
			t.Fatalf("no-op response/events=%+v", noop)
		}
		capability := web.navigation.Capability()
		if capability == nil || noop.Navigation.GenerationID != capability.GenerationID {
			t.Fatalf("no-op generation=%q capability=%+v", noop.Navigation.GenerationID, capability)
		}
	})

	t.Run("project partial", func(t *testing.T) {
		root := t.TempDir()
		projectDir := filepath.Join(root, "project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		for _, id := range projectDeleteCanonicalSessionIDs[:2] {
			writeSession(t, stateDir, id, project.CanonicalPath)
		}
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		oldRemove := removeProjectSessionFile
		removeProjectSessionFile = func(path string) error {
			if strings.Contains(path, projectDeleteCanonicalSessionIDs[1]) {
				return errors.New("injected partial cleanup failure")
			}
			return oldRemove(path)
		}
		t.Cleanup(func() { removeProjectSessionFile = oldRemove })
		web := NewWebServer(hubcore.WebConfig{StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		source.mu.Lock()
		source.inputs.Tree.Projects[0].Key = project.ID
		source.mu.Unlock()
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		source.changeTitle("partial")
		req := httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(`{"key":"`+project.ID+`","working_dir":"`+project.CanonicalPath+`"}`))
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("partial status=%d body=%s", rec.Code, rec.Body.String())
		}
		var response projectDeleteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Deleted) != 1 || len(response.Skipped) != 1 {
			t.Fatalf("partial response=%+v", response)
		}
		assertDeleteNavigation(t, web, rec.Body.Bytes(), appwire.NavigationTargetProject, project.ID)
	})

	t.Run("project resumed request", func(t *testing.T) {
		root := t.TempDir()
		projectDir := filepath.Join(root, "work")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		oldRemoveDir := removeProjectSessionDir
		removeProjectSessionDir = func(string) error { return errors.New("injected resume fence") }
		t.Cleanup(func() { removeProjectSessionDir = oldRemoveDir })
		web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, StateDir: root, Past: past, Roster: hubcore.NewRosterWithEntries()})
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		source.mu.Lock()
		source.inputs.Tree.Projects[0].Key = project.ID
		source.mu.Unlock()
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		body := `{"key":"` + project.ID + `","working_dir":"` + project.CanonicalPath + `"}`
		source.changeTitle("resume-first")
		first := httptest.NewRecorder()
		web.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body)))
		if first.Code != http.StatusOK {
			t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
		}
		var firstResponse projectDeleteResponse
		if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
			t.Fatal(err)
		}
		if len(firstResponse.Deleted) != 0 || len(firstResponse.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
			t.Fatalf("first resumed response/events=%+v", firstResponse)
		}
		capability := web.navigation.Capability()
		if capability == nil || firstResponse.Navigation.GenerationID != capability.GenerationID {
			t.Fatalf("first resumed generation=%q capability=%+v", firstResponse.Navigation.GenerationID, capability)
		}
		removeProjectSessionDir = oldRemoveDir
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		// The resumed request completes durable directory cleanup and reports the
		// session as newly deleted, so it publishes the converged project target.
		source.changeTitle("resume-second")
		second := httptest.NewRecorder()
		web.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/project/delete", newBody(body)))
		if second.Code != http.StatusOK {
			t.Fatalf("resumed status=%d body=%s", second.Code, second.Body.String())
		}
		var resumed projectDeleteResponse
		if err := json.Unmarshal(second.Body.Bytes(), &resumed); err != nil {
			t.Fatal(err)
		}
		if len(resumed.Deleted) != 1 {
			t.Fatalf("resumed response/events=%+v", resumed)
		}
		assertDeleteNavigation(t, web, second.Body.Bytes(), appwire.NavigationTargetProject, project.ID)
	})

	t.Run("session changed repeated and no-op", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "projects", "session-delete-0123456789")
		projectDir := filepath.Join(root, "project")
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			t.Fatal(err)
		}
		project, err := identifier.ResolveProject(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		writeSession(t, stateDir, webTestSessionID, project.CanonicalPath)
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		source.changeTitle("session-baseline")
		source.mu.Lock()
		source.inputs.Tree.Projects[0].Key = filepath.Base(stateDir)
		source.mu.Unlock()
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		source.changeTitle("session-changed")
		rec, resp := postSessionDelete(t, web, webTestSessionID)
		if rec.Code != http.StatusOK || len(resp.Deleted) != 1 {
			t.Fatalf("changed status/body=%d %+v", rec.Code, resp)
		}
		var decoded sessionNavigationDeleteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		events := web.navigation.DrainPublications()
		decodedTargets := decoded.Navigation.Targets
		if len(events) != 1 || decoded.Navigation.GenerationID != events[0].GenerationID || !reflect.DeepEqual(decodedTargets, events[0].Targets) {
			t.Fatalf("session convergence=%+v events=%+v", decoded.Navigation, events)
		}
		if len(events[0].Targets) != 1 || events[0].Targets[0].Kind != appwire.NavigationTargetProject || events[0].Targets[0].ProjectKey != filepath.Base(stateDir) {
			t.Fatalf("session targets=%+v", events[0].Targets)
		}
		if events[0].Targets[0].Revision == 0 {
			t.Fatalf("session scoped target has zero revision: %+v", events[0].Targets[0])
		}
		changedGeneration := decoded.Navigation.GenerationID
		rec, _ = postSessionDelete(t, web, webTestSessionID)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Code)
		}
		var repeat sessionNavigationDeleteResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &repeat); err != nil {
			t.Fatal(err)
		}
		if len(repeat.Deleted) != 0 || len(repeat.Navigation.Targets) != 0 || repeat.Navigation.GenerationID != changedGeneration || len(web.navigation.DrainPublications()) != 0 {
			t.Fatalf("session repeat=%+v", repeat)
		}
	})
}
