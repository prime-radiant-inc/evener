package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// seenLiveRows projects the Live section for one idle live session per entry of
// turnEnds against seen, keyed by session ID.
func seenLiveRows(t *testing.T, now time.Time, turnEnds map[string]time.Time, seen hubcore.SessionSeenSnapshot) map[string]hubapi.NavigationSessionSummary {
	t.Helper()
	var metas []schema.SessionMeta
	var live []hubcore.LiveEntry
	liveIDs := make(map[string]bool)
	for id, ended := range turnEnds {
		liveIDs[id] = true
		metas = append(metas, schema.SessionMeta{ID: id, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}})
		live = append(live, hubcore.LiveEntry{PID: len(live) + 1, SessionID: id, Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended})
	}
	tree := hubcore.BuildTreeAt(metas, live, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree, SessionSeen: seen, Live: liveIDs})
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]hubapi.NavigationSessionSummary)
	for _, row := range projection.LivePage(0, 0).Sessions {
		rows[row.SessionID] = row
	}
	return rows
}

// A live row carries when its last turn ended and, against the hub's seen
// marker and epoch, whether that turn is unseen (S4). The phone reads unseen
// as Finished and its absence as Idle, and echoes turn_ended_at back as
// seenThrough, so the millisecond round trip through the wire must be exact.
func TestNavigationRowsCarryTurnEndedAtAndUnseen(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	epoch := now.Add(-time.Hour)
	mark := now.Add(-10 * time.Minute)
	turnEnds := map[string]time.Time{
		"01SEEN":       mark,
		"01NEWER":      mark.Add(time.Millisecond),
		"01PREEPOCH":   epoch.Add(-time.Minute),
		"01POSTEPOCH":  epoch.Add(time.Minute),
		"01UNREAD":     mark.Add(-time.Minute),
		"01NEVERENDED": {},
	}
	seen := hubcore.SessionSeenSnapshot{Epoch: epoch, Records: map[hubcore.ArchiveKey]hubcore.SessionSeenRecord{
		hubcore.SessionPinKey("", "01SEEN"):   {SeenThrough: mark},
		hubcore.SessionPinKey("", "01NEWER"):  {SeenThrough: mark},
		hubcore.SessionPinKey("", "01UNREAD"): {SeenThrough: mark, Unread: true},
	}}
	rows := seenLiveRows(t, now, turnEnds, seen)
	for id, wantUnseen := range map[string]bool{
		"01SEEN": false, "01NEWER": true, "01PREEPOCH": false, "01POSTEPOCH": true, "01UNREAD": true, "01NEVERENDED": false,
	} {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("no Live row for %s in %v", id, rows)
		}
		fields := navigationSummaryJSONFields(t, row)
		if _, present := fields["unseen"]; row.Unseen != wantUnseen || present != wantUnseen {
			t.Errorf("%s: unseen = %v (key present %v), want %v: the key is absent unless it is true", id, row.Unseen, present, wantUnseen)
		}
		ended := turnEnds[id]
		raw, present := fields["turn_ended_at"]
		if present == ended.IsZero() {
			t.Fatalf("%s: turn_ended_at present = %v for a turn end of %v", id, present, ended)
		}
		if want := `"` + ended.Format(time.RFC3339Nano) + `"`; present && string(raw) != want {
			t.Errorf("%s: turn_ended_at = %s, want %s", id, raw, want)
		}
	}

	// The phone marks a row seen with Date.parse(turn_ended_at): milliseconds
	// read back off the wire. That mark must clear exactly the turn it showed.
	var shownText string
	if err := json.Unmarshal(navigationSummaryJSONFields(t, rows["01NEWER"])["turn_ended_at"], &shownText); err != nil {
		t.Fatal(err)
	}
	shown, err := time.Parse(time.RFC3339Nano, shownText)
	if err != nil {
		t.Fatal(err)
	}
	seen.Records[hubcore.SessionPinKey("", "01NEWER")] = hubcore.SessionSeenRecord{SeenThrough: hubcore.UnixMilliTime(shown.UnixMilli())}
	if seenLiveRows(t, now, turnEnds, seen)["01NEWER"].Unseen {
		t.Fatal("marking the shown turn_ended_at seen left the row unseen: the millisecond round trip is not exact")
	}
}

// Capture reads the hub's seen markers beside its pins and favorites, so a
// navigation build decorates rows from the store evener/session/seen/set
// writes.
func TestNavigationCaptureReadsTheSeenMarkers(t *testing.T) {
	store := hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "index.db"))
	through := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	if _, err := store.MarkSeen("", "01A", through); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), SessionSeen: store})
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	seen := snapshot.Inputs.SessionSeen
	if seen.Epoch.IsZero() || !seen.Records[hubcore.SessionPinKey("", "01A")].SeenThrough.Equal(through) {
		t.Fatalf("captured seen markers = %+v, want the store's epoch and the mark through %v", seen, through)
	}
}

// A navigation build owns the seen records it captured: both clone paths copy
// them.
func TestNavigationInputsCloneOwnsTheSeenRecords(t *testing.T) {
	key := hubcore.SessionPinKey("", "01A")
	inputs := navigationBuildInputs{SessionSeen: hubcore.SessionSeenSnapshot{Epoch: time.Now(), Records: map[hubcore.ArchiveKey]hubcore.SessionSeenRecord{key: {Unread: true}}}}
	contextClone, err := cloneNavigationInputsContext(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, clone := range []navigationBuildInputs{cloneNavigationInputs(inputs), contextClone} {
		delete(clone.SessionSeen.Records, key)
	}
	if !inputs.SessionSeen.Records[key].Unread {
		t.Fatal("a clone of the navigation inputs aliased the seen records")
	}
}

// A cloned summary owns its timestamps, and a summary without them clones
// without them, so the clone's JSON omits the same keys.
func TestNavigationSummaryCloneOwnsItsTimestamps(t *testing.T) {
	updatedAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	turnEndedAt := updatedAt.Add(-time.Minute)
	updated, ended := updatedAt, turnEndedAt
	original := hubapi.NavigationSessionSummary{UpdatedAt: &updated, TurnEndedAt: &ended}
	clone := cloneNavigationSummary(original)
	*original.UpdatedAt = updatedAt.Add(time.Hour)
	*original.TurnEndedAt = turnEndedAt.Add(time.Hour)
	if clone.UpdatedAt == nil || !clone.UpdatedAt.Equal(updatedAt) || clone.TurnEndedAt == nil || !clone.TurnEndedAt.Equal(turnEndedAt) {
		t.Fatalf("clone timestamps = %v, %v; want its own %v, %v", clone.UpdatedAt, clone.TurnEndedAt, updatedAt, turnEndedAt)
	}
	if bare := cloneNavigationSummary(hubapi.NavigationSessionSummary{}); bare.UpdatedAt != nil || bare.TurnEndedAt != nil {
		t.Fatalf("clone of a summary without timestamps = %v, %v; want none", bare.UpdatedAt, bare.TurnEndedAt)
	}
}

// A live row carries the hub's seen-through mark for it, floored at the
// store's epoch, so a client can tell output that streamed after the mark
// from output the person already saw, even while a turn is still running and
// has no turn end yet. With no seen store the key is absent.
func TestNavigationRowsCarrySeenThrough(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	epoch := now.Add(-time.Hour)
	mark := now.Add(-10 * time.Minute)
	turnEnds := map[string]time.Time{"01MARKED": mark, "01UNMARKED": {}, "01OLDMARK": mark}
	seen := hubcore.SessionSeenSnapshot{Epoch: epoch, Records: map[hubcore.ArchiveKey]hubcore.SessionSeenRecord{
		hubcore.SessionPinKey("", "01MARKED"):  {SeenThrough: mark},
		hubcore.SessionPinKey("", "01OLDMARK"): {SeenThrough: epoch.Add(-time.Minute)},
	}}
	rows := seenLiveRows(t, now, turnEnds, seen)
	for id, want := range map[string]time.Time{"01MARKED": mark, "01UNMARKED": epoch, "01OLDMARK": epoch} {
		raw, present := navigationSummaryJSONFields(t, rows[id])["seen_through"]
		if !present {
			t.Fatalf("%s: no seen_through", id)
		}
		if quoted := `"` + want.Format(time.RFC3339Nano) + `"`; string(raw) != quoted {
			t.Errorf("%s: seen_through = %s, want %s", id, raw, quoted)
		}
	}

	tree := hubcore.BuildTreeAt([]schema.SessionMeta{{ID: "01NOTLIVE", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}}}, []hubcore.LiveEntry{{PID: 1, SessionID: "01NOTLIVE", Status: appwire.ThreadStatusIdle}}, nil, now)
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: tree, SessionSeen: seen})
	if err != nil {
		t.Fatal(err)
	}
	notLive := projection.LivePage(0, 0).Sessions
	if len(notLive) == 0 {
		t.Fatal("no row for the session that isn't live")
	}
	for _, row := range notLive {
		if _, present := navigationSummaryJSONFields(t, row)["seen_through"]; present {
			t.Errorf("%s: seen_through present on a row that isn't live", row.SessionID)
		}
	}

	for id, row := range seenLiveRows(t, now, turnEnds, hubcore.SessionSeenSnapshot{}) {
		if _, present := navigationSummaryJSONFields(t, row)["seen_through"]; present {
			t.Errorf("%s: seen_through present with no seen store", id)
		}
	}
}
