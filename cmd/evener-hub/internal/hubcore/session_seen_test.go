package hubcore

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

var seenTestNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// newSeenTestStore opens a store at path whose clock reads *now.
func newSeenTestStore(path string, now *time.Time) *SessionSeenStore {
	store := NewSessionSeenStore(path)
	store.now = func() time.Time { return *now }
	return store
}

// setupErrorSeenStore returns a SessionSeenStore whose openDB routes through
// the shared error-injecting driver (pin_section_nonretry_test.go), so a test
// can fail one exec inside MarkBatch's transaction on demand.
func setupErrorSeenStore(t *testing.T, path string) *SessionSeenStore {
	t.Helper()
	initErrorDriver()
	store := newSeenTestStore(path, &seenTestNow)
	store.openDB = func(_, dataSourceName string) (*sql.DB, error) {
		return sql.Open(errorDriverName, dataSourceName)
	}
	return store
}

// TestSessionSeenStoreMarkBatchIsOneTransaction pins that MarkBatch's writes
// share one index.db transaction: a failure partway through must roll back
// every mark in the call, not just the one that failed, so a malformed or
// unlucky batch cannot leave some sessions marked and others not (S4).
func TestSessionSeenStoreMarkBatchIsOneTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	store := setupErrorSeenStore(t, path)
	resetErrorCounters()
	errorExecTarget.Store(2) // fail the second mark's write inside the tx
	turn := seenTestNow.Add(10 * time.Minute)
	if _, err := store.MarkBatch([]SessionSeenMark{
		{SessionID: "01A", SeenThrough: turn},
		{SessionID: "01B", SeenThrough: turn},
	}); err == nil {
		t.Fatal("MarkBatch with a failing second write returned no error")
	}
	resetErrorCounters()
	snapshot, err := newSeenTestStore(path, &seenTestNow).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 0 {
		t.Fatalf("after a failed batch, records = %+v, want none: the first write must roll back with the second", snapshot.Records)
	}
}

// TestSessionSeenStoreMarkBatchPersistsEveryMark pins the success path the
// rollback test above leaves uncovered: a multi-mark batch commits every mark
// it was given - seen and unread, across sources - not just the last one, so
// every session in a validated call lands in the snapshot.
func TestSessionSeenStoreMarkBatchPersistsEveryMark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	now := seenTestNow
	store := newSeenTestStore(path, &now)
	turnA := seenTestNow.Add(10 * time.Minute)
	turnB := seenTestNow.Add(20 * time.Minute)
	changed, err := store.MarkBatch([]SessionSeenMark{
		{SessionID: "01A", SeenThrough: turnA},
		{Source: "paradise-park", SessionID: "01B", SeenThrough: turnB},
		{SessionID: "01C", Unread: true},
	})
	if err != nil || !changed {
		t.Fatalf("MarkBatch = %v, %v; want a committed change", changed, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	want := map[ArchiveKey]SessionSeenRecord{
		SessionPinKey("", "01A"):              {SeenThrough: turnA},
		SessionPinKey("paradise-park", "01B"): {SeenThrough: turnB},
		SessionPinKey("", "01C"):              {Unread: true},
	}
	if len(snapshot.Records) != len(want) {
		t.Fatalf("records after one batch = %+v, want every mark (%d)", snapshot.Records, len(want))
	}
	for key, record := range want {
		if got := snapshot.Records[key]; !got.SeenThrough.Equal(record.SeenThrough) || got.Unread != record.Unread {
			t.Errorf("record %v = %+v, want %+v", key, got, record)
		}
	}
}

// fuzzScenarioSessionSeenStore_EpochIsSetOnceAndSurvivesReopen: the epoch is
// the store's first read and never moves after, so the first Board after an
// upgrade counts every earlier turn as seen (S4 ruling 13).
func fuzzScenarioSessionSeenStore_EpochIsSetOnceAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	now := seenTestNow
	first, err := newSeenTestStore(path, &now).Snapshot()
	if err != nil || !first.Epoch.Equal(seenTestNow) {
		t.Fatalf("first epoch = %v (%v), want %v", first.Epoch, err, seenTestNow)
	}
	now = seenTestNow.Add(time.Hour)
	again, err := newSeenTestStore(path, &now).Snapshot()
	if err != nil || !again.Epoch.Equal(seenTestNow) {
		t.Fatalf("reopened epoch = %v (%v), want still %v", again.Epoch, err, seenTestNow)
	}
}

// fuzzScenarioSessionSeenStore_MarkSeenMovesForwardOnly: a mark records the
// turn-ended time a client showed; an older one, from a device showing a stale
// Board, changes nothing, and "local" names the controller's own sessions.
func fuzzScenarioSessionSeenStore_MarkSeenMovesForwardOnly(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	turn := seenTestNow.Add(10 * time.Minute)
	for _, step := range []struct {
		source  string
		through time.Time
		changed bool
		want    time.Time
	}{
		{"", turn, true, turn},
		{"local", turn, false, turn},
		{"", turn.Add(-time.Minute), false, turn},
		{"", turn.Add(time.Millisecond), true, turn.Add(time.Millisecond)},
	} {
		changed, err := store.MarkSeen(step.source, "01A", step.through)
		if err != nil || changed != step.changed {
			t.Fatalf("MarkSeen(%q, %v) = %v, %v; want changed %v", step.source, step.through, changed, err, step.changed)
		}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if got := snapshot.Records[SessionPinKey("", "01A")].SeenThrough; !got.Equal(step.want) {
			t.Fatalf("after MarkSeen(%v): seen through %v, want %v", step.through, got, step.want)
		}
	}
}

// fuzzScenarioSessionSeenStore_UnreadOutranksSeenUntilTheNextMark: "Mark as
// unread" makes a seen session unseen and keeps its seen-through; the next
// mark (opening it again) clears it.
func fuzzScenarioSessionSeenStore_UnreadOutranksSeenUntilTheNextMark(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	key := SessionPinKey("", "01A")
	turn := seenTestNow.Add(10 * time.Minute)
	if _, err := store.MarkSeen("", "01A", turn); err != nil {
		t.Fatal(err)
	}
	for i, wantChanged := range []bool{true, false} {
		changed, err := store.MarkUnread("", "01A")
		if err != nil || changed != wantChanged {
			t.Fatalf("MarkUnread #%d = %v, %v; want changed %v", i+1, changed, err, wantChanged)
		}
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record := snapshot.Records[key]; !record.Unread || !record.SeenThrough.Equal(turn) || !snapshot.Unseen(key, turn) {
		t.Fatalf("after MarkUnread: record %+v, unseen %v; want unread and seen through %v", record, snapshot.Unseen(key, turn), turn)
	}
	if changed, err := store.MarkSeen("", "01A", turn); err != nil || !changed {
		t.Fatalf("marking the same turn seen again = %v, %v; want it to clear unread", changed, err)
	}
	if snapshot, err = store.Snapshot(); err != nil || snapshot.Unseen(key, turn) {
		t.Fatalf("after MarkSeen the session still reads unseen (%v)", err)
	}
}

// fuzzScenarioSessionSeenStore_SourcesKeepSeparateMarks: two hosts' sessions
// that share a bare ID keep separate markers.
func fuzzScenarioSessionSeenStore_SourcesKeepSeparateMarks(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	local, remote := seenTestNow.Add(time.Minute), seenTestNow.Add(2*time.Minute)
	if _, err := store.MarkSeen("", "01A", local); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkSeen("paradise-park", "01A", remote); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Records[SessionPinKey("", "01A")].SeenThrough; !got.Equal(local) {
		t.Fatalf("local mark = %v, want %v", got, local)
	}
	if got := snapshot.Records[SessionPinKey("paradise-park", "01A")].SeenThrough; !got.Equal(remote) {
		t.Fatalf("remote mark = %v, want %v", got, remote)
	}
}

// fuzzScenarioSessionSeenStore_OnChangeFiresOnlyWhenAMarkChanges: an unchanged
// mark must not invalidate navigation.
func fuzzScenarioSessionSeenStore_OnChangeFiresOnlyWhenAMarkChanges(t *testing.T) {
	now := seenTestNow
	store := newSeenTestStore(filepath.Join(t.TempDir(), "index.db"), &now)
	fired := 0
	store.SetOnChange(func() { fired++ })
	for range 2 {
		if _, err := store.MarkSeen("", "01A", seenTestNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := store.Delete("", "01A"); err != nil {
			t.Fatal(err)
		}
	}
	if fired != 2 {
		t.Fatalf("onChange fired %d times, want 2: one mark, one delete", fired)
	}
	if snapshot, err := store.Snapshot(); err != nil || len(snapshot.Records) != 0 {
		t.Fatalf("after Delete: records %+v (%v), want none", snapshot.Records, err)
	}
}

// fuzzScenarioSessionSeenStore_WithoutADatabaseIsNoStore: an unconfigured
// store writes nothing and reads as no store, so nothing reads unseen.
func fuzzScenarioSessionSeenStore_WithoutADatabaseIsNoStore(t *testing.T) {
	store := NewSessionSeenStore("")
	if changed, err := store.MarkSeen("", "01A", seenTestNow); changed || err != nil {
		t.Fatalf("MarkSeen with no database = %v, %v", changed, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || !snapshot.Epoch.IsZero() || snapshot.Unseen(SessionPinKey("", "01A"), seenTestNow) {
		t.Fatalf("snapshot with no database = %+v (%v), want no store", snapshot, err)
	}
}

// fuzzScenarioSessionSeenSnapshot_Unseen: the rule the Board's Finished band
// reads (S4).
func fuzzScenarioSessionSeenSnapshot_Unseen(t *testing.T) {
	epoch := seenTestNow
	mark := epoch.Add(10 * time.Minute)
	snapshot := SessionSeenSnapshot{Epoch: epoch, Records: map[ArchiveKey]SessionSeenRecord{
		SessionPinKey("", "seen"):   {SeenThrough: mark},
		SessionPinKey("", "unread"): {SeenThrough: mark, Unread: true},
	}}
	for _, tc := range []struct {
		name  string
		id    string
		ended time.Time
		want  bool
	}{
		{"a turn that ended exactly at its mark", "seen", mark, false},
		{"a turn that ended a millisecond after its mark", "seen", mark.Add(time.Millisecond), true},
		{"a turn that ended before its mark", "seen", mark.Add(-time.Minute), false},
		{"an unmarked turn that ended before the epoch", "unmarked", epoch.Add(-time.Minute), false},
		{"an unmarked turn that ended after the epoch", "unmarked", epoch.Add(time.Minute), true},
		{"an unread session", "unread", mark.Add(-time.Minute), true},
		{"a row with no turn-ended time", "unread", time.Time{}, false},
	} {
		if got := snapshot.Unseen(SessionPinKey("", tc.id), tc.ended); got != tc.want {
			t.Errorf("%s: unseen = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (SessionSeenSnapshot{}).Unseen(SessionPinKey("", "unmarked"), epoch.Add(time.Hour)) {
		t.Error("with no store, a session read unseen")
	}
}

// fuzzScenarioSessionSeenSnapshot_SeenThrough: a session's mark floored at the
// store's epoch, zero with no store.
func fuzzScenarioSessionSeenSnapshot_SeenThrough(t *testing.T) {
	epoch := seenTestNow
	mark := epoch.Add(10 * time.Minute)
	snapshot := SessionSeenSnapshot{Epoch: epoch, Records: map[ArchiveKey]SessionSeenRecord{
		SessionPinKey("", "marked"): {SeenThrough: mark},
		SessionPinKey("", "old"):    {SeenThrough: epoch.Add(-time.Minute)},
		SessionPinKey("", "unread"): {Unread: true},
	}}
	for id, want := range map[string]time.Time{"marked": mark, "old": epoch, "unread": epoch, "unmarked": epoch} {
		if got := snapshot.SeenThrough(SessionPinKey("", id)); !got.Equal(want) {
			t.Errorf("%s: seen through = %v, want %v", id, got, want)
		}
	}
	if got := (SessionSeenSnapshot{}).SeenThrough(SessionPinKey("", "marked")); !got.IsZero() {
		t.Errorf("with no store, seen through = %v, want zero", got)
	}
}

// fuzzScenarioSessionSeenSnapshot_CloneOwnsItsRecords: a navigation build owns
// the records it captured.
func fuzzScenarioSessionSeenSnapshot_CloneOwnsItsRecords(t *testing.T) {
	snapshot := SessionSeenSnapshot{Epoch: seenTestNow, Records: map[ArchiveKey]SessionSeenRecord{SessionPinKey("", "01A"): {Unread: true}}}
	clone := snapshot.Clone()
	delete(clone.Records, SessionPinKey("", "01A"))
	if len(snapshot.Records) != 1 {
		t.Fatal("Clone aliased the records")
	}
}

// fuzzScenarioUnixMilliTime_RoundTripsTheWireStamp: the daemon sends turn ends
// in milliseconds, and a time built from one round-trips exactly.
func fuzzScenarioUnixMilliTime_RoundTripsTheWireStamp(t *testing.T) {
	at := time.Date(2026, 9, 26, 12, 0, 0, 123_000_000, time.UTC)
	if got := UnixMilliTime(at.UnixMilli()); !got.Equal(at) || got.Location() != time.UTC {
		t.Fatalf("UnixMilliTime = %v, want %v in UTC", got, at)
	}
	if !UnixMilliTime(0).IsZero() || !UnixMilliTime(-1).IsZero() {
		t.Fatal("a zero or negative stamp is not the zero time")
	}
	if UnixMilliseconds(time.Time{}) != 0 || UnixMilliseconds(at) != at.UnixMilli() {
		t.Fatal("UnixMilliseconds does not invert UnixMilliTime")
	}
}
