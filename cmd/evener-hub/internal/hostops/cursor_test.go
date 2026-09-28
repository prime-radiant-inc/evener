package hostops

// Operations pagination and cursor tests (deploy pipeline 08b §8, §10; §12's
// "Pinned pagination", "Cursor-invalidated", "Operations incarnation scope",
// and "Protocol shapes" rows). The store-level half: the versioned base64url
// envelope, id-ascending sort and resume, the per-host bounds map (triple or
// the literal "absent"), every stale-entry arm this slice can reach, the
// limit's default and cap, and the 8 KiB encoded cap. Compaction
// (cursor-invalidated) belongs to S6 and is not built here.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// cursorTestRecord persists one pending deploy record pinned to the pair.
func cursorTestRecord(t *testing.T, store *Store, host, clientOperationID string, generation uint64, incarnationID string) Record {
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

// mirrorCursorBoundary writes one host's boundary triple the way the
// registry's hub.toml write does.
func mirrorCursorBoundary(t *testing.T, store *Store, host string, generation uint64, incarnationID string, presenceEpoch uint64) {
	t.Helper()
	if err := store.MirrorBoundaries(map[string]Boundary{
		host: {Generation: generation, IncarnationID: incarnationID, PresenceEpoch: presenceEpoch},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries(%s): %v", host, err)
	}
}

// cursorFields decodes a cursor's base64url envelope into its raw JSON fields.
func cursorFields(t *testing.T, cursor string) map[string]json.RawMessage {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor %q is not base64url: %v", cursor, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("cursor payload %s is not JSON: %v", raw, err)
	}
	return fields
}

// rawCursor encodes a hand-written JSON body as a cursor, so a test can present
// an envelope the codec itself would never mint.
func rawCursor(body string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(body))
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

// wantCursorStale fails unless err is §8's typed stale-entry re-list refusal.
func wantCursorStale(t *testing.T, err error) {
	t.Helper()
	var stale *CursorStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %T (%v), want a *CursorStaleError", err, err)
	}
	if stale.Binding != StaleBindingGeneration {
		t.Fatalf("refusal binding = %q, want %q", stale.Binding, StaleBindingGeneration)
	}
}

// TestCursorEnvelopeCarriesTheV2LiteralKeys pins §8's envelope literal: the
// cursor is a base64url JSON object with exactly {v: 2, pos, compactSeq,
// bounds, quarantineEpoch}, bounds entries are [generation, incarnationId,
// presenceEpoch] arrays or the literal "absent", and the codec round-trips it.
func TestCursorEnvelopeCarriesTheV2LiteralKeys(t *testing.T) {
	pos := formatAllocatorID(7)
	envelope := CursorEnvelope{
		Version:         2,
		Position:        pos,
		CompactSeq:      11,
		QuarantineEpoch: 3,
		Window:          CursorWindowHost,
		Bounds: map[string]CursorBound{
			"m4": {Boundary: Boundary{Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 5}},
			"m5": {Absent: true},
		},
	}
	cursor, err := EncodeCursor(envelope)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor %q is not base64url: %v", cursor, err)
	}
	if strings.ContainsAny(cursor, "+/=") {
		t.Fatalf("cursor %q is not the URL-safe alphabet without padding", cursor)
	}
	want := fmt.Sprintf(`{"v":2,"pos":%q,"compactSeq":11,"bounds":{"m4":[2,"inc-m4",5],"m5":"absent"},"window":"host","quarantineEpoch":3}`, pos)
	if string(raw) != want {
		t.Fatalf("cursor payload =\n%s\nwant\n%s", raw, want)
	}
	decoded, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if decoded.Version != 2 || decoded.Position != pos || decoded.CompactSeq != 11 || decoded.QuarantineEpoch != 3 {
		t.Fatalf("decoded envelope = %+v, want the minted values", decoded)
	}
	if decoded.Window != CursorWindowHost {
		t.Fatalf("decoded window = %q, want %q", decoded.Window, CursorWindowHost)
	}
	if decoded.Bounds["m4"] != envelope.Bounds["m4"] || !decoded.Bounds["m5"].Absent {
		t.Fatalf("decoded bounds = %+v, want the minted bounds", decoded.Bounds)
	}
	unfiltered := envelope
	unfiltered.Window = CursorWindowAll
	cursor, err = EncodeCursor(unfiltered)
	if err != nil {
		t.Fatalf("EncodeCursor(all): %v", err)
	}
	if fields := cursorFields(t, cursor); string(fields["window"]) != `"all"` {
		t.Fatalf("cursor window field = %s, want \"all\"", fields["window"])
	}
}

// TestCursorRefusesAnEnvelopeThatIsNotV2 pins §8: "A cursor whose envelope
// version is not 2 is a typed stale-entry re-list refusal, never a best-effort
// decode."
func TestCursorRefusesAnEnvelopeThatIsNotV2(t *testing.T) {
	// A v1 cursor predates the window key: the version check must come first, so
	// it reads as stale rather than as a malformed current envelope.
	_, err := DecodeCursor(rawCursor(`{"v":1,"pos":"00000000000000000007","compactSeq":0,"bounds":{},"quarantineEpoch":0}`))
	wantCursorStale(t, err)
	if _, err := DecodeCursor("not-base64!!"); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("garbage cursor error = %v, want ErrInvalidOperationsQuery", err)
	}
	if _, err := DecodeCursor(rawCursor(`{"v":2,"pos":"nope","compactSeq":0,"bounds":{},"window":"all","quarantineEpoch":0}`)); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("cursor with a bogus pos error = %v, want ErrInvalidOperationsQuery", err)
	}
}

// TestOperationsReadPagesByIDAscendingAcrossTerminalWrites pins §8's ordering
// and §12's resume rule: records sort and resume by the controller-assigned id
// ascending — createdAt is display-only, so a wall-clock rollback that stamps a
// later record earlier never moves it before the cursor — and a terminal
// transition landing between pages does not disturb the continuation.
func TestOperationsReadPagesByIDAscendingAcrossTerminalWrites(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	first := cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	second := cursorTestRecord(t, store, "m4", "op-2", 2, "inc-m4")
	third := cursorTestRecord(t, store, "m4", "op-3", 2, "inc-m4")
	// The rollback: the newest record's display-only stamp lands before the
	// oldest's. Sort and resume must still follow the ids.
	setRecordCreatedAt(t, store, third.ID, first.CreatedAt.Add(-time.Hour))
	if first.ID >= second.ID || second.ID >= third.ID {
		t.Fatalf("ids are not ascending: %q %q %q", first.ID, second.ID, third.ID)
	}

	page1, err := store.ReadOperations(OperationsQuery{Limit: 2})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page1.Records) != 2 || page1.Records[0].ID != first.ID || page1.Records[1].ID != second.ID {
		t.Fatalf("page 1 ids = %v, want %q, %q", recordIDs(page1.Records), first.ID, second.ID)
	}
	if page1.NextCursor == "" {
		t.Fatal("page 1 carried no nextCursor though a third record is behind it")
	}

	// A terminal write lands between pages, past the cursor's position. The
	// continuation must resume after the position and see the record's new
	// state, never repeat or skip.
	if _, err := store.TransitionToState(third.ID, StateComplete, &Result{OK: true, Message: "done"}, ""); err != nil {
		t.Fatalf("TransitionToState: %v", err)
	}
	page2, err := store.ReadOperations(OperationsQuery{Limit: 2, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(page 2): %v", err)
	}
	if len(page2.Records) != 1 || page2.Records[0].ID != third.ID {
		t.Fatalf("page 2 ids = %v, want %q", recordIDs(page2.Records), third.ID)
	}
	if page2.Records[0].State != StateComplete {
		t.Fatalf("page 2 replayed %q in state %q, want the terminal state", third.ID, page2.Records[0].State)
	}
	page3, err := store.ReadOperations(OperationsQuery{Limit: 2, Cursor: page2.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(page 3): %v", err)
	}
	if len(page3.Records) != 0 || page3.NextCursor != "" {
		t.Fatalf("page 3 = %d records, cursor %q; want the exhausted store", len(page3.Records), page3.NextCursor)
	}
}

// recordIDs is the ids of a page's records, for failure messages.
func recordIDs(records []Record) []string {
	ids := make([]string, len(records))
	for i, record := range records {
		ids[i] = record.ID
	}
	return ids
}

// TestOperationsReadLimitDefaultsAndCaps pins §8's "limit defaults to 50 and
// caps at 200. Responses never exceed the cap."
func TestOperationsReadLimitDefaultsAndCaps(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	for i := range 205 {
		cursorTestRecord(t, store, "m4", fmt.Sprintf("op-%03d", i), 2, "inc-m4")
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

// TestOperationsReadPinsEveryHostInTheQueryAtMint pins §8's bounds map and
// §10's hostBoundaries rule: an unfiltered cross-host page omits the top-level
// pair and pins one boundary per host the store knows — the current triple for
// a host that holds records, the literal "absent" for a host that holds none —
// and hosts created after the mint are never added to a continuation.
func TestOperationsReadPinsEveryHostInTheQueryAtMint(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-m4-1", 2, "inc-m4")
	mirrorCursorBoundary(t, store, "m5", 4, "inc-m5", 1) // boundary only: no records
	mirrorCursorBoundary(t, store, "m6", 1, "inc-m6", 2)
	cursorTestRecord(t, store, "m6", "op-m6-1", 1, "inc-m6")

	page, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if page.Generation != nil {
		t.Fatalf("unfiltered page echoed generation %d, want no top-level pair", *page.Generation)
	}
	if len(page.HostBoundaries) != 3 {
		t.Fatalf("hostBoundaries = %+v, want one entry per host in the query", page.HostBoundaries)
	}
	if bound := page.HostBoundaries["m4"]; bound.Absent || bound.Boundary != (Boundary{Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3}) {
		t.Fatalf("hostBoundaries[m4] = %+v, want its current triple", bound)
	}
	if bound := page.HostBoundaries["m5"]; !bound.Absent {
		t.Fatalf("hostBoundaries[m5] = %+v, want the literal absent marker", bound)
	}
	if bound := page.HostBoundaries["m6"]; bound.Absent || bound.Boundary.Generation != 1 {
		t.Fatalf("hostBoundaries[m6] = %+v, want its current triple", bound)
	}
	if page.NextCursor == "" {
		t.Fatal("page carried no nextCursor")
	}
	fields := cursorFields(t, page.NextCursor)
	if got := string(fields["v"]); got != "2" {
		t.Fatalf("cursor v = %s, want 2", got)
	}
	if got := string(fields["window"]); got != `"all"` {
		t.Fatalf("cursor window = %s, want \"all\" for an unfiltered cursor", got)
	}
	var bounds map[string]json.RawMessage
	if err := json.Unmarshal(fields["bounds"], &bounds); err != nil {
		t.Fatalf("cursor bounds: %v", err)
	}
	if string(bounds["m5"]) != `"absent"` {
		t.Fatalf("cursor bounds[m5] = %s, want the literal \"absent\"", bounds["m5"])
	}
	if string(bounds["m4"]) != `[2,"inc-m4",3]` {
		t.Fatalf("cursor bounds[m4] = %s, want [2,\"inc-m4\",3]", bounds["m4"])
	}

	// A host created after the mint has no stored triple: later pages skip its
	// records, and the minted window never grows to include it.
	mirrorCursorBoundary(t, store, "m9", 5, "inc-m9", 1)
	cursorTestRecord(t, store, "m9", "op-m9-1", 5, "inc-m9")
	continued, err := store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(continuation): %v", err)
	}
	if len(continued.Records) != 0 {
		t.Fatalf("continuation listed %v, want the newer host's records skipped", recordIDs(continued.Records))
	}
	// A fresh unpinned read does serve the newer host's records.
	fresh, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations(fresh): %v", err)
	}
	found := false
	for _, record := range fresh.Records {
		if record.Host == "m9" {
			found = true
		}
	}
	if !found {
		t.Fatal("the fresh read did not list the newer host's record")
	}
}

// TestOperationsReadHostPinnedPageEchoesThePair pins §8/§10's host-pinned
// shape: generation/incarnationId present exactly on host-pinned pages, no
// hostBoundaries there, and the effective pair is the one actually listed —
// the request's pair when it names one (superseded pairs included), the
// current pair otherwise.
func TestOperationsReadHostPinnedPageEchoesThePair(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")
	old := cursorTestRecord(t, store, "m4", "op-old", 1, "inc-old")
	// A second host's record must never leak into a host-pinned page.
	mirrorCursorBoundary(t, store, "m9", 5, "inc-m9", 1)
	cursorTestRecord(t, store, "m9", "op-m9", 5, "inc-m9")

	page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
	if err != nil {
		t.Fatalf("ReadOperations(m4): %v", err)
	}
	if page.Generation == nil || *page.Generation != 2 || page.IncarnationID != "inc-m4" {
		t.Fatalf("page pair = %v/%q, want the current 2/inc-m4", page.Generation, page.IncarnationID)
	}
	if page.HostBoundaries != nil {
		t.Fatalf("host-pinned page carried hostBoundaries %+v, want none", page.HostBoundaries)
	}
	for _, record := range page.Records {
		if record.Host != "m4" {
			t.Fatalf("host-pinned page leaked host %q", record.Host)
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
	colliding := cursorTestRecord(t, store, "m4", "op-colliding", 1, "inc-other")
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
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	first := cursorTestRecord(t, store, "m4", "op-alpha", 2, "inc-m4")
	second := cursorTestRecord(t, store, "m4", "op-beta", 2, "inc-m4")
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
// host-pinned lookup that names no pair echoes the row it returned — first page
// and continuation alike — never the host's current pair.
func TestOperationsReadPinnedDetailEchoesTheListedRecordPair(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	old1 := cursorTestRecord(t, store, "m4", "op-old-1", 1, "inc-old")
	old2 := cursorTestRecord(t, store, "m4", "op-old-2", 1, "inc-old")
	cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", ID: old1.ID, Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(detail): %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != old1.ID {
		t.Fatalf("detail page ids = %v, want %q", recordIDs(page.Records), old1.ID)
	}
	if page.Generation == nil || *page.Generation != 1 || page.IncarnationID != "inc-old" {
		t.Fatalf("detail page pair = %v/%q, want the listed row's 1/inc-old", page.Generation, page.IncarnationID)
	}
	if page.NextCursor == "" {
		t.Fatal("detail page carried no nextCursor")
	}
	// The continuation recovers the same listed pair from the row it resumes
	// after, so the top-level pair does not flip between pages.
	continued, err := store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(continuation): %v", err)
	}
	if len(continued.Records) != 1 || continued.Records[0].ID != old2.ID {
		t.Fatalf("continuation ids = %v, want %q", recordIDs(continued.Records), old2.ID)
	}
	if continued.Generation == nil || *continued.Generation != 1 || continued.IncarnationID != "inc-old" {
		t.Fatalf("continuation pair = %v/%q, want the same listed 1/inc-old", continued.Generation, continued.IncarnationID)
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
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-old-a", 1, "inc-old-a")
	cursorTestRecord(t, store, "m4", "op-old-b", 1, "inc-old-b")
	current := cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

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

// TestOperationsReadRefusesANarrowedUnfilteredCursor pins the other direction
// of §8's window pinning: a cursor minted over a cross-host window cannot be
// continued host-pinned, because that would truncate the authoritative map and
// silently drop every other host's records from the pagination.
func TestOperationsReadRefusesANarrowedUnfilteredCursor(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-m4", 2, "inc-m4")
	mirrorCursorBoundary(t, store, "m9", 5, "inc-m9", 1)
	cursorTestRecord(t, store, "m9", "op-m9", 5, "inc-m9")

	page, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor to continue with")
	}
	if len(page.HostBoundaries) != 2 {
		t.Fatalf("first page hostBoundaries = %+v, want both hosts", page.HostBoundaries)
	}
	_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
	wantCursorStale(t, err)
}

// TestOperationsReadPinnedContinuationKeepsTheListedPair pins the High fix: a
// cursor minted for a superseded pair keeps listing THAT pair when the
// continuation repeats it or omits it, and never falls back to the host's
// current pair (§8: "An omitted filter on a later page reads as the
// pinned-cursor window, never as a fresh unpinned query"; §12: "A cursor minted
// under one pair never lists the other").
func TestOperationsReadPinnedContinuationKeepsTheListedPair(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	old1 := cursorTestRecord(t, store, "m4", "op-old-1", 1, "inc-old")
	old2 := cursorTestRecord(t, store, "m4", "op-old-2", 1, "inc-old")
	current := cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")
	generation := uint64(1)

	page1, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-old", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations(page 1): %v", err)
	}
	if len(page1.Records) != 1 || page1.Records[0].ID != old1.ID {
		t.Fatalf("page 1 ids = %v, want %q", recordIDs(page1.Records), old1.ID)
	}
	if page1.Generation == nil || *page1.Generation != 1 || page1.IncarnationID != "inc-old" {
		t.Fatalf("page 1 pair = %v/%q, want the listed 1/inc-old", page1.Generation, page1.IncarnationID)
	}
	if page1.NextCursor == "" {
		t.Fatal("page 1 carried no nextCursor")
	}

	// An omitted pair reads as the pinned window: the remaining gen-1 row,
	// never the host's current gen-2 record.
	page2, err := store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(omitted pair): %v", err)
	}
	if len(page2.Records) != 1 || page2.Records[0].ID != old2.ID {
		t.Fatalf("omitted-pair continuation ids = %v, want only the pinned pair's next row %q",
			recordIDs(page2.Records), old2.ID)
	}
	for _, record := range page2.Records {
		if record.ID == current.ID {
			t.Fatalf("the continuation listed the current pair's record %q", current.ID)
		}
	}
	if page2.Generation == nil || *page2.Generation != 1 || page2.IncarnationID != "inc-old" {
		t.Fatalf("continuation pair = %v/%q, want the pinned 1/inc-old", page2.Generation, page2.IncarnationID)
	}
	// The explicitly repeated pair continues the same window.
	page3, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-old", Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(repeated pair): %v", err)
	}
	if len(page3.Records) != 1 || page3.Records[0].ID != old2.ID {
		t.Fatalf("repeated-pair continuation ids = %v, want only %q", recordIDs(page3.Records), old2.ID)
	}
	// The pinned window is exhausted; the current pair appears only on a fresh
	// unpinned read.
	page4, err := store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page2.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(exhausted): %v", err)
	}
	if len(page4.Records) != 0 {
		t.Fatalf("exhausted window listed %v", recordIDs(page4.Records))
	}
	fresh, err := store.ReadOperations(OperationsQuery{Host: "m4"})
	if err != nil {
		t.Fatalf("ReadOperations(fresh): %v", err)
	}
	if len(fresh.Records) != 1 || fresh.Records[0].ID != current.ID {
		t.Fatalf("fresh pinned read ids = %v, want the current pair's %q", recordIDs(fresh.Records), current.ID)
	}
}

// TestOperationsReadRefusesAPinnedCursorWithoutItsName pins the window key's
// first direction: a cursor minted host-pinned (window "host") cannot be served
// as an unfiltered query — §8's "a cursor minted for one pair validated against
// an unfiltered query is a typed stale-entry re-list refusal" — not even when
// the pinned pair is the host's current one.
func TestOperationsReadRefusesAPinnedCursorWithoutItsName(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	cursorTestRecord(t, store, "m4", "op-2", 2, "inc-m4")

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor to continue with")
	}
	if fields := cursorFields(t, page.NextCursor); string(fields["window"]) != `"host"` {
		t.Fatalf("pinned cursor window = %s, want \"host\"", fields["window"])
	}
	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	wantCursorStale(t, err)
}

// TestOperationsReadRefusesAnUnnamedHistoricalContinuation pins the Low fix: a
// cursor minted for one pair presented as an unfiltered query is §8's typed
// stale-entry re-list refusal — it never becomes a page of the pinned host's
// other pairs.
func TestOperationsReadRefusesAnUnnamedHistoricalContinuation(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-old-1", 1, "inc-old")
	cursorTestRecord(t, store, "m4", "op-old-2", 1, "inc-old")
	cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")
	generation := uint64(1)

	page, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-old", Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor to continue with")
	}
	_, err = store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	wantCursorStale(t, err)
}

// TestOperationsReadUnfilteredPagesTheCurrentWindow pins §10's "generation ...
// omitted: the current generation" for unfiltered pages: each host contributes
// its current pair's records, and history stays reachable through the
// host-pinned pair filter and the id detail filter — never through a later page
// of an unfiltered cursor.
func TestOperationsReadUnfilteredPagesTheCurrentWindow(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	old := cursorTestRecord(t, store, "m4", "op-old", 1, "inc-old")
	current := cursorTestRecord(t, store, "m4", "op-current", 2, "inc-m4")

	page, err := store.ReadOperations(OperationsQuery{})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != current.ID {
		t.Fatalf("unfiltered page ids = %v, want only the current pair's %q", recordIDs(page.Records), current.ID)
	}
	if page.NextCursor == "" {
		t.Fatal("page carried no nextCursor")
	}
	continued, err := store.ReadOperations(OperationsQuery{Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("ReadOperations(continuation): %v", err)
	}
	if len(continued.Records) != 0 {
		t.Fatalf("unfiltered continuation listed %v, want the superseded pair never admitted", recordIDs(continued.Records))
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

// TestDecodeCursorRefusesMalformedEnvelopes pins §8's "never a best-effort
// decode": an over-cap value, a missing key, an unknown key, a key named twice,
// and a malformed host key are each ErrInvalidOperationsQuery, never a silently
// zero-filled envelope.
func TestDecodeCursorRefusesMalformedEnvelopes(t *testing.T) {
	pos := formatAllocatorID(1)
	cases := map[string]string{
		"over the encoded cap": strings.Repeat("A", MaxCursorBytes+1),
		"a missing key":        rawCursor(fmt.Sprintf(`{"v":2,"pos":%q,"bounds":{},"window":"all","quarantineEpoch":0}`, pos)),
		"a missing window":     rawCursor(fmt.Sprintf(`{"v":2,"pos":%q,"compactSeq":0,"bounds":{},"quarantineEpoch":0}`, pos)),
		"an unknown window":    rawCursor(fmt.Sprintf(`{"v":2,"pos":%q,"compactSeq":0,"bounds":{},"window":"bogus","quarantineEpoch":0}`, pos)),
		"an unknown key":       rawCursor(fmt.Sprintf(`{"v":2,"pos":%q,"compactSeq":0,"bounds":{},"window":"all","quarantineEpoch":0,"extra":1}`, pos)),
		"a key named twice":    rawCursor(fmt.Sprintf(`{"v":2,"v":2,"pos":%q,"compactSeq":0,"bounds":{},"window":"all","quarantineEpoch":0}`, pos)),
		"an empty host key":    rawCursor(fmt.Sprintf(`{"v":2,"pos":%q,"compactSeq":0,"bounds":{"":"absent"},"window":"all","quarantineEpoch":0}`, pos)),
	}
	for name, cursor := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCursor(cursor); !errors.Is(err, ErrInvalidOperationsQuery) {
				t.Fatalf("DecodeCursor(%s) = %v, want ErrInvalidOperationsQuery", name, err)
			}
		})
	}
	// A structurally valid envelope that is still over the cap, not just a long
	// string of the wrong alphabet.
	hosts := make(map[string]any, 400)
	for i := range 400 {
		hosts[fmt.Sprintf("host-%03d", i)] = "absent"
	}
	body, err := json.Marshal(map[string]any{"v": 2, "pos": pos, "compactSeq": 0, "bounds": hosts, "window": "all", "quarantineEpoch": 0})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if _, err := DecodeCursor(rawCursor(string(body))); !errors.Is(err, ErrInvalidOperationsQuery) {
		t.Fatalf("DecodeCursor(valid over-cap envelope) = %v, want ErrInvalidOperationsQuery", err)
	}
}

// TestOperationsReadRefusesForgedBoundsEntries pins the Medium's third arm: a
// bounds entry for a host the store has never seen — neither records nor a
// mirrored boundary — is not accepted as an authoritative "absent" (or as a
// fabricated triple), and a position naming no stored row is refused too.
func TestOperationsReadRefusesForgedBoundsEntries(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")

	for name, tc := range map[string]struct {
		envelope CursorEnvelope
		host     string
	}{
		"an absent host the store never saw": {
			envelope: CursorEnvelope{
				Version:  2,
				Position: formatAllocatorID(1),
				Bounds:   map[string]CursorBound{"ghost": {Absent: true}},
				Window:   CursorWindowAll,
			},
		},
		"a fabricated triple for an unknown host": {
			envelope: CursorEnvelope{
				Version:  2,
				Position: formatAllocatorID(1),
				Bounds: map[string]CursorBound{"ghost": {Boundary: Boundary{
					Generation: 9, IncarnationID: "inc-ghost", PresenceEpoch: 9,
				}}},
				Window: CursorWindowAll,
			},
		},
		"a position naming no stored row": {
			envelope: CursorEnvelope{
				Version:  2,
				Position: formatAllocatorID(999),
				Bounds:   map[string]CursorBound{"m4": {Boundary: Boundary{Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3}}},
				Window:   CursorWindowHost,
			},
			host: "m4",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cursor, err := EncodeCursor(tc.envelope)
			if err != nil {
				t.Fatalf("EncodeCursor: %v", err)
			}
			_, err = store.ReadOperations(OperationsQuery{Host: tc.host, Cursor: cursor})
			wantCursorStale(t, err)
		})
	}
}

// TestOperationsReadRefusesAStaleCursor pins every §8 continuation refusal this
// slice can reach, each a typed stale-entry re-list: a pinned quarantine epoch
// that moved, a generation advance, a presence advance, a request pair the
// cursor did not pin, a pair filter with no host name, and a named host outside
// the cursor's pinned window.
func TestOperationsReadRefusesAStaleCursor(t *testing.T) {
	seed := func(t *testing.T) (*Store, Record) {
		t.Helper()
		store, _ := openTestStore(t)
		mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
		first := cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
		_ = cursorTestRecord(t, store, "m4", "op-2", 2, "inc-m4")
		return store, first
	}

	t.Run("quarantine epoch advanced", func(t *testing.T) {
		store, _ := seed(t)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		decoded, err := DecodeCursor(page.NextCursor)
		if err != nil {
			t.Fatalf("DecodeCursor: %v", err)
		}
		decoded.QuarantineEpoch++
		advanced, err := EncodeCursor(decoded)
		if err != nil {
			t.Fatalf("EncodeCursor: %v", err)
		}
		_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: advanced})
		wantCursorStale(t, err)
	})

	t.Run("generation advanced", func(t *testing.T) {
		store, _ := seed(t)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		mirrorCursorBoundary(t, store, "m4", 3, "inc-m4b", 3)
		_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
		wantCursorStale(t, err)
	})

	t.Run("presence advanced", func(t *testing.T) {
		store, _ := seed(t)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		// A removal preserves generation and incarnation but advances presence.
		mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 4)
		_, err = store.ReadOperations(OperationsQuery{Host: "m4", Cursor: page.NextCursor})
		wantCursorStale(t, err)
	})

	t.Run("pair the cursor did not pin", func(t *testing.T) {
		store, _ := seed(t)
		generation := uint64(2)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		other := uint64(1)
		_, err = store.ReadOperations(OperationsQuery{Host: "m4", Generation: &other, IncarnationID: "inc-other", Cursor: page.NextCursor})
		wantCursorStale(t, err)
	})

	t.Run("pair filter without a host name", func(t *testing.T) {
		store, _ := seed(t)
		generation := uint64(2)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4", Generation: &generation, IncarnationID: "inc-m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		_, err = store.ReadOperations(OperationsQuery{Generation: &generation, IncarnationID: "inc-m4", Cursor: page.NextCursor})
		wantCursorStale(t, err)
	})

	t.Run("host outside the pinned window", func(t *testing.T) {
		store, _ := seed(t)
		page, err := store.ReadOperations(OperationsQuery{Host: "m4"})
		if err != nil {
			t.Fatalf("ReadOperations: %v", err)
		}
		mirrorCursorBoundary(t, store, "m9", 5, "inc-m9", 1)
		cursorTestRecord(t, store, "m9", "op-m9", 5, "inc-m9")
		_, err = store.ReadOperations(OperationsQuery{Host: "m9", Cursor: page.NextCursor})
		wantCursorStale(t, err)
	})
}

// TestOperationsReadRefusesAnOverCapFirstPage pins §8's 8 KiB encoded cap: a
// first page whose bounds map would exceed it refuses the distinct
// cursor-too-large error carrying {capBytes: 8192} — never a truncated cursor.
func TestOperationsReadRefusesAnOverCapFirstPage(t *testing.T) {
	store, path := openTestStore(t)
	// 400 hosts: the absent entries alone push the encoded envelope past the
	// 8 KiB cap (each host contributes a key plus an "absent" marker, and the
	// base64url encoding inflates the JSON by a further third).
	boundaries := make(map[string]Boundary, 400)
	for i := range 400 {
		name := fmt.Sprintf("host-%03d", i)
		boundaries[name] = Boundary{Generation: 1, IncarnationID: "incarnation-" + name, PresenceEpoch: 1}
	}
	if err := store.MirrorBoundaries(boundaries, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	cursorTestRecord(t, store, "host-000", "op-1", 1, "incarnation-host-000")
	before := mustReadFile(t, path)

	_, err := store.ReadOperations(OperationsQuery{})
	var tooLarge *CursorTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %T (%v), want *CursorTooLargeError", err, err)
	}
	if tooLarge.CapBytes != MaxCursorBytes {
		t.Fatalf("capBytes = %d, want %d", tooLarge.CapBytes, MaxCursorBytes)
	}
	if after := mustReadFile(t, path); !bytes.Equal(before, after) {
		t.Fatal("a refused read rewrote the store file")
	}
	// The cap is on the encoded cursor itself: the same enabled state under the
	// cap mints normally (the over-cap case above needed hundreds of hosts; one
	// host fits).
	small, _ := openTestStore(t)
	mirrorCursorBoundary(t, small, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, small, "m4", "op-1", 2, "inc-m4")
	if _, err := small.ReadOperations(OperationsQuery{Host: "m4"}); err != nil {
		t.Fatalf("under-cap first page refused: %v", err)
	}
}

// TestOperationsReadIsReadOnlyOnTheStoreFile pins that a read — including a
// page mint followed by a continuation — never rewrites the store: cursors are
// client-held wire values, and the store's records are only read.
func TestOperationsReadIsReadOnlyOnTheStoreFile(t *testing.T) {
	store, path := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
	cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	cursorTestRecord(t, store, "m4", "op-2", 2, "inc-m4")
	before := mustReadFile(t, path)

	page, err := store.ReadOperations(OperationsQuery{Limit: 1})
	if err != nil {
		t.Fatalf("ReadOperations: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("no cursor to continue with")
	}
	if _, err := store.ReadOperations(OperationsQuery{Limit: 1, Cursor: page.NextCursor}); err != nil {
		t.Fatalf("ReadOperations(continuation): %v", err)
	}
	if after := mustReadFile(t, path); !bytes.Equal(before, after) {
		t.Fatal("reads rewrote the store file")
	}
}

// TestOperationsReadRefusesARecordHostWithNoMirroredBoundary pins the one
// state the store's own writers never produce: a host holding records but no
// boundary triple. A bounds entry needs a triple to validate against, so the
// read refuses rather than minting an entry later pages cannot check.
func TestOperationsReadRefusesARecordHostWithNoMirroredBoundary(t *testing.T) {
	store, _ := openTestStore(t)
	cursorTestRecord(t, store, "m4", "op-1", 2, "inc-m4")
	if _, err := store.ReadOperations(OperationsQuery{Host: "m4"}); !errors.Is(err, ErrMissingHostBoundary) {
		t.Fatalf("err = %v, want ErrMissingHostBoundary", err)
	}
	if _, err := store.ReadOperations(OperationsQuery{}); !errors.Is(err, ErrMissingHostBoundary) {
		t.Fatalf("unfiltered err = %v, want ErrMissingHostBoundary", err)
	}
}

// TestOperationsReadSerializesUnderTheStoreMutex pins §4's one store mutex: a
// page read racing writes observes complete records and never trips the race
// detector.
func TestOperationsReadSerializesUnderTheStoreMutex(t *testing.T) {
	store, _ := openTestStore(t)
	mirrorCursorBoundary(t, store, "m4", 2, "inc-m4", 3)
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
