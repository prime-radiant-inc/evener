package hub

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// pinSubagentFixture is a hub with one root, a subagent that has a parent, and
// a subagent whose parent link is lost (the tree still shows that one as a
// root today), all pinned into one section and favorited.
type pinSubagentFixture struct {
	web       *WebServer
	pins      *hubcore.PinSectionStore
	favorites *hubcore.FavoriteStore
	section   hubcore.PinSection
}

func newPinSubagentFixture(t *testing.T) pinSubagentFixture {
	t.Helper()
	now := timeNowForTest()
	dir := t.TempDir()
	pins := hubcore.NewPinSectionStore(filepath.Join(dir, "pins.db"))
	favorites := hubcore.NewFavoriteStore(filepath.Join(dir, "favorites.db"))
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), PinSections: pins, Favorite: favorites})
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01ROOT", UpdatedAt: now},
		{ID: "01CHILD", ParentSessionID: "01ROOT", IsSubagent: true, UpdatedAt: now},
		{ID: "01LOST", IsSubagent: true, UpdatedAt: now},
	})
	section, _, err := pins.CreateOrReuseAndAssign("Research", "", "01ROOT", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"01CHILD", "01LOST", "01UNKNOWN"} {
		if _, _, err := pins.Assign(section.ID, "", id, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"01ROOT", "01CHILD", "01LOST"} {
		if err := favorites.Set("", "session", id, true, now); err != nil {
			t.Fatal(err)
		}
	}
	return pinSubagentFixture{web: web, pins: pins, favorites: favorites, section: section}
}

func TestNavigationDropsPinnedSubagentsButKeepsTheirStoreRows(t *testing.T) {
	f := newPinSubagentFixture(t)
	snapshot, err := (webNavigationSource{web: f.web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assigned := snapshot.Inputs.PinAssignments
	for _, id := range []string{"01CHILD", "01LOST"} {
		if _, ok := assigned[hubcore.SessionPinKey("", id)]; ok {
			t.Errorf("navigation presents a pin on subagent %s", id)
		}
	}
	// A root keeps its pin, and an id no source knows keeps its dormant pin.
	for _, id := range []string{"01ROOT", "01UNKNOWN"} {
		if _, ok := assigned[hubcore.SessionPinKey("", id)]; !ok {
			t.Errorf("navigation lost the pin on %s", id)
		}
	}
	if len(snapshot.Inputs.PinSections) != 1 || snapshot.Inputs.PinSections[0].MemberCount != 2 {
		t.Fatalf("navigation sections = %+v, want one section counting only the root and the unknown id", snapshot.Inputs.PinSections)
	}

	stored, err := f.pins.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 4 {
		t.Fatalf("the store holds %d assignments after a build, want all 4 kept", len(stored))
	}
	sections, err := f.pins.Sections()
	if err != nil || len(sections) != 1 || sections[0].MemberCount != 4 {
		t.Fatalf("store sections = %+v, %v; the store keeps counting every row", sections, err)
	}
}

func TestNavigationDropsFavoritesOnSubagents(t *testing.T) {
	f := newPinSubagentFixture(t)
	snapshot, err := (webNavigationSource{web: f.web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	favorite := snapshot.Inputs.SessionFavorite
	if !favorite["01ROOT"] {
		t.Errorf("root favorite lost: %v", favorite)
	}
	for _, id := range []string{"01CHILD", "01LOST"} {
		if favorite[id] {
			t.Errorf("navigation presents a favorite on subagent %s", id)
		}
	}
	stored, err := f.favorites.Favorites()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("the store holds %d favorites after a build, want all 3 kept", len(stored))
	}
}

func TestNavigationDropsPinOnLiveSubagentWithoutMeta(t *testing.T) {
	now := timeNowForTest()
	pins := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	web := NewWebServer(hubcore.WebConfig{
		Past:        hubcore.NewPastIndex(""),
		PinSections: pins,
		Roster:      hubcore.NewRosterWithEntries(liveForPinTest("01ROOT", "01RUNNING"), liveForPinTest("01RUNNING")),
	})
	section, _, err := pins.CreateOrReuseAndAssign("Research", "", "01ROOT", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pins.Assign(section.ID, "", "01RUNNING", now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Inputs.PinAssignments[hubcore.SessionPinKey("", "01RUNNING")]; ok {
		t.Fatal("navigation presents a pin on a running subagent whose meta has not arrived")
	}
	if _, ok := snapshot.Inputs.PinAssignments[hubcore.SessionPinKey("", "01ROOT")]; !ok {
		t.Fatal("navigation lost the live root's pin")
	}
}

func TestUnpinRemovesAStaleSubagentPin(t *testing.T) {
	f := newPinSubagentFixture(t)
	for _, ref := range []string{"local:01CHILD", "01LOST", "01UNKNOWN"} {
		response, err := dispatchPinning[appwire.SessionPinUnpinResponse](t, f.web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: ref})
		if err != nil {
			t.Fatalf("unpin %s: %v", ref, err)
		}
		if !response.OK || !response.Changed {
			t.Fatalf("unpin %s = %+v, want the stale row removed", ref, response)
		}
	}
	stored, err := f.pins.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("store holds %d assignments, want only the root's", len(stored))
	}
}

func TestUnpinStillRefusesAnEmptyRef(t *testing.T) {
	f := newPinSubagentFixture(t)
	if _, err := dispatchPinning[appwire.SessionPinUnpinResponse](t, f.web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: " "}); err == nil {
		t.Fatal("unpin of an empty ref succeeded")
	}
}

func TestUnpinRemovesARemoteSubagentPin(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "th_sub", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_sub", Kind: "subagent", ParentRef: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	})
	pins := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), PinSections: pins, RemoteThreadCache: cache})
	if _, _, err := pins.CreateOrReuseAndAssign("Research", "host-a", "th_sub", timeNowForTest()); err != nil {
		t.Fatal(err)
	}
	response, err := dispatchPinning[appwire.SessionPinUnpinResponse](t, web, appwire.MethodEvenerSessionPinUnpin, appwire.SessionPinUnpinParams{SessionRef: "host-a:th_sub"})
	if err != nil || !response.Changed {
		t.Fatalf("unpin = %+v, %v; want the remote subagent pin removed", response, err)
	}
}

// classifyReferenced runs the authority for the given decision ids against the
// hub's current sources, the way a capture does.
func classifyReferenced(t *testing.T, web *WebServer, ids ...string) map[string]hubcore.FavoriteDecisionClassification {
	t.Helper()
	snapshot := web.navigationSnapshot(t.Context())
	authority, _ := web.favoriteAuthorityForReferences(snapshot, ids)
	decisions := make(map[hubcore.ArchiveKey]bool, len(ids))
	for _, id := range ids {
		decisions[hubcore.ArchiveKey{Kind: "session", ID: id}] = true
	}
	out := make(map[string]hubcore.FavoriteDecisionClassification, len(ids))
	for key, classification := range hubcore.ClassifyFavoriteDecisions(decisions, authority).Classifications {
		out[key.ID] = classification
	}
	return out
}

func TestReferencedAuthorityClassifiesRootsSubagentsAndUnknownIDs(t *testing.T) {
	f := newPinSubagentFixture(t)
	got := classifyReferenced(t, f.web, "01ROOT", "local:01ROOT", "01CHILD", "01LOST", "01UNKNOWN")
	want := map[string]hubcore.FavoriteDecisionState{
		"01ROOT":       hubcore.FavoriteDecisionValid,
		"local:01ROOT": hubcore.FavoriteDecisionValid,
		"01CHILD":      hubcore.FavoriteDecisionConfirmedInvalid,
		"01LOST":       hubcore.FavoriteDecisionConfirmedInvalid,
		"01UNKNOWN":    hubcore.FavoriteDecisionDormant,
	}
	for id, state := range want {
		if got[id].State != state {
			t.Errorf("%s classified %s, want %s", id, got[id].State, state)
		}
	}
}

func TestReferencedAuthorityKeepsRemoteSourceQuality(t *testing.T) {
	threads := []appwire.Thread{
		{ID: "th_1", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
		{ID: "th_sub", Source: "host-a", Evener: appwire.EvenerThread{Ref: "host-a:th_sub", Kind: "subagent", ParentRef: "host-a:th_1"}, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	}
	for _, test := range []struct {
		name     string
		complete bool
		want     map[string]hubcore.FavoriteDecisionState
	}{
		{"complete source", true, map[string]hubcore.FavoriteDecisionState{
			"host-a:th_1":   hubcore.FavoriteDecisionValid,
			"host-a:th_sub": hubcore.FavoriteDecisionConfirmedInvalid,
			"host-a:gone":   hubcore.FavoriteDecisionDormant,
		}},
		{"incomplete source", false, map[string]hubcore.FavoriteDecisionState{
			"host-a:th_1":   hubcore.FavoriteDecisionDormant,
			"host-a:th_sub": hubcore.FavoriteDecisionDormant,
			"host-a:gone":   hubcore.FavoriteDecisionDormant,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache := &hubcore.RemoteThreadCache{}
			cache.StoreSnapshot(threads, test.complete)
			web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), RemoteThreadCache: cache})
			got := classifyReferenced(t, web, "host-a:th_1", "host-a:th_sub", "host-a:gone")
			for id, state := range test.want {
				if got[id].State != state {
					t.Errorf("%s classified %s, want %s", id, got[id].State, state)
				}
			}
		})
	}
}

// A root with a missing parent, or a fork original, no longer goes Dormant:
// rootness comes from the shared nesting rule (spec R3).
func TestReferencedAuthorityUsesTheSharedNestingRule(t *testing.T) {
	now := timeNowForTest()
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01ORPHANFORK", ParentSessionID: "01MISSING", DivergenceTurn: 1, UpdatedAt: now},
		{ID: "01ORIG", ForkLabel: "edited", UpdatedAt: now},
		{ID: "01CONT", ParentSessionID: "01ORIG", DivergenceTurn: 1, UpdatedAt: now},
	})
	got := classifyReferenced(t, web, "01ORPHANFORK", "01ORIG", "01CONT")
	want := map[string]hubcore.FavoriteDecisionState{
		"01ORPHANFORK": hubcore.FavoriteDecisionValid,
		"01ORIG":       hubcore.FavoriteDecisionConfirmedInvalid,
		"01CONT":       hubcore.FavoriteDecisionValid,
	}
	for id, state := range want {
		if got[id].State != state {
			t.Errorf("%s classified %s, want %s", id, got[id].State, state)
		}
	}
}

// The authority reads one past-index entry per referenced id, however many
// sessions the hub holds.
func TestReferencedAuthorityWorkIsProportionalToReferencedIDs(t *testing.T) {
	now := timeNowForTest()
	metas := make([]schema.SessionMeta, 0, 2000)
	for i := range 2000 {
		metas = append(metas, schema.SessionMeta{ID: fmt.Sprintf("01BULK%05d", i), UpdatedAt: now})
	}
	metas = append(metas, schema.SessionMeta{ID: "01ROOT", UpdatedAt: now})
	past := hubcore.NewPastIndex("")
	past.SeedForTest(metas)

	lookups := 0
	local := localSessionIndex{
		roots: past.RootIndex(),
		known: func(id string) bool {
			lookups++
			_, ok := past.Lookup(id)
			return ok
		},
	}
	authorities, _ := referencedSessionAuthorities([]string{"01ROOT", "local:01ROOT", "01UNKNOWN"}, navigationSnapshot{}, local)
	if lookups != 2 {
		t.Fatalf("authority looked up %d ids for 2 distinct referenced local sessions", lookups)
	}
	if len(authorities) != 1 || authorities[0].ID != "01ROOT" {
		t.Fatalf("authorities = %+v, want only the known root", authorities)
	}
}
