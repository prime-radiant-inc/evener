package hub

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

func TestAppWireArchiveNavigationTicketConvergesWithPublisherBroadcast(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	source.mu.Lock()
	source.inputs.Tree.Projects[0].Key = project.ID
	source.mu.Unlock()
	web := NewWebServer(hubcore.WebConfig{
		Archive: hubcore.NewArchiveStore(filepath.Join(root, "archive.db")),
	})
	web.navigation = newTestNavigationService(t, source)

	// Prime the retained snapshot, then make the real archive handler observe
	// a changed navigation source. No scheduler or wall-clock synchronization
	// is involved: Refresh owns the ticket and publication atomically.
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	recorder := &navigationPublisherRecorder{seen: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		runNavigationPublisher(ctx, web.navigation, recorder)
		close(done)
	}()
	source.changeTitle("changed")
	response, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:       appwire.ArchiveTargetProject,
		ID:         project.ID,
		WorkingDir: project.CanonicalPath,
		Archived:   true,
	})
	if err != nil {
		t.Fatalf("archive project: %v", err)
	}
	waitNavigationSignal(t, recorder.seen, "archive navigation broadcast")
	methods, payloads := recorder.snapshot()
	if len(methods) != 1 || methods[0] != appwire.NotifyEvenerNavigationInvalidated || len(payloads) != 1 {
		t.Fatalf("broadcasts=%v payloads=%+v, want one navigation frame", methods, payloads)
	}
	publication := payloads[0]
	responseTargets := append([]appwire.NavigationInvalidationTarget(nil), response.Navigation.Targets...)
	if publication.GenerationID != response.Navigation.GenerationID || !reflect.DeepEqual(responseTargets, publication.Targets) {
		t.Fatalf("response navigation=%+v broadcast=%+v", response.Navigation, publication)
	}
	if len(publication.Targets) != 1 || publication.Targets[0].Kind != appwire.NavigationTargetProject || publication.Targets[0].ProjectKey != project.ID {
		t.Fatalf("publication targets=%+v", publication.Targets)
	}
	if publication.Targets[0].Revision == 0 {
		t.Fatalf("scoped archive target has zero revision: %+v", publication.Targets[0])
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("publisher replayed events: %+v", replay)
	}
	cancel()
	waitNavigationSignal(t, done, "navigation publisher shutdown")
}

func TestAppWireArchiveNavigationNoOpAndUnknownProjectSemantics(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	dir := t.TempDir()
	web := NewWebServer(hubcore.WebConfig{
		Favorite: hubcore.NewFavoriteStore(dir + "/favorites.db"),
		Archive:  hubcore.NewArchiveStore(dir + "/archive.db"),
	})
	web.navigation = newTestNavigationService(t, source)
	web.appRPC = newHubAppServerWithNavigation(web.cfg, web.sources, web.navigation)
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	initialGeneration := web.navigation.Capability().GenerationID

	// The first request changes the favorite store but not navigation. This is
	// the R39 no-op contract: an independent refresh flight returns empty and
	// emits no typed invalidation.
	first, err := dispatchFavoriteSet(t, web.appRPC, appwire.FavoriteSetParams{Kind: "project", ID: "p1", Favorited: true})
	if err != nil {
		t.Fatalf("first favorite: %v", err)
	}
	if first.Navigation.GenerationID != initialGeneration || len(first.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
		t.Fatalf("no-op response/events: response=%+v events=%+v", first.Navigation, web.navigation.DrainPublications())
	}
	repeated, err := dispatchFavoriteSet(t, web.appRPC, appwire.FavoriteSetParams{Kind: "project", ID: "p1", Favorited: true})
	if err != nil {
		t.Fatalf("repeat favorite: %v", err)
	}
	if repeated.Navigation.GenerationID != initialGeneration || len(repeated.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
		t.Fatalf("repeat favorite response/events: response=%+v events=%+v", repeated.Navigation, web.navigation.DrainPublications())
	}

	// Session archive deliberately uses the handler's unscoped hint. The
	// source still has a real p1 semantic change, so commitTargets may retain
	// both the precise project and the wildcard target; requiring wildcard
	// alone would incorrectly weaken the target assertion.
	source.changeTitle("unknown-session-check")
	response, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "unknown-session",
		Archived: true,
	})
	if err != nil {
		t.Fatalf("archive session: %v", err)
	}
	if !response.OK {
		t.Fatalf("unknown response = %+v, want success", response)
	}
	published := web.navigation.DrainPublications()
	responseTargets := append([]appwire.NavigationInvalidationTarget(nil), response.Navigation.Targets...)
	if len(published) != 1 || response.Navigation.GenerationID != published[0].GenerationID || !reflect.DeepEqual(responseTargets, published[0].Targets) {
		t.Fatalf("unknown convergence response=%+v publication=%+v", response.Navigation, published)
	}
	if len(published[0].Targets) != 2 {
		t.Fatalf("unknown session targets=%+v, want exactly project then wildcard", published[0].Targets)
	}
	projectTarget, wildcardTarget := published[0].Targets[0], published[0].Targets[1]
	if projectTarget.Kind != appwire.NavigationTargetProject || projectTarget.ProjectKey != "p1" || projectTarget.Revision == 0 {
		t.Fatalf("first unknown-session target=%+v, want p1 with nonzero revision", projectTarget)
	}
	if wildcardTarget.Kind != appwire.NavigationTargetAllLoadedProjects || wildcardTarget.Revision != 0 {
		t.Fatalf("second unknown-session target=%+v, want all_loaded_projects revision 0", wildcardTarget)
	}

	// Repeating the same AppWire request without another source change is
	// an independent no-op flight: the successful response remains R39-empty
	// and no second typed event is published.
	repeat, err := dispatchArchiveSet(t, web, appwire.ArchiveParams{
		Kind:     appwire.ArchiveTargetSession,
		ID:       "unknown-session",
		Archived: true,
	})
	if err != nil {
		t.Fatalf("repeat archive session: %v", err)
	}
	if repeat.Navigation.GenerationID != response.Navigation.GenerationID || len(repeat.Navigation.Targets) != 0 {
		t.Fatalf("repeat response targets=%+v, want empty", repeat.Navigation.Targets)
	}
	if events := web.navigation.DrainPublications(); len(events) != 0 {
		t.Fatalf("repeat published events=%+v, want empty", events)
	}
}
