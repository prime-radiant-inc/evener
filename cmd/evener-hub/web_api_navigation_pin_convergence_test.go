package hub

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func newPinNavigationRESTWeb(t *testing.T, withSection bool) (*WebServer, *hubcore.PinSection) {
	t.Helper()
	past := hubcore.NewPastIndex("")
	store := hubcore.NewPinSectionStore(t.TempDir() + "/pins.db")
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: store})
	web.injectMetasForTest([]schema.SessionMeta{topLevelMeta("session-a")})
	if withSection {
		section, _, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1_700_000_000, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
			t.Fatal(err)
		}
		return web, &section
	}
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	return web, nil
}

func assertPinNavigationPublication(t *testing.T, web *WebServer, generation string, targets []appwire.NavigationInvalidationTarget, wantKinds map[appwire.NavigationTargetKind]string) {
	t.Helper()
	published := web.navigation.DrainPublications()
	if len(published) != 1 {
		t.Fatalf("published events=%d, want exactly one: %+v", len(published), published)
	}
	if published[0].GenerationID != generation {
		t.Fatalf("response generation=%q publication=%q", generation, published[0].GenerationID)
	}
	if !reflect.DeepEqual(targets, published[0].Targets) {
		t.Fatalf("response targets=%+v publication targets=%+v", targets, published[0].Targets)
	}
	found := make(map[appwire.NavigationTargetKind]bool, len(targets))
	for _, target := range targets {
		wantID, ok := wantKinds[target.Kind]
		if !ok {
			switch target.Kind {
			case appwire.NavigationTargetManifest:
			case appwire.NavigationTargetProject:
				if target.ProjectKey != "no-project" {
					t.Fatalf("unexpected project target=%+v", target)
				}
			default:
				t.Fatalf("unexpected target kind/id: %q/%q", target.Kind, target.SectionID)
			}
			continue
		}
		found[target.Kind] = true
		if target.Kind == appwire.NavigationTargetPinSection && target.SectionID != wantID {
			t.Fatalf("pin target section_id=%q, want %q", target.SectionID, wantID)
		}
		if target.Kind == appwire.NavigationTargetPinCatalog && wantID != "" {
			t.Fatalf("pin catalog target id=%q, want empty", wantID)
		}
	}
	for kind := range wantKinds {
		if !found[kind] {
			t.Fatalf("targets=%+v, missing required kind=%q", targets, kind)
		}
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("publisher replayed events: %+v", replay)
	}
}

func assertPinNoNavigation(t *testing.T, web *WebServer, targets []appwire.NavigationInvalidationTarget) {
	t.Helper()
	if len(targets) != 0 {
		t.Fatalf("no-op navigation targets=%+v, want empty", targets)
	}
	if events := web.navigation.DrainPublications(); len(events) != 0 {
		t.Fatalf("no-op published events=%+v, want empty", events)
	}
}

func TestRESTNavigationPinSectionRenameDeleteConverges(t *testing.T) {
	t.Run("rename changed and repeat no-op", func(t *testing.T) {
		web, section := newPinNavigationRESTWeb(t, true)
		rr := patchJSON(t, web.Handler(), "/api/pin-sections/"+section.ID, `{"name":"RESEARCH"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("rename status=%d body=%s", rr.Code, rr.Body.String())
		}
		response := decodeJSON[pinSectionMutationResponse](t, rr)
		if !response.OK || !response.Changed || response.Section.ID != section.ID || response.Section.Name != "RESEARCH" || response.Section.MemberCount != 1 {
			t.Fatalf("rename response=%+v", response)
		}
		assertPinNavigationPublication(t, web, response.Navigation.GenerationID, response.Navigation.Targets, map[appwire.NavigationTargetKind]string{appwire.NavigationTargetPinCatalog: ""})

		repeat := patchJSON(t, web.Handler(), "/api/pin-sections/"+section.ID, `{"name":"RESEARCH"}`)
		if repeat.Code != http.StatusOK {
			t.Fatalf("repeat status=%d body=%s", repeat.Code, repeat.Body.String())
		}
		repeatResponse := decodeJSON[pinSectionMutationResponse](t, repeat)
		if !repeatResponse.OK || repeatResponse.Changed || repeatResponse.Section.ID != section.ID || repeatResponse.Section.Name != "RESEARCH" || repeatResponse.Section.MemberCount != 1 {
			t.Fatalf("repeat response=%+v", repeatResponse)
		}
		assertPinNoNavigation(t, web, repeatResponse.Navigation.Targets)
	})

	t.Run("delete changed and absent", func(t *testing.T) {
		web, section := newPinNavigationRESTWeb(t, true)
		rr := deleteURL(t, web.Handler(), "/api/pin-sections/"+section.ID)
		if rr.Code != http.StatusOK {
			t.Fatalf("delete status=%d body=%s", rr.Code, rr.Body.String())
		}
		response := decodeJSON[pinSectionDeleteResponse](t, rr)
		if !response.OK || !response.Changed || response.MemberCount != 1 {
			t.Fatalf("delete response=%+v", response)
		}
		assertPinNavigationPublication(t, web, response.Navigation.GenerationID, response.Navigation.Targets, map[appwire.NavigationTargetKind]string{appwire.NavigationTargetPinCatalog: "", appwire.NavigationTargetPinSection: section.ID})

		absent := deleteURL(t, web.Handler(), "/api/pin-sections/"+section.ID)
		if absent.Code != http.StatusNotFound {
			t.Fatalf("absent delete status=%d body=%s", absent.Code, absent.Body.String())
		}
		if events := web.navigation.DrainPublications(); len(events) != 0 {
			t.Fatalf("absent delete events=%+v", events)
		}
	})
}

func TestRESTNavigationSessionPinAssignUnpinConverges(t *testing.T) {
	web, _ := newPinNavigationRESTWeb(t, false)
	assigned := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_name":"Research"}`)
	if assigned.Code != http.StatusOK {
		t.Fatalf("assign status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	assignResponse := decodeJSON[sessionPinNavigationResponse](t, assigned)
	if !assignResponse.OK || !assignResponse.Changed || assignResponse.Assignment.SessionRef != "local:session-a" || assignResponse.Assignment.Section.Name != "Research" || assignResponse.Assignment.Section.MemberCount != 1 {
		t.Fatalf("assign response=%+v", assignResponse)
	}
	sectionID := assignResponse.Assignment.Section.ID
	assertPinNavigationPublication(t, web, assignResponse.Navigation.GenerationID, assignResponse.Navigation.Targets, map[appwire.NavigationTargetKind]string{appwire.NavigationTargetPinCatalog: "", appwire.NavigationTargetPinSection: sectionID})

	repeat := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_name":" research "}`)
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat assign status=%d body=%s", repeat.Code, repeat.Body.String())
	}
	repeatResponse := decodeJSON[sessionPinNavigationResponse](t, repeat)
	if !repeatResponse.OK || repeatResponse.Changed || repeatResponse.Assignment.SessionRef != "local:session-a" || repeatResponse.Assignment.Section.ID != sectionID || repeatResponse.Assignment.Section.MemberCount != 1 {
		t.Fatalf("repeat assign response=%+v", repeatResponse)
	}
	assertPinNoNavigation(t, web, repeatResponse.Navigation.Targets)

	unpinned := deleteURL(t, web.Handler(), "/api/session-pin?ref=local%3Asession-a")
	if unpinned.Code != http.StatusOK {
		t.Fatalf("unpin status=%d body=%s", unpinned.Code, unpinned.Body.String())
	}
	unpinResponse := decodeJSON[sessionPinNavigationResponse](t, unpinned)
	if !unpinResponse.OK || !unpinResponse.Changed || unpinResponse.Assignment.SessionRef != "local:session-a" || unpinResponse.Assignment.Section.ID != "" {
		t.Fatalf("unpin response=%+v", unpinResponse)
	}
	assertPinNavigationPublication(t, web, unpinResponse.Navigation.GenerationID, unpinResponse.Navigation.Targets, map[appwire.NavigationTargetKind]string{appwire.NavigationTargetPinCatalog: "", appwire.NavigationTargetPinSection: sectionID})

	repeatUnpin := deleteURL(t, web.Handler(), "/api/session-pin?ref=local%3Asession-a")
	if repeatUnpin.Code != http.StatusOK {
		t.Fatalf("repeat unpin status=%d body=%s", repeatUnpin.Code, repeatUnpin.Body.String())
	}
	repeatUnpinResponse := decodeJSON[sessionPinNavigationResponse](t, repeatUnpin)
	if !repeatUnpinResponse.OK || repeatUnpinResponse.Changed || repeatUnpinResponse.Assignment.SessionRef != "local:session-a" {
		t.Fatalf("repeat unpin response=%+v", repeatUnpinResponse)
	}
	assertPinNoNavigation(t, web, repeatUnpinResponse.Navigation.Targets)
}

func TestRESTNavigationSessionPinAbsentAssignHasNoEvent(t *testing.T) {
	web, _ := newPinNavigationRESTWeb(t, false)
	absent := postJSON(t, web.Handler(), "/api/session-pin", `{"session_ref":"local:session-a","section_id":"missing"}`)
	if absent.Code != http.StatusNotFound {
		t.Fatalf("absent assign status=%d body=%s", absent.Code, absent.Body.String())
	}
	if events := web.navigation.DrainPublications(); len(events) != 0 {
		t.Fatalf("absent assign events=%+v", events)
	}
}
