package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func topLevelMeta(id string) schema.SessionMeta {
	return schema.SessionMeta{ID: id, UpdatedAt: timeNowForTest()}
}

func newPinNavigationAppWireWeb(t *testing.T, withSection bool) (*WebServer, *hubcore.PinSection) {
	t.Helper()
	past := hubcore.NewPastIndex("")
	store := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: store})
	web.injectMetasForTest([]schema.SessionMeta{topLevelMeta("session-a"), topLevelMeta("session-b")})
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}
	if !withSection {
		return web, nil
	}
	section, _, err := store.CreateOrReuseAndAssign("Research", "session-a", time.Unix(1_700_000_000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := web.navigation.Refresh(t.Context(), navigationChangeHint{}); err != nil {
		t.Fatal(err)
	}
	web.navigation.DrainPublications()
	return web, &section
}

func dispatchPinning[T any](t *testing.T, web *WebServer, method string, params any) (T, error) {
	t.Helper()
	var zero T
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
		ID: appwire.NewIntID(1), Method: method, Params: raw,
	})
	if err != nil {
		return zero, err
	}
	response, ok := result.(T)
	if !ok {
		t.Fatalf("%s response type = %T, want %T", method, result, zero)
	}
	return response, nil
}

func assertAppWirePinNavigationPublication(t *testing.T, web *WebServer, mutation appwire.NavigationMutation, expected []appwire.NavigationInvalidationTarget) {
	t.Helper()
	published := web.navigation.DrainPublications()
	if len(published) != 1 {
		t.Fatalf("published events=%d, want exactly one: %+v", len(published), published)
	}
	if mutation.GenerationID != published[0].GenerationID || !reflect.DeepEqual(mutation.Targets, published[0].Targets) {
		t.Fatalf("response navigation=%+v, publication=%+v", mutation, published[0])
	}
	if len(mutation.Targets) != len(expected) {
		t.Fatalf("targets=%+v, want exact targets=%+v", mutation.Targets, expected)
	}
	for index, target := range mutation.Targets {
		want := expected[index]
		if target.Revision == 0 {
			t.Fatalf("target[%d]=%+v, want positive revision", index, target)
		}
		if target.Kind != want.Kind || target.Section != want.Section || target.SectionID != want.SectionID || target.Catalog != want.Catalog || target.ProjectKey != want.ProjectKey {
			t.Fatalf("target[%d]=%+v, want exact identity=%+v", index, target, want)
		}
	}
	if replay := web.navigation.DrainPublications(); len(replay) != 0 {
		t.Fatalf("publisher replayed events: %+v", replay)
	}
}

func assertAppWirePinNoNavigation(t *testing.T, web *WebServer, mutation appwire.NavigationMutation, generation string) {
	t.Helper()
	if mutation.GenerationID != generation {
		t.Fatalf("no-op generation=%q, want %q", mutation.GenerationID, generation)
	}
	if len(mutation.Targets) != 0 {
		t.Fatalf("no-op navigation targets=%+v, want empty", mutation.Targets)
	}
	if events := web.navigation.DrainPublications(); len(events) != 0 {
		t.Fatalf("no-op published events=%+v, want empty", events)
	}
}

func TestHubSessionPinAssignAndUnpinPreserveCanonicalIdempotentReceipts(t *testing.T) {
	attentionCalls := 0
	past := hubcore.NewPastIndex("")
	store := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	web := NewWebServer(hubcore.WebConfig{Past: past, PinSections: store, PokeAttention: func() { attentionCalls++ }})
	web.injectMetasForTest([]schema.SessionMeta{topLevelMeta("session-a"), topLevelMeta("session-b")})
	if _, err := web.navigation.Representation(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}); err != nil {
		t.Fatal(err)
	}

	name := "Research"
	assigned, err := dispatchPinning[appwire.SessionPinMutationResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:session-a", SectionName: &name})
	if err != nil {
		t.Fatalf("assign by name: %v", err)
	}
	if !assigned.OK || !assigned.Changed || assigned.Assignment.SessionRef != "local:session-a" || assigned.Assignment.Section == nil || assigned.Assignment.Section.Name != "Research" || assigned.Assignment.Section.MemberCount != 1 {
		t.Fatalf("assign response=%+v", assigned)
	}
	sectionID := assigned.Assignment.Section.ID
	assertAppWirePinNavigationPublication(t, web, assigned.Navigation, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetManifest}, {Kind: appwire.NavigationTargetPinCatalog}, {Kind: appwire.NavigationTargetPinSection, SectionID: sectionID}, {Kind: appwire.NavigationTargetProject, ProjectKey: "no-project"}})

	normalized := " research "
	repeat, err := dispatchPinning[appwire.SessionPinMutationResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "session-a", SectionName: &normalized})
	if err != nil {
		t.Fatalf("repeat assign: %v", err)
	}
	if !repeat.OK || repeat.Changed || repeat.Assignment.Section == nil || repeat.Assignment.Section.ID != sectionID || repeat.Assignment.Section.MemberCount != 1 {
		t.Fatalf("repeat assign response=%+v", repeat)
	}
	assertAppWirePinNoNavigation(t, web, repeat.Navigation, assigned.Navigation.GenerationID)

	byID, err := dispatchPinning[appwire.SessionPinMutationResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:session-b", SectionID: &sectionID})
	if err != nil {
		t.Fatalf("assign by id: %v", err)
	}
	if !byID.Changed || byID.Assignment.Section == nil || byID.Assignment.Section.MemberCount != 2 {
		t.Fatalf("assign by id response=%+v", byID)
	}
	assertAppWirePinNavigationPublication(t, web, byID.Navigation, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetPinCatalog}, {Kind: appwire.NavigationTargetPinSection, SectionID: sectionID}, {Kind: appwire.NavigationTargetProject, ProjectKey: "no-project"}})

	unpinned, err := dispatchPinning[appwire.SessionPinMutationResponse](t, web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: "local:session-a"})
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if !unpinned.OK || !unpinned.Changed || unpinned.Assignment.SessionRef != "local:session-a" || unpinned.Assignment.Section != nil {
		t.Fatalf("unpin response=%+v", unpinned)
	}
	assertAppWirePinNavigationPublication(t, web, unpinned.Navigation, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetPinCatalog}, {Kind: appwire.NavigationTargetPinSection, SectionID: sectionID}, {Kind: appwire.NavigationTargetProject, ProjectKey: "no-project"}})

	repeatUnpin, err := dispatchPinning[appwire.SessionPinMutationResponse](t, web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: "local:session-a"})
	if err != nil {
		t.Fatalf("repeat unpin: %v", err)
	}
	if !repeatUnpin.OK || repeatUnpin.Changed || repeatUnpin.Assignment.SessionRef != "local:session-a" {
		t.Fatalf("repeat unpin response=%+v", repeatUnpin)
	}
	assertAppWirePinNoNavigation(t, web, repeatUnpin.Navigation, unpinned.Navigation.GenerationID)
	if attentionCalls != 3 {
		t.Fatalf("attention calls=%d, want one for each changed mutation", attentionCalls)
	}
}

func TestHubPinSectionRenameAndDeletePreserveCanonicalReceipts(t *testing.T) {
	web, section := newPinNavigationAppWireWeb(t, true)

	renamed, err := dispatchPinning[appwire.PinSectionRenameResponse](t, web, appwire.MethodEvenerPinSectionRename, appwire.PinSectionRenameParams{SectionID: section.ID, Name: "RESEARCH"})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !renamed.OK || !renamed.Changed || renamed.Section.ID != section.ID || renamed.Section.Name != "RESEARCH" || renamed.Section.MemberCount != 1 {
		t.Fatalf("rename response=%+v", renamed)
	}
	assertAppWirePinNavigationPublication(t, web, renamed.Navigation, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetPinCatalog}})

	repeat, err := dispatchPinning[appwire.PinSectionRenameResponse](t, web, appwire.MethodEvenerPinSectionRename, appwire.PinSectionRenameParams{SectionID: section.ID, Name: "RESEARCH"})
	if err != nil {
		t.Fatalf("repeat rename: %v", err)
	}
	if !repeat.OK || repeat.Changed || repeat.Section.Name != "RESEARCH" {
		t.Fatalf("repeat rename response=%+v", repeat)
	}
	assertAppWirePinNoNavigation(t, web, repeat.Navigation, renamed.Navigation.GenerationID)

	deleted, err := dispatchPinning[appwire.PinSectionDeleteResponse](t, web, appwire.MethodEvenerPinSectionDelete, appwire.PinSectionDeleteParams{SectionID: section.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted.OK || !deleted.Changed || deleted.MemberCount != 1 {
		t.Fatalf("delete response=%+v", deleted)
	}
	assertAppWirePinNavigationPublication(t, web, deleted.Navigation, []appwire.NavigationInvalidationTarget{{Kind: appwire.NavigationTargetManifest}, {Kind: appwire.NavigationTargetPinCatalog}, {Kind: appwire.NavigationTargetPinSection, SectionID: section.ID}, {Kind: appwire.NavigationTargetProject, ProjectKey: "no-project"}})
}

func TestHubPinningErrorsPreserveTypedFailureKinds(t *testing.T) {
	web, section := newPinNavigationAppWireWeb(t, true)
	second, _, err := web.cfg.PinSections.CreateOrReuseAndAssign("Writing", "session-b", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	name := "Other"
	tests := []struct {
		name      string
		method    string
		params    any
		code      int
		errorInfo appwire.ErrorInfo
		message   string
	}{
		{"assign neither selector", appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:session-a"}, appwire.CodeInvalidParams, appwire.ErrorInvalidParams, "exactly one"},
		{"assign both selectors", appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:session-a", SectionID: &section.ID, SectionName: &name}, appwire.CodeInvalidParams, appwire.ErrorInvalidParams, "exactly one"},
		{"assign unknown section", appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:session-a", SectionID: stringPointerForHub("missing")}, appwire.CodeInvalidParams, appwire.ErrorResourceNotFound, hubcore.ErrPinSectionNotFound.Error()},
		{"assign nested session", appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:missing", SectionID: &section.ID}, appwire.CodeInvalidParams, appwire.ErrorInvalidParams, "real top-level session"},
		{"rename conflict", appwire.MethodEvenerPinSectionRename, appwire.PinSectionRenameParams{SectionID: section.ID, Name: second.Name}, appwire.CodeConflict, appwire.ErrorConflict, hubcore.ErrPinSectionConflict.Error()},
		{"rename missing", appwire.MethodEvenerPinSectionRename, appwire.PinSectionRenameParams{SectionID: "missing", Name: "Other"}, appwire.CodeInvalidParams, appwire.ErrorResourceNotFound, hubcore.ErrPinSectionNotFound.Error()},
		{"delete missing", appwire.MethodEvenerPinSectionDelete, appwire.PinSectionDeleteParams{SectionID: "missing"}, appwire.CodeInvalidParams, appwire.ErrorResourceNotFound, hubcore.ErrPinSectionNotFound.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := dispatchPinning[any](t, web, test.method, test.params)
			assertNavigationWireError(t, err, test.code, test.errorInfo)
			if got := err.Error(); !strings.Contains(got, test.message) {
				t.Fatalf("error=%q, want %q", got, test.message)
			}
			if events := web.navigation.DrainPublications(); len(events) != 0 {
				t.Fatalf("failed mutation published events=%+v", events)
			}
		})
	}
}

func TestHubPinningRequiresConfiguredStore(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	_, err := dispatchPinning[appwire.PinSectionDeleteResponse](t, web, appwire.MethodEvenerPinSectionDelete, appwire.PinSectionDeleteParams{SectionID: "missing"})
	assertNavigationWireError(t, err, appwire.CodeInternalError, appwire.ErrorInternal)
}

func stringPointerForHub(value string) *string { return &value }
