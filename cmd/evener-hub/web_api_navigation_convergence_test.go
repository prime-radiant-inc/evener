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
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("publisher replayed events: %+v", replay)
	}
}

func TestRESTNavigationNoOpAndUnknownProjectSemantics(t *testing.T) {
	source := newTestNavigationSource(time.Unix(1_700_000_000, 0).UTC())
	web := NewWebServer(hubcore.WebConfig{
		Favorite: hubcore.NewFavoriteStore(t.TempDir() + "/favorites.db"),
	})
	web.navigation = newTestNavigationService(t, source)
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}

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
	if len(first.Navigation.Targets) != 0 || len(web.navigation.DrainPublications()) != 0 {
		t.Fatalf("no-op response/events: response=%+v events=%+v", first.Navigation, web.navigation.DrainPublications())
	}

	source.changeTitle("unknown-project-check")
	rr = postJSON(t, web.Handler(), "/api/favorite", `{"kind":"project","id":"unknown","favorited":false}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("unknown status/body=%d %s", rr.Code, rr.Body.String())
	}
	var response favoriteMutationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	published := web.navigation.DrainPublications()
	if len(published) != 1 || response.Navigation.GenerationID != published[0].GenerationID || !reflect.DeepEqual(response.Navigation.Targets, published[0].Targets) {
		t.Fatalf("unknown convergence response=%+v publication=%+v", response.Navigation, published)
	}
	if len(published[0].Targets) != 1 || published[0].Targets[0].Kind != appwire.NavigationTargetAllLoadedProjects {
		t.Fatalf("unknown project targets=%+v", published[0].Targets)
	}
}
