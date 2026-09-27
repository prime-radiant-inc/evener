package hub

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func newSessionSeenWeb(t *testing.T) (*WebServer, *hubcore.SessionSeenStore) {
	t.Helper()
	store := hubcore.NewSessionSeenStore(filepath.Join(t.TempDir(), "index.db"))
	return NewWebServer(hubcore.WebConfig{Past: hubcore.NewPastIndex(""), SessionSeen: store}), store
}

func dispatchSessionSeenSet(t *testing.T, web *WebServer, marks ...appwire.SessionSeenMark) (appwire.SessionSeenSetResponse, error) {
	t.Helper()
	return dispatchPinning[appwire.SessionSeenSetResponse](t, web, appwire.MethodEvenerSessionSeenSet, appwire.SessionSeenSetParams{Sessions: marks})
}

// A mark records the turn end a client showed and reports whether it changed
// anything; the same mark again changes nothing and commits no navigation
// targets. Unread is its own mark and keeps the seen-through.
func TestSessionSeenSetRecordsMarksAndReportsChange(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	through := time.Date(2026, 9, 26, 11, 58, 0, 123_000_000, time.UTC)
	mark := appwire.SessionSeenMark{Ref: "local:01A", SeenThrough: through.UnixMilli()}

	if first, err := dispatchSessionSeenSet(t, web, mark); err != nil || !first.OK || !first.Changed {
		t.Fatalf("first mark = %+v (%v), want a committed change", first, err)
	}
	if again, err := dispatchSessionSeenSet(t, web, mark); err != nil || !again.OK || again.Changed || len(again.Navigation.Targets) != 0 {
		t.Fatalf("repeated mark = %+v (%v), want no change and no targets", again, err)
	}
	if unread, err := dispatchSessionSeenSet(t, web, appwire.SessionSeenMark{Ref: "local:01A", Unread: true}); err != nil || !unread.Changed {
		t.Fatalf("unread mark = %+v (%v), want a change", unread, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if record := snapshot.Records[hubcore.SessionPinKey("", "01A")]; !record.SeenThrough.Equal(through) || !record.Unread {
		t.Fatalf("stored record = %+v, want seen through %v and unread", record, through)
	}
}

// A malformed call is refused before anything is written: one bad mark fails
// the whole call.
func TestSessionSeenSetRefusesAMalformedCallWithoutWriting(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	good := appwire.SessionSeenMark{Ref: "local:01GOOD", SeenThrough: 1_790_000_000_000}
	tooMany := make([]appwire.SessionSeenMark, maxSessionSeenMarks+1)
	for i := range tooMany {
		tooMany[i] = good
	}
	for name, marks := range map[string][]appwire.SessionSeenMark{
		"no sessions":                    nil,
		"too many sessions":              tooMany,
		"a malformed ref":                {good, {Ref: "not a ref", SeenThrough: 1}},
		"both seenThrough and unread":    {good, {Ref: "local:01B", SeenThrough: 1, Unread: true}},
		"neither seenThrough nor unread": {good, {Ref: "local:01B"}},
		"a negative seenThrough":         {good, {Ref: "local:01B", SeenThrough: -1, Unread: true}},
		"an unknown source":              {good, {Ref: "ghost:01B", SeenThrough: 1}},
		"a seenThrough days ahead":       {good, {Ref: "local:01B", SeenThrough: time.Now().Add(48 * time.Hour).UnixMilli()}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := dispatchSessionSeenSet(t, web, marks...)
			assertNavigationWireError(t, err, appwire.CodeInvalidParams, appwire.ErrorInvalidParams)
		})
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 0 {
		t.Fatalf("records after refused calls = %+v, want none", snapshot.Records)
	}
}

// Deleting a controller session forgets its marker and leaves a remote host's
// marker on the same bare ID alone.
func TestSessionScrubClearsOnlyTheControllerSeenMarker(t *testing.T) {
	web, store := newSessionSeenWeb(t)
	through := time.Date(2026, 9, 26, 11, 58, 0, 0, time.UTC)
	for _, source := range []string{"", "host-a"} {
		if _, err := store.MarkSeen(source, "th_1", through); err != nil {
			t.Fatal(err)
		}
	}
	if failures := web.scrubSessionDecisions("th_1"); len(failures) != 0 {
		t.Fatalf("scrub failures = %v", failures)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, local := snapshot.Records[hubcore.SessionPinKey("", "th_1")]; local {
		t.Fatal("the controller's marker survived its session's deletion")
	}
	if _, remote := snapshot.Records[hubcore.SessionPinKey("host-a", "th_1")]; !remote {
		t.Fatal("scrubbing the controller session dropped a remote host's marker")
	}
}

// The hub opens its seen store in index.db and wires its change hook, so a
// mark from any writer invalidates navigation.
func TestRunMainWiresTheSeenMarkerStore(t *testing.T) {
	_, cfg, deps := newTraceMainTestDeps(t)
	var web *WebServer
	deps.afterWeb = func(created *WebServer) { web = created }
	var stderr bytes.Buffer
	if err := runMain([]string{"-addr", cfg.Addr, "-evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("runMain: %v, stderr=%s", err, stderr.String())
	}
	if web == nil || web.cfg.SessionSeen == nil {
		t.Fatal("the hub started without a seen-marker store")
	}
	_, before, _ := web.navigation.snapshotPendingHint()
	if changed, err := web.cfg.SessionSeen.MarkSeen("", "01A", time.Now()); err != nil || !changed {
		t.Fatalf("MarkSeen = %v, %v; want a change", changed, err)
	}
	if _, after, _ := web.navigation.snapshotPendingHint(); after <= before {
		t.Fatal("a seen mark did not invalidate navigation")
	}
}
