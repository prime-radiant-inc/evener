package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
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

func dispatchThreadNameSet(t *testing.T, server *appserver.Server, params appwire.ThreadNameSetParams) error {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal thread name params: %v", err)
	}
	_, err = server.Router().Dispatch(t.Context(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerThreadNameSet,
		Params: raw,
	})
	return err
}

func TestAppWireNavigationRenameConvergesLiveAndEnded(t *testing.T) {
	t.Run("live changed and repeat no-op", func(t *testing.T) {
		source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
		live := &renameNavigationSource{scriptedAppSource: &scriptedAppSource{
			id: "local",
			thread: appwire.Thread{ID: "live-rename", Evener: appwire.EvenerThread{
				Ref:          "local:live-rename",
				Capabilities: appwire.ThreadCapabilities{Rename: true},
			}},
		}, onRename: source.changeTitle}
		registry := appsource.NewRegistry()
		registry.Add(live)
		navigation := newTestNavigationService(t, source)
		if _, err := navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, registry, navigation)

		if err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:live-rename", Name: "  renamed-live  "}); err != nil {
			t.Fatalf("thread name set: %v", err)
		}
		generation := assertRenameNavigation(t, navigation, 1, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetProject, ProjectKey: "p1"}, {Kind: appwire.NavigationTargetAllLoadedProjects}}, "")
		if live.got.Ref != "local:live-rename" || live.got.Name != "renamed-live" {
			t.Fatalf("SetThreadName params=%+v", live.got)
		}

		if err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:live-rename", Name: "renamed-live"}); err != nil {
			t.Fatalf("repeat thread name set: %v", err)
		}
		assertRenameNavigation(t, navigation, 0, nil, generation)
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
		navigation := newTestNavigationService(t, source)
		if _, err := navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		registry := appsource.NewRegistry()
		server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: past, Roster: hubcore.NewRosterWithEntries()}, registry, navigation)
		oldSave := saveSessionMetaForRename
		saveSessionMetaForRename = func(dir string, meta schema.SessionMeta) error {
			source.changeTitle(meta.Name)
			return oldSave(dir, meta)
		}
		defer func() { saveSessionMetaForRename = oldSave }()

		if err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:" + original.ID, Name: "  renamed-ended  "}); err != nil {
			t.Fatalf("thread name set: %v", err)
		}
		generation := assertRenameNavigation(t, navigation, 1, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetProject, ProjectKey: projectKey}}, "")
		got, err := schema.LoadSessionMeta(stateDir, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "renamed-ended" || got.NameSource != "user" || got.ID != original.ID || !reflect.DeepEqual(got.EnvInfo, original.EnvInfo) {
			t.Fatalf("renamed meta=%+v, preserved original=%+v", got, original)
		}

		if err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:" + original.ID, Name: "renamed-ended"}); err != nil {
			t.Fatalf("repeat thread name set: %v", err)
		}
		assertRenameNavigation(t, navigation, 0, nil, generation)
	})
}

func TestAppWireRenameValidatesRefAndName(t *testing.T) {
	server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry(), nil)
	for _, tc := range []struct {
		name   string
		params appwire.ThreadNameSetParams
	}{
		{name: "invalid ref", params: appwire.ThreadNameSetParams{Ref: "missing-ref", Name: "name"}},
		{name: "empty normalized name", params: appwire.ThreadNameSetParams{Ref: "local:session", Name: "  \t"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dispatchThreadNameSet(t, server, tc.params)
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("error=%v, want invalid params", err)
			}
		})
	}
}

func TestAppWireRenameKeepsNonLocalRefSeparateFromMatchingLocalSession(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0123456789")
	localMeta := schema.SessionMeta{ID: sessionID, Name: "local name", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := schema.SaveSessionMeta(stateDir, localMeta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	remote := &renameNavigationSource{scriptedAppSource: &scriptedAppSource{
		id: "remote",
		thread: appwire.Thread{ID: sessionID, Evener: appwire.EvenerThread{
			Ref:          "remote:" + sessionID,
			Capabilities: appwire.ThreadCapabilities{Rename: true},
		}},
	}}
	registry := appsource.NewRegistry()
	registry.Add(remote)
	server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: past}, registry, nil)

	originalLoad := loadSessionMetaForRename
	loadSessionMetaForRename = func(string, string) (schema.SessionMeta, error) {
		return schema.SessionMeta{}, errors.New("local metadata unavailable")
	}
	t.Cleanup(func() { loadSessionMetaForRename = originalLoad })

	if err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "remote:" + sessionID, Name: "remote name"}); err != nil {
		t.Fatalf("thread name set: %v", err)
	}
	if remote.got.Ref != "remote:"+sessionID || remote.got.Name != "remote name" {
		t.Fatalf("SetThreadName params=%+v", remote.got)
	}
	entry, ok := past.Find(sessionID)
	if !ok {
		t.Fatalf("local past session %q not found", sessionID)
	}
	if entry.Meta.Name != localMeta.Name {
		t.Fatalf("local past name=%q, want preserved %q", entry.Meta.Name, localMeta.Name)
	}
}

func TestAppWireRenameReportsEndedMetadataFailures(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-failure-0123456789")
	meta := schema.SessionMeta{ID: sessionID, Name: "old", UpdatedAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: past}, appsource.NewRegistry(), nil)

	originalLoad := loadSessionMetaForRename
	originalSave := saveSessionMetaForRename
	t.Cleanup(func() {
		loadSessionMetaForRename = originalLoad
		saveSessionMetaForRename = originalSave
	})

	loadSessionMetaForRename = func(string, string) (schema.SessionMeta, error) {
		return schema.SessionMeta{}, errors.New("read failed")
	}
	err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:" + sessionID, Name: "new"})
	assertInternalRenameError(t, err, "load meta: read failed")

	loadSessionMetaForRename = originalLoad
	saveSessionMetaForRename = func(string, schema.SessionMeta) error {
		return errors.New("write failed")
	}
	err = dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:" + sessionID, Name: "new"})
	assertInternalRenameError(t, err, "save meta: write failed")
}

func assertInternalRenameError(t *testing.T, err error, message string) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError || wire.Message != message {
		t.Fatalf("error=%v, want internal error %q", err, message)
	}
}

func TestAppWireRenameDoesNotEditMetaWhenEndedSessionBecomesLive(t *testing.T) {
	const sessionID = "02wMz5Txv2enqVTitaig6F"
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-rename-race-0123456789")
	original := schema.SessionMeta{
		ID:        sessionID,
		Name:      "old",
		UpdatedAt: time.Unix(1_700_000_000, 0).UTC(),
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: stateDir},
	}
	if err := schema.SaveSessionMeta(stateDir, original); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle})
	registry := appsource.NewRegistry()
	registry.Add(&scriptedAppSource{
		id: "local",
		thread: appwire.Thread{ID: sessionID, Evener: appwire.EvenerThread{
			Ref:          "local:" + sessionID,
			Capabilities: appwire.ThreadCapabilities{Rename: true},
		}},
	})
	server := newHubAppServerWithNavigation(hubcore.WebConfig{Past: past, Roster: roster}, registry, nil)

	originalLoad := loadSessionMetaForRename
	loadSessionMetaForRename = func(dir, id string) (schema.SessionMeta, error) {
		meta, err := originalLoad(dir, id)
		writeRendezvous(t, runDir, rendezvous.Entry{
			PID:        91,
			Address:    "127.0.0.1:4591",
			SessionID:  sessionID,
			WorkingDir: stateDir,
		})
		roster.Refresh()
		return meta, err
	}
	t.Cleanup(func() { loadSessionMetaForRename = originalLoad })

	err := dispatchThreadNameSet(t, server, appwire.ThreadNameSetParams{Ref: "local:" + sessionID, Name: "new"})
	if err == nil {
		t.Fatal("thread name set succeeded after the session became live")
	}
	meta, loadErr := schema.LoadSessionMeta(stateDir, sessionID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if meta.Name != original.Name {
		t.Fatalf("meta name=%q, want preserved %q", meta.Name, original.Name)
	}
}

func assertRenameNavigation(t *testing.T, navigation *NavigationService, wantEvents int, wantTargets []appwire.NavigationInvalidationTarget, wantGeneration string) string {
	t.Helper()
	events := navigation.DrainPublications()
	if len(events) != wantEvents {
		t.Fatalf("typed events=%d, want %d: %+v", len(events), wantEvents, events)
	}
	if wantEvents == 0 {
		capability := navigation.Capability()
		if capability == nil || capability.GenerationID != wantGeneration {
			t.Fatalf("no-op capability=%+v, want generation %q", capability, wantGeneration)
		}
		return capability.GenerationID
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
	if replay := navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("second typed event=%+v", replay)
	}
	return events[0].GenerationID
}
