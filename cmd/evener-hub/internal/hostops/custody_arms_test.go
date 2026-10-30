package hostops

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The corrupt-store arms the custody snapshot refuses: a corrupt file whose
// bytes yield no complete custody is an incomplete-custody refusal, never a
// quarantine that drops a name it could not prove. Each row plants one
// malformed region in an otherwise custodyable corrupt store (the sequence
// corruption the shared records already carry) and asserts the boot's refusal.
// The pendingCompensation family is the exception the raw walk must leave
// unjudged: the duplicate-key byte rule still applies, but a record's keys are
// not the walk's to spell-check, so a store carrying an armed record still
// quarantines (TestQuarantineCompletesWithArmedCompensations).

const (
	custodyArmRec1 = `{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
		`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}`
	custodyArmRec2 = `{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}`
	// custodyArmRec3 is the terminal record the missing-evidence rows leave as
	// the highest visible id, so the gap below it has no removal evidence.
	custodyArmRec3 = `{"id":"00000000000000000003","clientOperationId":"op-h3","host":"h3","kind":"deploy","state":"complete",` +
		`"generation":4,"incarnationId":"inc-h3","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}`
	// custodyArmTombstone is a schema-valid dedup tombstone the rows extend.
	custodyArmTombstone = `{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1,"result":{"ok":false,"message":"undone"}}`
)

// custodyArmStore assembles a corrupt store body: the state-transition sequence
// stays below the terminal stamp, so every row is quarantined, and extra carries
// the region under test.
func custodyArmStore(hwm uint64, records, extra string) string {
	body := `{"version":1,"sequence":1,"allocatorHighWaterMark":` + strconv.FormatUint(hwm, 10) + `,"records":[` + records + `]`
	if extra != "" {
		body += `,` + extra
	}
	return body + `}`
}

// custodyArmStoreAt is custodyArmStore with an explicit top-level
// state-transition sequence. A row whose only corruption is its compensation
// region uses sequence 2 — the terminal record's stamp — so the loader refuses
// it for that region alone; rows that need unrelated corruption keep
// custodyArmStore's sequence 1.
func custodyArmStoreAt(sequence, hwm uint64, records, extra string) string {
	return strings.Replace(custodyArmStore(hwm, records, extra), `"sequence":1`,
		`"sequence":`+strconv.FormatUint(sequence, 10), 1)
}

// validTombstoneWithID renders a schema-valid tombstone for the rows that need
// one at an id the row names.
func validTombstoneWithID(id string) string {
	return `{"id":"` + id + `","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1,"result":{"ok":true,"message":"done"}}`
}

func TestCustodySnapshotRefusesUnprovableCorruptStores(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
	}{
		{
			name: "a tombstone whose terminal result carries no outcome",
			body: custodyArmStore(2, custodyArmRec1,
				`"tombstones":[{`+
					`"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",`+
					`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",`+
					`"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1,"result":{"message":"no outcome"}}]`),
		},
		{
			name: "a boundary mirror outside the mirror schema",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2,
				`"boundaries":{"hX":{"generation":3,"incarnationId":"inc-x","extra":1}}`),
		},
		{
			name: "a removal marker outside the marker schema",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2,
				`"removedHosts":{"hX":{"removedAt":"2026-09-26T00:00:00Z","generation":3,"incarnationId":"inc-x","extra":1}}`),
		},
		{
			name: "a record whose id is not controller-assigned",
			body: custodyArmStore(2, strings.Replace(custodyArmRec1, `"00000000000000000001"`, `"not-an-id"`, 1)+`,`+custodyArmRec2, ""),
		},
		{
			name: "a tombstone sitting above the allocator high-water mark",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2,
				`"compactSeq":1,"tombstones":[`+validTombstoneWithID("00000000000000000003")+`]`),
		},
		{
			name: "a tombstone accounting for an id a retained record still carries",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2,
				`"compactSeq":1,"tombstones":[`+custodyArmTombstone+`]`),
		},
		{
			name: "a compaction mark carrying a seq above the store's compactSeq",
			body: custodyArmStore(3, custodyArmRec1+`,`+custodyArmRec2,
				`"compactSeq":1,"compactionMarks":[{"seq":5,"hosts":{"hB":"00000000000000000003"}}]`),
		},
		{
			name: "two compaction marks sharing one seq",
			body: custodyArmStore(3, custodyArmRec1+`,`+custodyArmRec2,
				`"compactSeq":2,"compactionMarks":[{"seq":1,"hosts":{"hB":"00000000000000000003"}},{"seq":1,"hosts":{"hC":"00000000000000000003"}}]`),
		},
		{
			name: "an id below the smallest compaction evidence with no removal proof",
			body: custodyArmStore(3, custodyArmRec1+`,`+custodyArmRec3,
				`"compactSeq":5,"compactionMarks":[{"seq":1,"hosts":{"hB":"00000000000000000003"}}]`),
		},
		{
			name: "a store carrying trailing bytes after the JSON document",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2, "") + ` garbage`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, tc.body)
			if err := loadAt(path); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want errors.Is(ErrStoreCorrupt)", err)
			}
		})
	}
}

// TestQuarantineCompletesWithArmedCompensations pins the corrected raw-walk
// contract for the pendingCompensation family: the custody byte-walk carries no
// owned-key table and does not spell-check the keys inside a raw field, so a
// corrupt store that also carries an armed (or loader-malformed) compensation
// record still completes the custody-first quarantine instead of wedging every
// boot. In the preimage-row row, that unjudged interior is what ownedKeysFor
// must report not-owned.
//
// Every row is a store the loader refuses. The first is the reported
// regression: a well-formed armed record while an unrelated corruption — the
// state-transition sequence sitting below the terminal record's stamp — makes
// the loader refuse the store. The others carry a sound sequence and only their
// compensation region makes the loader refuse them, so each pins one loader
// arm: the canonical spelling of the record and of a preimage row, and the
// record's own decode rules. Custody consumes the family as a raw region the
// replacement discards.
func TestQuarantineCompletesWithArmedCompensations(t *testing.T) {
	t.Parallel()

	value, err := newTokenValue()
	if err != nil {
		t.Fatalf("newTokenValue: %v", err)
	}
	row := tokenRowJSON("hB", value, tokenEpoch, tokenEpoch.Add(5*time.Minute), tokenEpoch)
	nonCanonicalRow := strings.Replace(row, `"host":"hB"`, `"Host":"hB"`, 1)

	cases := []struct {
		name string
		body string
	}{
		{
			name: "a well-formed armed record over an unrelated corrupt sequence",
			body: custodyArmStore(2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hB":{"host":"hB","phase":"compensating-armed","rows":[],"stashReference":"stash","generation":3}}`),
		},
		{
			name: "a record key outside the canonical spelling",
			body: custodyArmStoreAt(2, 2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hB":{"Host":"hB","phase":"compensating-armed","rows":[],"stashReference":"stash","generation":3}}`),
		},
		{
			name: "a preimage row key outside the canonical spelling",
			body: custodyArmStoreAt(2, 2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hB":{"host":"hB","phase":"compensating-armed","rows":[`+nonCanonicalRow+
					`],"stashReference":"stash","generation":3}}`),
		},
		{
			name: "a compensation record missing its phase",
			body: custodyArmStoreAt(2, 2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hB":{"host":"hB","rows":[],"stashReference":"stash","generation":3}}`),
		},
		{
			name: "a compensation record carrying a phase outside the machine",
			body: custodyArmStoreAt(2, 2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hB":{"host":"hB","phase":"nonsense","rows":[],"stashReference":"stash","generation":3}}`),
		},
		{
			name: "a compensation record keyed by a host it does not name",
			body: custodyArmStoreAt(2, 2, custodyArmRec1+`,`+custodyArmRec2,
				`"pendingCompensation":{"hZ":{"host":"hB","phase":"compensating-armed","rows":[],"stashReference":"stash","generation":3}}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, tc.body)
			store, err := Open(path)
			if err != nil {
				t.Fatalf("Open on a corrupt store carrying a compensation record: err = %v, want the custody-first quarantine", err)
			}
			signal := store.Quarantine()
			if signal == nil || signal.QuarantineEpoch != 1 || signal.QuarantinedFile != path {
				t.Fatalf("Quarantine() = %+v, want epoch 1 for %s", signal, path)
			}
			// §4 starts the replacement with no outstanding compensation, so the
			// family the corrupt file carried is discarded by design.
			if got := store.Compensations(); len(got) != 0 {
				t.Fatalf("the replacement store carries compensations %+v, want none", got)
			}
			// The corrupt bytes survive in the aside file: a quarantine that
			// lost them would drop the only evidence of what the file held.
			_, aside := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
			if string(aside) != tc.body {
				t.Fatalf("the aside file = %s, want the planted bytes %s", aside, tc.body)
			}
		})
	}
}

// TestStoreLoadNormalizesRemovalMarkers pins the loader's timestamp
// normalization for removal markers: a stored offset form survives the load
// only as its UTC form, with its instant preserved. The store is a valid one —
// the terminal record's sequence is covered — so the marker loads through the
// normal path instead of behind a quarantine.
func TestStoreLoadNormalizesRemovalMarkers(t *testing.T) {
	t.Parallel()
	path := StorePath(t.TempDir())
	body := strings.Replace(validArmStoreJSON, `"sequence":2}]}`,
		`"sequence":2}],"removedHosts":{"hX":{"removedAt":"2026-09-26T02:00:00+02:00","generation":3,"incarnationId":"inc-x"}}}`, 1)
	writeRawStore(t, path, 0o600, body)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open refused a valid store carrying a removal marker: %v", err)
	}
	marker, ok := store.RemovedHosts()["hX"]
	if !ok {
		t.Fatalf("the loaded store carries no removal marker for hX: %v", store.RemovedHosts())
	}
	want := time.Date(2026, time.September, 26, 0, 0, 0, 0, time.UTC)
	if !marker.RemovedAt.Equal(want) {
		t.Fatalf("removedAt = %v, want the stored instant %v preserved", marker.RemovedAt, want)
	}
	if marker.RemovedAt.Location() != time.UTC {
		t.Fatalf("removedAt = %v, want the stored offset form loaded as its UTC representation", marker.RemovedAt)
	}
}

// TestQuarantineSecondEpochKeepsALineageFenceUnderItsID pins the prior-import
// rule across quarantine epochs: the corrupt replacement of a first quarantine
// re-exports the fence the first custody file carries under the same id, so the
// second custody keeps the lineage's original id instead of refusing the reuse.
func TestQuarantineSecondEpochKeepsALineageFenceUnderItsID(t *testing.T) {
	t.Parallel()
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if signal := first.Quarantine(); signal == nil || signal.QuarantineEpoch != 1 {
		t.Fatalf("the first quarantine's signal = %+v, want epoch 1", signal)
	}

	// The replacement serves the same fence record; a second corrupt file
	// carrying that record quarantines again, and the fence the first custody
	// file carries is the same lineage, so it keeps its id.
	second := `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
		custodyArmRec1 + `,` + custodyArmRec2 + `]}`
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatalf("plant the second corrupt store: %v", err)
	}
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("second Open over the prior custody's fence: %v", err)
	}
	signal := reopened.Quarantine()
	if signal == nil || signal.QuarantineEpoch != 2 {
		t.Fatalf("the second quarantine's signal = %+v, want epoch 2", signal)
	}
}

// TestQuarantineSecondEpochRefusesAnAliasedFenceID pins the refusal half (the fence is not imported; the file is set aside unproven) of the
// prior-import rule: a second corrupt store whose fence carries an id an
// earlier custody file already handed to a different record identity is
// refused, so an id-only resolve can never alias an unrelated record.
func TestQuarantineSecondEpochRefusesAnAliasedFenceID(t *testing.T) {
	t.Parallel()
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	if _, err := Open(path); err != nil {
		t.Fatalf("first Open: %v", err)
	}

	// The second corrupt file carries the same record id under a different
	// host: the earlier custody's id now names another record identity.
	aliased := strings.Replace(custodyArmRec1, `"host":"h1"`, `"host":"hZ"`, 1)
	if err := os.WriteFile(path, []byte(`{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[`+
		aliased+`,`+custodyArmRec2+`]}`), 0o600); err != nil {
		t.Fatalf("plant the aliased corrupt store: %v", err)
	}
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open error = %v, want the unproven set-aside", err)
	}
	if signal := store.Quarantine(); signal == nil || !signal.Unproven || signal.QuarantineEpoch != 2 {
		t.Fatalf("Quarantine() = %+v, want an unproven signal at epoch 2", signal)
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("the replacement imported an aliasing fence id: %+v", records)
	}
}
