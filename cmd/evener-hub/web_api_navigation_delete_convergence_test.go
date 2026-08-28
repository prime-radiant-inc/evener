package hub

import (
	"encoding/json"
	"errors"
	"net/http"
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

func assertDeleteNavigation(t *testing.T, web *WebServer, response appwire.ProjectDeleteResponse, kind appwire.NavigationTargetKind, projectKey string) {
	t.Helper()
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

func TestDeleteNavigationConvergence(t *testing.T) {
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
		full, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
			Key:        project.ID,
			WorkingDir: project.CanonicalPath,
		})
		if err != nil {
			t.Fatalf("full delete: %v", err)
		}
		if len(full.Deleted) != 2 || len(full.Skipped) != 0 {
			t.Fatalf("full response=%+v", full)
		}
		assertDeleteNavigation(t, web, full, appwire.NavigationTargetProject, project.ID)

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
		noop, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
			Key:        project.ID,
			WorkingDir: project.CanonicalPath,
		})
		if err != nil {
			t.Fatalf("no-op delete: %v", err)
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
		response, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{
			Key:        project.ID,
			WorkingDir: project.CanonicalPath,
		})
		if err != nil {
			t.Fatalf("partial delete: %v", err)
		}
		if len(response.Deleted) != 1 || len(response.Skipped) != 1 {
			t.Fatalf("partial response=%+v", response)
		}
		assertDeleteNavigation(t, web, response, appwire.NavigationTargetProject, project.ID)
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
		params := appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: project.CanonicalPath}
		source.changeTitle("resume-first")
		firstResponse, err := dispatchProjectDelete(t, web, params)
		if err != nil {
			t.Fatalf("first resumed delete: %v", err)
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
		resumed, err := dispatchProjectDelete(t, web, params)
		if err != nil {
			t.Fatalf("resumed delete: %v", err)
		}
		if len(resumed.Deleted) != 1 {
			t.Fatalf("resumed response/events=%+v", resumed)
		}
		assertDeleteNavigation(t, web, resumed, appwire.NavigationTargetProject, project.ID)
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
