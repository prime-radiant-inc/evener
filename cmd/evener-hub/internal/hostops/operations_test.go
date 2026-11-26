package hostops

// Operations read tests (deploy pipeline 08b §4, §8, §10; §12's "Operations
// incarnation scope" and "Protocol shapes" rows). The store-level half: the
// bounded listing in id-ascending order, the limit's default and cap, the
// host-pinned effective pair, the unfiltered current window, the query filters,
// the compacted-record detail replay, and the read's one-mutex, no-write
// posture.

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// readTestRecord persists one pending deploy record pinned to the pair.
func readTestRecord(t *testing.T, store *Store, host, clientOperationID string, generation uint64, incarnationID string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{
		ClientOperationID: clientOperationID,
		Host:              host,
		Kind:              KindDeploy,
		Generation:        generation,
		IncarnationID:     incarnationID,
	})
	if err != nil {
		t.Fatalf("Create(%s/%s): %v", host, clientOperationID, err)
	}
	return record
}

// mirrorTestBoundary writes one host's boundary triple via the production
// HostRecords mirror path.
func mirrorTestBoundary(t *testing.T, store *Store, host string, generation uint64, incarnationID string, presenceEpoch uint64) {
	t.Helper()
	mirror := HostMirror{
		Boundaries: map[string]Boundary{
			host: {Generation: generation, IncarnationID: incarnationID, PresenceEpoch: presenceEpoch},
		},
	}
	family, _, imported := store.HostRecords()
	if !imported {
		if err := store.ImportHostRecords(HostRecords{}, mirror); err != nil {
			t.Fatalf("ImportHostRecords(%s): %v", host, err)
		}
		return
	}
	if err := store.CommitHostRecords(family, mirror); err != nil {
		t.Fatalf("CommitHostRecords(%s): %v", host, err)
	}
}

// setRecordCreatedAt stamps one stored record's display-only createdAt, so a
// test can model a wall-clock rollback that stamps a later record earlier.
func setRecordCreatedAt(t *testing.T, store *Store, id string, at time.Time) {
	t.Helper()
	store.cell.mu.Lock()
	defer store.cell.mu.Unlock()
	for i := range store.cell.state.Records {
		if store.cell.state.Records[i].ID == id {
			store.cell.state.Records[i].CreatedAt = at
			return
		}
	}
	t.Fatalf("no stored record %q", id)
}

// quarantineEpoch is the test-only probe for the durable quarantine counter
// the store was opened at.
func (s *Store) quarantineEpoch() uint64 {
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	return s.cell.quarantineEpoch
}

// recordIDs is the ids of a page's records, for failure messages.
func recordIDs(records []Record) []string {
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}

// TestOperationsReadListsByIDAscending pins §8's ordering: records sort by the
// controller-assigned id ascending, and createdAt is display-only, so a
// wall-clock rollback that stamps a later record earlier never moves it.
func TestOperationsReadListsByIDAscending(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	first := readTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	second := readTestRecord(t, store, "m4", "op-2", 2, "inc-m4")
	third := readTestRecord(t, store, "m4", "op-3", 2, "inc-m4")
	// The rollback: the newest record's display-only stamp lands before the
	// oldest's. The listing must still follow the ids.
	setRecordCreatedAt(t, store, third.ID, first.CreatedAt.Add(-time.Hour))
	if first.ID >= second.ID || second.ID >= third.ID {
		t.Fatalf("ids are not ascending: %q %q %q", first.ID, second.ID, third.ID)
	}

	page, err := store.ReadOperations(OperationsQuery{Limit: 2})
	if err != nil {
		t.Fatalf("ReadOperations(limit 2): %v", err)
	}
	if len(page.Records) != 2 || page.Records[0].ID != first.ID || page.Records[1].ID != second.ID {
		t.Fatalf("limited listing ids = %v, want %q, %q", recordIDs(page.Records), first.ID, second.ID)
	}

	page, err = store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if len(page.Records) != 3 ||
		page.Records[0].ID != first.ID || page.Records[1].ID != second.ID || page.Records[2].ID != third.ID {
		t.Fatalf("listing ids = %v, want %q, %q, %q", recordIDs(page.Records), first.ID, second.ID, third.ID)
	}
}

// TestOperationsReadLimitDefaultsAndCaps pins §8's "limit defaults to 50 and
// caps at 200. Responses never exceed the cap."
func TestOperationsReadLimitDefaultsAndCaps(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	for i := range 205 {
		readTestRecord(t, store, "m4", fmt.Sprintf("op-%03d", i), 2, "inc-m4")
	}
	page, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations(default limit): %v", err)
	}
	if len(page.Records) != DefaultOperationsLimit {
		t.Fatalf("default limit returned %d records, want %d", len(page.Records), DefaultOperationsLimit)
	}
	page, err = store.ReadOperations(OperationsQuery{Limit: 300})
	if err != nil {
		t.Fatalf("ReadOperations(limit 300): %v", err)
	}
	if len(page.Records) != MaxOperationsLimit {
		t.Fatalf("over-cap limit returned %d records, want the %d cap", len(page.Records), MaxOperationsLimit)
	}
	page, err = store.ReadOperations(OperationsQuery{Limit: 7})
	if err != nil {
		t.Fatalf("ReadOperations(limit 7): %v", err)
	}
	if len(page.Records) != 7 {
		t.Fatalf("limit 7 returned %d records", len(page.Records))
	}
	if _, err := store.ReadOperations(OperationsQuery{Limit: -1}); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("negative limit error = %v, want ErrInvalidOperationsQuery", err)
	}
}

// TestOperationsReadRefusesInvalidParams pins the read's caller-side refusals:
// a state outside the closed set, an incarnationId without its generation, and
// a pair filter without the host name it pins.
func TestOperationsReadRefusesInvalidParams(t *testing.T) {
	store, _ := openTestStore(t)
	if _, err := store.ReadOperations(OperationsQuery{State: State("bogus")}); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("unknown state = %v, want ErrInvalidOperationsQuery", err)
	}
	if _, err := store.ReadOperations(OperationsQuery{IncarnationID: "inc-m4"}); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("incarnationId without generation = %v, want ErrInvalidOperationsQuery", err)
	}
	generation := uint64(2)
	if _, err := store.ReadOperations(OperationsQuery{Generation: &generation}); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("generation without host = %v, want ErrInvalidOperationsQuery", err)
	}
}

// TestOperationsReadHostPinnedPageEchoesThePair pins §8/§10's host-pinned
// shape: generation/incarnationId present exactly on host-pinned reads, and the
// effective pair is the one actually listed — the request's pair when it names
// one (superseded pairs included), the current pair otherwise.
func TestOperationsReadHostPinnedPageEchoesThePair(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	readTestRecord(t, store, "m4", "op-current", 2, "inc-m4")
	old := readTestRecord(t, store, "m4", "op-old", 1, "inc-old")
	// A second host's record must never leak into a host-pinned read.
	mirrorTestBoundary(t, store, "m9", 5, "inc-m9", 1)
	readTestRecord(t, store, "m9", "op-m9", 5, "inc-m9")

	page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
	if err != nil {
		t.Fatalf("ReadOperations(m4): %v", err)
	}
	if page.Generation == nil || *page.Generation != 2 || page.IncarnationID != "inc-m4" {
		t.Fatalf("page pair = %v/%q, want the current 2/inc-m4", page.Generation, page.IncarnationID)
	}
	for _, record := range page.Records {
		if record.Host != "m4" {
			t.Fatalf("host-pinned read leaked host %q", record.Host)
		}
	}

	generation := uint64(1)
	page, err = store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-old"})
	if err != nil {
		t.Fatalf("ReadOperations(m4, 1/inc-old): %v", err)
	}
	if page.Generation == nil || *page.Generation != 1 || page.IncarnationID != "inc-old" {
		t.Fatalf("page pair = %v/%q, want the listed 1/inc-old", page.Generation, page.IncarnationID)
	}
	if len(page.Records) != 1 || page.Records[0].ID != old.ID {
		t.Fatalf("superseded pair listed %v, want only %q", recordIDs(page.Records), old.ID)
	}

	// §12 Operations incarnation scope: a generation plus incarnationId pair
	// addresses the colliding same-generation incarnation, and the response
	// echoes the listed pair.
	colliding := readTestRecord(t, store, "m4", "op-colliding", 1, "inc-other")
	page, err = store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-other"})
	if err != nil {
		t.Fatalf("ReadOperations(m4, 1/inc-other): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != colliding.ID {
		t.Fatalf("colliding incarnation listed %v, want only %q", recordIDs(page.Records), colliding.ID)
	}
	if page.IncarnationID != "inc-other" {
		t.Fatalf("page incarnation = %q, want the listed inc-other", page.IncarnationID)
	}
}

// TestOperationsReadFiltersByOperationIDStateAndDetailID pins §10's filters:
// operationId matches the client-supplied clientOperationId (never the
// controller-assigned id), state selects the exact durable state, and id is the
// detail filter for the controller-assigned record id.
func TestOperationsReadFiltersByOperationIDStateAndDetailID(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	first := readTestRecord(t, store, "m4", "op-alpha", 2, "inc-m4")
	second := readTestRecord(t, store, "m4", "op-beta", 2, "inc-m4")
	if _, err := store.TransitionToState(second.ID, StateFailed, &Result{OK: false, Message: "boom"}, ""); err != nil {
		t.Fatalf("TransitionToState: %v", err)
	}

	page, err := store.ReadOperations(OperationsQuery{ClientOperationID: "op-alpha"})
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != first.ID {
		t.Fatalf("operationId filter = %v, %v; want only %q", recordIDs(page.Records), err, first.ID)
	}
	page, err = store.ReadOperations(OperationsQuery{State: StateFailed})
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != second.ID {
		t.Fatalf("state filter = %v, %v; want only %q", recordIDs(page.Records), err, second.ID)
	}
	page, err = store.ReadOperations(OperationsQuery{ID: second.ID})
	if err != nil || len(page.Records) != 1 || page.Records[0].ID != second.ID {
		t.Fatalf("id filter = %v, %v; want only %q", recordIDs(page.Records), err, second.ID)
	}
	if page, err = store.ReadOperations(OperationsQuery{ID: first.ID, State: StateFailed}); err != nil || len(page.Records) != 0 {
		t.Fatalf("combined filters = %v, %v; want no records", recordIDs(page.Records), err)
	}
}

// TestOperationsReadPinnedDetailEchoesTheListedRecordPair pins §8's "effective
// generation and incarnationId actually listed" for the `id` detail filter: a
// host-pinned lookup that names no pair echoes the row it returned, never the
// host's current pair.
func TestOperationsReadPinnedDetailEchoesTheListedRecordPair(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	old := readTestRecord(t, store, "m4", "op-old-1", 1, "inc-old")
	readTestRecord(t, store, "m4", "op-old-2", 1, "inc-old")
	readTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", ID: old.ID, Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(detail): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != old.ID {
		t.Fatalf("detail page ids = %v, want %q", recordIDs(page.Records), old.ID)
	}
	if page.Generation == nil || *page.Generation != 1 || page.IncarnationID != "inc-old" {
		t.Fatalf("detail page pair = %v/%q, want the listed row's 1/inc-old", page.Generation, page.IncarnationID)
	}
}

// TestOperationsReadRefusesASupersededGenerationWithoutIncarnation pins §10's
// "incarnationId ... required alongside generation whenever the caller names a
// superseded pair": a generation-only filter for a generation that is not the
// host's current one is invalid params, never the silent selection of whichever
// incarnation happened to be newest. The current generation still resolves
// without it.
func TestOperationsReadRefusesASupersededGenerationWithoutIncarnation(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	readTestRecord(t, store, "m4", "op-old-a", 1, "inc-old-a")
	readTestRecord(t, store, "m4", "op-old-b", 1, "inc-old-b")
	current := readTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

	superseded := uint64(1)
	if _, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &superseded}); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("superseded generation-only read = %v, want ErrInvalidOperationsQuery", err)
	}
	stillCurrent := uint64(2)
	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &stillCurrent})
	if err != nil {
		t.Fatalf("current generation-only read: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != current.ID {
		t.Fatalf("current generation-only read ids = %v, want %q", recordIDs(page.Records), current.ID)
	}
	if page.IncarnationID != "inc-m4" {
		t.Fatalf("current generation-only read pair = %v/%q, want the current incarnation", page.Generation, page.IncarnationID)
	}
}

// TestOperationsReadUnfilteredListsTheCurrentWindow pins §10's "generation ...
// omitted: the current generation" for unfiltered reads: each host contributes
// its current pair's records, and history stays reachable through the
// host-pinned pair filter and the id detail filter.
func TestOperationsReadUnfilteredListsTheCurrentWindow(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	old := readTestRecord(t, store, "m4", "op-old", 1, "inc-old")
	current := readTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

	page, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != current.ID {
		t.Fatalf("unfiltered page ids = %v, want only the current pair's %q", recordIDs(page.Records), current.ID)
	}
	if page.Generation != nil {
		t.Fatalf("unfiltered read echoed generation %d, want no top-level pair", *page.Generation)
	}
	// History stays readable directly.
	generation := uint64(1)
	history, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-old"})
	if err != nil || len(history.Records) != 1 || history.Records[0].ID != old.ID {
		t.Fatalf("pinned history read = %v, %v; want %q", recordIDs(history.Records), err, old.ID)
	}
	detail, err := store.ReadOperations(OperationsQuery{ID: old.ID})
	if err != nil || len(detail.Records) != 1 || detail.Records[0].ID != old.ID {
		t.Fatalf("detail read = %v, %v; want %q", recordIDs(detail.Records), err, old.ID)
	}
}

// TestOperationsReadIsReadOnlyOnTheStoreFile pins that a read never rewrites
// the store: the records are only read.
func TestOperationsReadIsReadOnlyOnTheStoreFile(t *testing.T) {
	store, path := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	readTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	readTestRecord(t, store, "m4", "op-2", 2, "inc-m4")
	before := mustReadFile(t, path)

	if _, err := store.ReadOperations(OperationsQuery{Limit: 1}); err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if _, err := store.ReadOperations(OperationsQuery{}); err != nil {
		t.Fatalf("ReadOperations(unfiltered): %v", err)
	}
	if after := mustReadFile(t, path); !bytes.Equal(before, after) {
		t.Fatal("reads rewrote the store file")
	}
}

// TestOperationsReadGroundsARecordHostWithNoMirroredBoundary pins the one state
// the store's own writers never produce but the custody import does: a host
// holding records but no boundary triple. The read grounds the host's pair in
// its own newest record, so the closed name stays addressable and listable.
func TestOperationsReadGroundsARecordHostWithNoMirroredBoundary(t *testing.T) {
	store, _ := openTestStore(t)
	readTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
	if err != nil {
		t.Fatalf("host-pinned err = %v, want the record's own pinned pair", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID == "" {
		t.Fatalf("host-pinned page = %+v, want the record listed", page.Records)
	}
	if page.Generation == nil || *page.Generation != 2 || page.IncarnationID != "inc-m4" {
		t.Fatalf("host-pinned page pair = %v/%q, want the record's own pair", page.Generation, page.IncarnationID)
	}
	if _, err := store.ReadOperations(OperationsQuery{ID: page.Records[0].ID}); err != nil {
		t.Fatalf("id detail err = %v, want the record addressable by id", err)
	}
	unfiltered, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("unfiltered err = %v", err)
	}
	if len(unfiltered.Records) != 1 || unfiltered.Records[0].Host != "m4" {
		t.Fatalf("unfiltered page = %+v, want the record listed", unfiltered.Records)
	}
}

// TestOperationsReadSerializesUnderTheStoreMutex pins §4's one store mutex: a
// read racing writes observes complete records and never trips the race
// detector.
func TestOperationsReadSerializesUnderTheStoreMutex(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorTestBoundary(t, store, "m4", 2, "inc-m4", 3)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				if _, err := store.ReadOperations(OperationsQuery{Limit: 5}); err != nil {
					t.Errorf("ReadOperations: %v", err)
					return
				}
			}
		})
	}
	for i := range 20 {
		wg.Go(func() {
			if _, err := store.Create(NewRecord{
				ClientOperationID: fmt.Sprintf("op-%02d", i),
				Host:              "m4",
				Kind:              KindDeploy,
				Generation:        2,
				IncarnationID:     "inc-m4",
			}); err != nil {
				t.Errorf("Create: %v", err)
			}
		})
	}
	wg.Wait()
	page, err := store.ReadOperations(OperationsQuery{Limit: 200})
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if len(page.Records) != 20 {
		t.Fatalf("store holds %d records, want 20", len(page.Records))
	}
}
