package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// FuzzLoadStore drives the operation store's decode and its boot reaping over
// arbitrary bytes served as the store file, against an in-memory filesystem
// (afero.NewMemMapFs) so the target never touches disk and replays
// deterministically. It is the package's fuzz surface for the store file's
// decode path, and the oracle is the pair of invariants this substrate exists
// for: a store the loader cannot prove is its own is never served, and the
// store it does accept survives the boot pass, one-shot, with every sequence
// value and every untouched record exactly where the spec puts them.
//
// Oracle, for every input:
//   - A refusal never comes with a store: exactly one of (store, error) is set.
//   - An accepted store accepts the boot pass; the pass moves exactly the
//     pending/running records, advances the durable sequence once per moved
//     record, and leaves every other record byte-identical.
//   - The pass is one-shot: a second call moves nothing and rewrites nothing.
//   - Everything the atomic writer lands is safely re-loadable: a reload reads
//     back the same sequence and the same records.
func FuzzLoadStore(f *testing.F) {
	seeds := []string{
		``,
		`{}`,
		`not json`,
		`{"version":2,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":[]}`,
		// A pending record and a complete one: the pass must move only the first.
		`{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
			`{"id":"00000000000000000002","clientOperationId":"client-h2","host":"h2","kind":"restart","state":"complete","generation":7,"incarnationId":"inc-2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:01Z","hostRemoved":false,"sequence":1}]}`,
		// A running record carrying its fencing epoch and an orphan-unverified
		// record carrying its boundary: the pass moves the first and must leave
		// the second alone (spec §7's one exception).
		`{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"running","generation":7,"incarnationId":"inc-1","fencingEpoch":{"bootId":"boot-1","opSeq":3},"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
			`{"id":"00000000000000000002","clientOperationId":"client-h2","host":"h2","kind":"restart","state":"orphan-unverified","generation":7,"incarnationId":"inc-2","orphanBoundary":[{"host":"h2","kind":"local-linux"}],"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:01Z","hostRemoved":false}]}`,
		// A record whose pinned pair is missing and one whose stamp is ahead of
		// the store's sequence: both must be refused, never served.
		`{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":0,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"failed","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false,"sequence":9}]}`,
		// A file missing a required top-level field, a file with a null record
		// list, and a file whose record set is not in the allocator's canonical
		// ascending id order: all schema-invalid, never an empty or reordered
		// store.
		`{"version":1,"records":[]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":0,"records":null}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":2,"records":[` +
			`{"id":"00000000000000000002","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
			`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`,
		`{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"1","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending","generation":7,"incarnationId":"inc-1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		fs := afero.NewMemMapFs()
		path := StorePath("/state")
		// Every input stands for a fresh process: drop the path's shared cell so
		// the open below reads this input's file rather than the previous
		// iteration's in-memory state.
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if err := fs.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := afero.WriteFile(fs, path, []byte(body), 0o600); err != nil {
			t.Fatalf("write the fuzzed store file: %v", err)
		}
		// Force the mode: the loader's owner-only rule must see the store's own
		// 0600 whatever the backing filesystem does with a create mode.
		if err := fs.Chmod(path, 0o600); err != nil {
			t.Fatalf("Chmod: %v", err)
		}

		store, err := openFS(fs, path, storeFaults{})
		if err != nil {
			if store != nil {
				t.Fatalf("a refused store came with a store handle: err=%v", err)
			}
			if !errors.Is(err, ErrStoreCorrupt) && !errors.Is(err, ErrStoreReadableBeyondOwner) {
				// An unclassifiable refusal would leave the quarantine path
				// (spec §4) unable to tell what it is looking at.
				t.Fatalf("refusal is not classified: %v", err)
			}
			return
		}
		if store == nil {
			t.Fatalf("an accepted store came back nil")
		}

		before := store.Records()
		sequenceBefore := store.Sequence()
		inFlight := 0
		for _, record := range before {
			if record.State.InFlight() {
				inFlight++
			}
		}

		moved, err := store.RecoverInterrupted()
		if err != nil {
			t.Fatalf("boot pass on an accepted store: %v\nstore: %s", err, body)
		}
		if moved != inFlight {
			t.Fatalf("boot pass moved %d records, want the %d in-flight ones\nstore: %s", moved, inFlight, body)
		}
		if got, want := store.Sequence(), sequenceBefore+uint64(inFlight); got != want {
			t.Fatalf("sequence = %d after the boot pass, want %d\nstore: %s", got, want, body)
		}

		after := store.Records()
		if len(after) != len(before) {
			t.Fatalf("boot pass changed the record count: %d -> %d\nstore: %s", len(before), len(after), body)
		}
		for i, record := range before {
			got := after[i]
			switch {
			case record.State.InFlight():
				if got.State != StateInterrupted {
					t.Fatalf("boot pass left record %q in %q, want %q\nstore: %s", record.ID, got.State, StateInterrupted, body)
				}
				if got.Result == nil || got.Result.OK || got.Result.Message == "" {
					t.Fatalf("boot pass left record %q without a crash note: %+v\nstore: %s", record.ID, got.Result, body)
				}
				if got.Sequence <= sequenceBefore || got.Sequence > store.Sequence() {
					t.Fatalf("boot pass stamped record %q with %d, want a value in (%d, %d]\nstore: %s",
						record.ID, got.Sequence, sequenceBefore, store.Sequence(), body)
				}
			default:
				if !sameRecord(t, record, got) {
					t.Fatalf("boot pass changed the non-in-flight record %q:\nbefore: %s\nafter:  %s",
						record.ID, marshalRecords(t, []Record{record}), marshalRecords(t, []Record{got}))
				}
			}
		}

		if again, err := store.RecoverInterrupted(); err != nil || again != 0 {
			t.Fatalf("second boot pass: moved=%d err=%v, want 0 and no error\nstore: %s", again, err, body)
		}
		if got := store.Sequence(); got != sequenceBefore+uint64(inFlight) {
			t.Fatalf("second boot pass moved the sequence to %d\nstore: %s", got, body)
		}

		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore before the reload: %v", err)
		}
		reloaded, err := openFS(fs, path, storeFaults{})
		if err != nil {
			t.Fatalf("reload after the boot pass: %v\nstore: %s", err, body)
		}
		if reloaded.Sequence() != store.Sequence() {
			t.Fatalf("reloaded sequence = %d, want %d\nstore: %s", reloaded.Sequence(), store.Sequence(), body)
		}
		if got, want := marshalRecords(t, reloaded.Records()), marshalRecords(t, store.Records()); !bytes.Equal(got, want) {
			t.Fatalf("reload read back different records:\nreloaded: %s\nin memory: %s\nstore: %s", got, want, body)
		}
	})
}

// sameRecord compares the durable content of two records. It compares their
// rendered form rather than the structs, because an in-memory time.Time keeps a
// monotonic reading and a location pointer that a parsed one does not, and those
// would make equal records look different.
func sameRecord(t *testing.T, a, b Record) bool {
	t.Helper()
	return bytes.Equal(marshalRecords(t, []Record{a}), marshalRecords(t, []Record{b}))
}

// marshalRecords renders a record list the way the store writes it, with
// timestamps normalized to UTC.
func marshalRecords(t *testing.T, records []Record) []byte {
	t.Helper()
	normalized := make([]Record, len(records))
	for i, record := range records {
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		normalized[i] = record
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal records: %v", err)
	}
	return data
}
