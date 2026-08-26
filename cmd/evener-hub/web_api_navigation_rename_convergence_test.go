package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type renameNavigationSource struct {
	*scriptedAppSource
	onRename func(string)
	got      appwire.ThreadNameSetParams
}

func (s *renameNavigationSource) SetThreadName(_ context.Context, params appwire.ThreadNameSetParams) error {
	s.got = params
	s.thread.Name = params.Name
	if s.onRename != nil {
		s.onRename(params.Name)
	}
	return nil
}

func TestRESTNavigationRenameConvergesLiveAndEnded(t *testing.T) {
	t.Run("live changed and repeat no-op", func(t *testing.T) {
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
		live := &renameNavigationSource{scriptedAppSource: &scriptedAppSource{id: "local"}, onRename: source.changeTitle}
		registry := appsource.NewRegistry()
		registry.Add(live)
		web.sources = registry
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		oldLive := isLiveForRename
		isLiveForRename = func(*WebServer, string) bool { return true }
		defer func() { isLiveForRename = oldLive }()

		rr := postJSON(t, web.Handler(), "/api/sessions/local:live-rename/rename", `{"name":"renamed-live"}`)
		assertRenameNavigation(t, rr, web, 1, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetProject, ProjectKey: "p1"}, {Kind: appwire.NavigationTargetAllLoadedProjects}})
		if live.got.Ref != "local:live-rename" || live.got.Name != "renamed-live" {
			t.Fatalf("SetThreadName params=%+v", live.got)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rr.Code)
		}

		rr = postJSON(t, web.Handler(), "/api/sessions/local:live-rename/rename", `{"name":"renamed-live"}`)
		assertRenameNavigation(t, rr, web, 0, nil)
	})

	t.Run("ended changed and repeat no-op", func(t *testing.T) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "projects", "project-0123456789")
		original := schema.SessionMeta{ID: "02wMz5Txv1C3Hut0M8GCeB", Name: "old", NameSource: "generated", UpdatedAt: time.Unix(1_700_000_000, 0).UTC(), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/preserve"}}
		if err := schema.SaveSessionMeta(stateDir, original); err != nil {
			t.Fatal(err)
		}
		past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		projectKey := filepath.Base(stateDir)
		source.inputs.Tree.Projects[0].Key = projectKey
		source.inputs.Tree.Projects[0].Name = projectKey
		source.inputs.Tree.Projects[0].Current[0].Project = projectKey
		web := NewWebServer(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()})
		web.navigation = newTestNavigationService(t, source)
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		oldSave := saveSessionMetaForRename
		saveSessionMetaForRename = func(dir string, meta schema.SessionMeta) error {
			source.changeTitle(meta.Name)
			return oldSave(dir, meta)
		}
		defer func() { saveSessionMetaForRename = oldSave }()

		rr := postJSON(t, web.Handler(), "/api/sessions/local:"+original.ID+"/rename", `{"name":"renamed-ended"}`)
		assertRenameNavigation(t, rr, web, 1, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetProject, ProjectKey: projectKey}})
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200", rr.Code)
		}
		got, err := schema.LoadSessionMeta(stateDir, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "renamed-ended" || got.NameSource != "user" || got.ID != original.ID || !reflect.DeepEqual(got.EnvInfo, original.EnvInfo) {
			t.Fatalf("renamed meta=%+v, preserved original=%+v", got, original)
		}

		rr = postJSON(t, web.Handler(), "/api/sessions/local:"+original.ID+"/rename", `{"name":"renamed-ended"}`)
		assertRenameNavigation(t, rr, web, 0, nil)
	})
}

func assertRenameNavigation(t *testing.T, rr *httptest.ResponseRecorder, web *WebServer, wantEvents int, wantTargets []appwire.NavigationInvalidationTarget) {
	t.Helper()
	var response renameMutationResponse
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q, want 200", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("status=%d body=%q: %v", rr.Code, rr.Body.String(), err)
	}
	events := web.navigation.DrainPublications()
	if len(events) != wantEvents {
		t.Fatalf("typed events=%d, want %d: %+v", len(events), wantEvents, events)
	}
	if wantEvents == 0 {
		if response.Navigation.GenerationID == "" || len(response.Navigation.Targets) != 0 {
			t.Fatalf("no-op navigation=%+v", response.Navigation)
		}
		return
	}
	responseTargets := append([]appwire.NavigationInvalidationTarget(nil), response.Navigation.Targets...)
	if response.Navigation.GenerationID != events[0].GenerationID || !reflect.DeepEqual(responseTargets, events[0].Targets) {
		t.Fatalf("response navigation=%+v publication=%+v", response.Navigation, events[0])
	}
	if len(events[0].Targets) != len(wantTargets) {
		t.Fatalf("rename target count=%d, want %d: %+v", len(events[0].Targets), len(wantTargets), events[0].Targets)
	}
	for i, want := range wantTargets {
		got := events[0].Targets[i]
		if got.Kind != want.Kind || got.ProjectKey != want.ProjectKey {
			t.Fatalf("rename target[%d]=%+v, want kind/key=%+v", i, got, want)
		}
		if got.Kind == appwire.NavigationTargetProject && got.Revision == 0 {
			t.Fatalf("rename project target[%d] has no revision: %+v", i, got)
		}
		if got.Kind == appwire.NavigationTargetAllLoadedProjects && got.Revision != 0 {
			t.Fatalf("rename wildcard target[%d] revision=%d, want 0", i, got.Revision)
		}
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("second typed event=%+v", replay)
	}
}
