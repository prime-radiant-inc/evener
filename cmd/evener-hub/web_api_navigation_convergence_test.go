package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// navigationRESTCapture is deliberately built at the real handler boundary:
// Refresh returns the ticket handed to REST, while DrainPublications is the
// publisher FIFO consumed by the broadcaster in main. Keeping both values in
// this helper prevents tests from comparing two independently-built targets.
type navigationRESTCapture struct {
	generation string
	targets    []appwire.NavigationInvalidationTarget
	published  []appwire.NavigationInvalidatedPayload
}

func captureNavigationREST(t *testing.T, web *WebServer, rr *httptest.ResponseRecorder) navigationRESTCapture {
	t.Helper()
	var response favoriteMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	published := web.navigation.DrainPublications()
	return navigationRESTCapture{
		generation: response.Navigation.GenerationID,
		targets:    append([]appwire.NavigationInvalidationTarget(nil), response.Navigation.Targets...),
		published:  published,
	}
}

func TestRESTNavigationTicketConvergesWithPublisherFIFO(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web := NewWebServer(hubcore.WebConfig{
		Favorite: hubcore.NewFavoriteStore(t.TempDir() + "/favorites.db"),
	})
	web.navigation = newTestNavigationService(t, source)

	// Prime the retained snapshot, then make the real favorite handler observe
	// a changed navigation source. No scheduler or wall-clock synchronization
	// is involved: Refresh owns the ticket and publication atomically.
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	source.changeTitle("changed")
	rr := postFavorite(t, web, "project", "p1", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response favoriteMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatal("successful favorite response did not preserve ok=true")
	}
	capture := captureNavigationREST(t, web, rr)
	if len(capture.published) != 1 {
		t.Fatalf("published events=%d, want exactly one: %+v", len(capture.published), capture.published)
	}
	publication := capture.published[0]
	if publication.GenerationID != capture.generation {
		t.Fatalf("generation response=%q publication=%q", capture.generation, publication.GenerationID)
	}
	if !reflect.DeepEqual(capture.targets, publication.Targets) {
		t.Fatalf("response targets=%+v publication targets=%+v", capture.targets, publication.Targets)
	}
	if len(publication.Targets) != 1 || publication.Targets[0].Kind != appwire.NavigationTargetProject || publication.Targets[0].ProjectKey != "p1" {
		t.Fatalf("publication targets=%+v", publication.Targets)
	}
	if publication.Targets[0].Revision == 0 {
		t.Fatalf("scoped favorite target has zero revision: %+v", publication.Targets[0])
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("publisher replayed events: %+v", replay)
	}
}

func TestRESTNavigationNoOpAndUnknownProjectSemantics(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	dir := t.TempDir()
	web := NewWebServer(hubcore.WebConfig{
		Favorite: hubcore.NewFavoriteStore(dir + "/favorites.db"),
		Archive:  hubcore.NewArchiveStore(dir + "/archive.db"),
	})
	web.navigation = newTestNavigationService(t, source)
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	initialGeneration := web.navigation.Capability().GenerationID

	// The first request changes the favorite store but not navigation. This is
	// the R39 no-op contract: an independent refresh flight returns empty and
	// emits no typed invalidation.
	rr := postJSON(t, web.Handler(), "/api/favorite", `{"kind":"project","id":"p1","favorited":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rr.Code, rr.Body.String())
	}
	var first favoriteMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Navigation.GenerationID != initialGeneration || len(first.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
		t.Fatalf("no-op response/events: response=%+v events=%+v", first.Navigation, web.navigation.DrainPublications())
	}

	// Session archive deliberately uses the handler's unscoped hint. The
	// source still has a real p1 semantic change, so commitTargets may retain
	// both the precise project and the wildcard target; requiring wildcard
	// alone would incorrectly weaken the target assertion.
	source.changeTitle("unknown-session-check")
	rr = postJSON(t, web.Handler(), "/api/archive", `{"kind":"session","id":"unknown-session","archived":true}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("unknown status/body=%d %s", rr.Code, rr.Body.String())
	}
	var response archiveMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
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

	// Repeating the same real handler request without another source change is
	// an independent no-op flight: the successful response remains R39-empty
	// and no second typed event is published.
	rr = postJSON(t, web.Handler(), "/api/archive", `{"kind":"session","id":"unknown-session","archived":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("repeat status=%d body=%s", rr.Code, rr.Body.String())
	}
	var repeat archiveMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &repeat); err != nil {
		t.Fatal(err)
	}
	if repeat.Navigation.GenerationID != response.Navigation.GenerationID || len(repeat.Navigation.Targets) != 0 {
		t.Fatalf("repeat response targets=%+v, want empty", repeat.Navigation.Targets)
	}
	if events := web.navigation.DrainPublications(); len(events) != 0 {
		t.Fatalf("repeat published events=%+v, want empty", events)
	}
}
