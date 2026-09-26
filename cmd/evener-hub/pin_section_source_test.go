package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// newSharedBareIDPinWeb builds a hub whose tree carries one controller session
// and two remote hosts' sessions that all share the bare ID "th_1". Only a
// source-qualified pin key can keep the three apart.
func newSharedBareIDPinWeb(t *testing.T) (*WebServer, *hubcore.PinSectionStore) {
	t.Helper()
	store := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "th_1", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		{ID: "th_1", Source: "host-b", Evener: appwire.EvenerThread{Ref: "host-b:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	})
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), PinSections: store, RemoteThreadCache: cache})
	web.injectMetasForTest([]schema.SessionMeta{{ID: "th_1", UpdatedAt: timeNowForTest()}})
	return web, store
}

func pinSectionRowRefs(t *testing.T, web *WebServer, sectionID string) []string {
	t.Helper()
	result, err := web.navigation.readV2(context.Background(), navigationResourceKey{Kind: navigationResourcePinSection, SectionID: sectionID, Limit: 50}, nil)
	if err != nil {
		t.Fatalf("read pin section %s: %v", sectionID, err)
	}
	var snapshot hubapi.NavigationSnapshot
	if err := json.Unmarshal(result.Response.Data, &snapshot); err != nil {
		t.Fatalf("decode pin section %s: %v", sectionID, err)
	}
	refs := make([]string, 0, len(snapshot.Entities))
	for _, entity := range snapshot.Entities {
		var summary hubapi.NavigationSessionSummary
		if err := json.Unmarshal(entity.Value, &summary); err != nil {
			t.Fatalf("decode pin section row: %v", err)
		}
		refs = append(refs, summary.Ref)
	}
	return refs
}

// TestSessionPinAssignKeepsSameBareIDOnTwoSourcesIndependent drives the pin
// RPC with one bare session ID living on the controller and on two remote
// hosts: each source holds its own pin, the projection attributes each pin to
// its own row, and unpinning one source leaves the others.
func TestSessionPinAssignKeepsSameBareIDOnTwoSourcesIndependent(t *testing.T) {
	web, store := newSharedBareIDPinWeb(t)

	assign := func(ref, sectionName string) appwire.SessionPinAssignResponse {
		t.Helper()
		response, err := dispatchPinning[appwire.SessionPinAssignResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: ref, SectionName: &sectionName})
		if err != nil {
			t.Fatalf("assign %s: %v", ref, err)
		}
		return response
	}
	assign("host-a:th_1", "Host A")
	assign("host-b:th_1", "Host B")
	assign("local:th_1", "Local")

	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	wantSources := map[string]bool{"": false, "host-a": false, "host-b": false}
	if len(assignments) != len(wantSources) {
		t.Fatalf("assignments = %+v, want one pin per source", assignments)
	}
	for key, assignment := range assignments {
		if key.Kind != "session" || key.ID != "th_1" {
			t.Fatalf("assignment key = %+v, want the bare ID th_1", key)
		}
		if _, ok := wantSources[key.Source]; !ok {
			t.Fatalf("assignment key = %+v, want a known source", key)
		}
		if assignment.Source != key.Source || assignment.SessionID != "th_1" {
			t.Fatalf("assignment = %+v, want the key's own (source, id)", assignment)
		}
		wantSources[key.Source] = true
	}
	for source, seen := range wantSources {
		if !seen {
			t.Fatalf("no pin recorded for source %q: %+v", source, assignments)
		}
	}

	sections, err := store.Sections()
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range sections {
		want := map[string][]string{"Host A": {"host-a:th_1"}, "Host B": {"host-b:th_1"}, "Local": {"local:th_1"}}[section.Name]
		if got := pinSectionRowRefs(t, web, section.ID); len(got) != len(want) || (len(want) == 1 && got[0] != want[0]) {
			t.Fatalf("section %q rows = %v, want %v", section.Name, got, want)
		}
	}

	unpinned, err := dispatchPinning[appwire.SessionPinUnpinResponse](t, web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: "host-a:th_1"})
	if err != nil {
		t.Fatalf("unpin host-a: %v", err)
	}
	if !unpinned.OK || !unpinned.Changed || unpinned.Assignment.SessionRef != "host-a:th_1" {
		t.Fatalf("unpin response = %+v, want a changed canonical host-a receipt", unpinned)
	}
	after, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("assignments after unpinning host-a = %+v, want host-b and local", after)
	}
	for _, key := range []hubcore.ArchiveKey{
		{Kind: "session", ID: "th_1", Source: "host-b"},
		{Kind: "session", ID: "th_1"},
	} {
		if _, ok := after[key]; !ok {
			t.Fatalf("assignments after unpinning host-a = %+v, want %+v preserved", after, key)
		}
	}
}

// TestSessionPinUnqualifiedRefResolvesToLocalSource pins the legacy caller
// contract: a request that carries no source (a bare ref, or the canonical
// "local:" spelling) addresses the controller's own session, and both
// spellings name one pin.
func TestSessionPinUnqualifiedRefResolvesToLocalSource(t *testing.T) {
	web, store := newSharedBareIDPinWeb(t)

	first, err := dispatchPinning[appwire.SessionPinAssignResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "th_1", SectionName: new("Bare")})
	if err != nil {
		t.Fatalf("assign bare th_1: %v", err)
	}
	if first.Assignment.SessionRef != "local:th_1" {
		t.Fatalf("bare ref receipt = %q, want the canonical local ref", first.Assignment.SessionRef)
	}
	second, err := dispatchPinning[appwire.SessionPinAssignResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "local:th_1", SectionName: new("Canonical")})
	if err != nil {
		t.Fatalf("assign local:th_1: %v", err)
	}
	if !second.Changed {
		t.Fatalf(`"local:th_1" must address the pin "th_1" already holds: %+v`, second)
	}
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 {
		t.Fatalf("assignments = %+v, want one controller pin for both spellings", assignments)
	}
	assignment, ok := assignments[hubcore.ArchiveKey{Kind: "session", ID: "th_1"}]
	if !ok || assignment.Source != "" {
		t.Fatalf("controller pin = %+v (found %v), want the source-less key", assignment, ok)
	}
	if assignment.SectionID != second.Assignment.Section.ID {
		t.Fatalf("controller pin section = %s, want %s", assignment.SectionID, second.Assignment.Section.ID)
	}
}

// TestSessionPinUnknownSourceIsRejectedTyped pins the source-aware resolver:
// a ref that names a source the tree does not carry is refused as invalid
// parameters instead of being resolved against the controller's rows.
func TestSessionPinUnknownSourceIsRejectedTyped(t *testing.T) {
	web, store := newSharedBareIDPinWeb(t)

	_, err := dispatchPinning[appwire.SessionPinAssignResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: "ghost:th_1", SectionName: new("Ghost")})
	if err == nil {
		t.Fatal("assign from an unknown source should fail")
	}
	assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
	if got := err.Error(); !strings.Contains(got, "unknown source: ghost") {
		t.Fatalf("error = %q, want it to name the unknown source", got)
	}
	if _, err := dispatchPinning[appwire.SessionPinUnpinResponse](t, web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: "ghost:th_1"}); err == nil {
		t.Fatal("unpin from an unknown source should fail")
	}
	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 0 {
		t.Fatalf("assignments after rejected requests = %+v, want none", assignments)
	}
}

// TestSessionScrubClearsOnlyTheControllerPin pins the read side of the same
// key: scrubbing the controller's session removes its pin and leaves the
// remote host's pin on the same bare ID untouched.
func TestSessionScrubClearsOnlyTheControllerPin(t *testing.T) {
	web, store := newSharedBareIDPinWeb(t)

	for ref, sectionName := range map[string]string{"host-a:th_1": "Host A", "local:th_1": "Local"} {
		if _, err := dispatchPinning[appwire.SessionPinAssignResponse](t, web, appwire.MethodEvenerSessionPinAssign, appwire.SessionPinAssignParams{SessionRef: ref, SectionName: new(sectionName)}); err != nil {
			t.Fatalf("assign %s: %v", ref, err)
		}
	}
	if failures := web.scrubSessionDecisions("th_1"); len(failures) != 0 {
		t.Fatalf("scrub failures = %v", failures)
	}

	assignments, err := store.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 {
		t.Fatalf("assignments after scrubbing the controller session = %+v, want only host-a", assignments)
	}
	hostA, ok := assignments[hubcore.ArchiveKey{Kind: "session", ID: "th_1", Source: "host-a"}]
	if !ok || hostA.SectionID == "" {
		t.Fatalf("host-a pin after the controller scrub = %+v (found %v), want it preserved", hostA, ok)
	}
}
