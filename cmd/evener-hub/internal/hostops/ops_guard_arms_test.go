package hostops

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// The store's public-guard and validation arms: the caller-supplied halves the
// API refuses before any state is touched, the nil-handle guards a hub without
// its operation store answers with, and the persisted-value validators the
// loader enforces over hand-edited files. Each row asserts the refusal (or the
// zero answer) and its sentinel, never message copy.

// validArmStore is a loadable store: one open fence record and one terminal
// record whose terminal stamp the store's sequence covers.
const validArmStoreJSON = `{"version":1,"sequence":2,"allocatorHighWaterMark":2,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`

// armPair is a complete registry pair for the request fixtures.
var armPair = OperationPair{Generation: 3, IncarnationID: "inc-h1"}

func TestLookupOperationRejectsMalformedQueries(t *testing.T) {
	t.Parallel()
	store, err := Open(StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cases := []struct {
		name  string
		query OperationDedupQuery
	}{
		{
			name:  "no client operation id",
			query: OperationDedupQuery{Host: "h1", Kind: KindRestart, Current: armPair},
		},
		{
			name: "a client operation id over the byte bound",
			query: OperationDedupQuery{
				ClientOperationID: strings.Repeat("x", MaxClientOperationIDBytes+1),
				Host:              "h1", Kind: KindRestart, Current: armPair,
			},
		},
		{
			name: "a client operation id that is not valid UTF-8",
			query: OperationDedupQuery{
				ClientOperationID: "op-\xff",
				Host:              "h1", Kind: KindRestart, Current: armPair,
			},
		},
		{
			name:  "no host name",
			query: OperationDedupQuery{ClientOperationID: "op", Kind: KindRestart, Current: armPair},
		},
		{
			name:  "a kind outside the operation set",
			query: OperationDedupQuery{ClientOperationID: "op", Host: "h1", Kind: Kind("nonsense"), Current: armPair},
		},
		{
			name:  "an incomplete registry pair",
			query: OperationDedupQuery{ClientOperationID: "op", Host: "h1", Kind: KindRestart, Current: OperationPair{Generation: 3}},
		},
		{
			name: "an incomplete intended pair",
			query: OperationDedupQuery{
				ClientOperationID: "op", Host: "h1", Kind: KindRestart, Current: armPair,
				Intended: &OperationPair{Generation: 2},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := store.LookupOperation(tc.query); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("LookupOperation error = %v, want errors.Is(ErrInvalidRecord)", err)
			}
		})
	}
}

func TestStoreNilGuardsAnswerWithoutPanicking(t *testing.T) {
	t.Parallel()
	var store *Store
	if _, _, err := store.LookupOperation(OperationDedupQuery{}); err == nil {
		t.Fatal("LookupOperation on a nil store succeeded")
	}
	if _, ok := store.TerminalOperationSince("h1", 0); ok {
		t.Fatal("TerminalOperationSince on a nil store reported a terminal operation")
	}
	if !store.EffectiveNow().IsZero() {
		t.Fatal("EffectiveNow on a nil store answered a time")
	}
	if _, err := store.ConsumeTokenAndCreateOperation(OperationCreateRequest{}); err == nil {
		t.Fatal("ConsumeTokenAndCreateOperation on a nil store succeeded")
	}
	if _, err := store.CreateOperation(OperationCreateRequest{}); err == nil {
		t.Fatal("CreateOperation on a nil store succeeded")
	}
	if _, err := store.AppendProgress("00000000000000000001", "note"); err == nil {
		t.Fatal("AppendProgress on a nil store succeeded")
	}
	if moved, err := store.InterruptInFlight("shutdown"); moved != 0 || err == nil {
		t.Fatalf("InterruptInFlight on a nil store moved %d with err %v", moved, err)
	}
}

func TestCreateOperationRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	store, err := Open(StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cases := []struct {
		name string
		req  OperationCreateRequest
		want error
	}{
		{
			name: "a kind outside the operation set",
			req:  OperationCreateRequest{ClientOperationID: "op-x", Host: "h1", Kind: Kind("nonsense"), Pair: armPair, BootID: "boot"},
			want: ErrInvalidRecord,
		},
		{
			name: "no client operation id",
			req:  OperationCreateRequest{Host: "h1", Kind: KindRestart, Pair: armPair, BootID: "boot"},
			want: ErrInvalidRecord,
		},
		{
			name: "no host name",
			req:  OperationCreateRequest{ClientOperationID: "op-x", Kind: KindRestart, Pair: armPair, BootID: "boot"},
			want: ErrInvalidRecord,
		},
		{
			name: "an incomplete pinned pair",
			req:  OperationCreateRequest{ClientOperationID: "op-x", Host: "h1", Kind: KindRestart, BootID: "boot"},
			want: ErrInvalidRecord,
		},
		{
			name: "no controller boot id for the fencing epoch",
			req:  OperationCreateRequest{ClientOperationID: "op-x", Host: "h1", Kind: KindRestart, Pair: armPair},
			want: ErrInvalidProbeEpoch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := store.CreateOperation(tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("CreateOperation error = %v, want errors.Is(%v)", err, tc.want)
			}
		})
	}
}

func TestCreateOperationWithoutDedupCreatesAPendingRecord(t *testing.T) {
	t.Parallel()
	store, err := Open(StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	outcome, err := store.CreateOperation(OperationCreateRequest{
		ClientOperationID: "op-restart",
		Host:              "h1",
		Kind:              KindRestart,
		Pair:              armPair,
		BootID:            "boot-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if outcome.Replayed {
		t.Fatal("a fresh create reported itself as a dedup replay")
	}
	if outcome.Record.State != StatePending {
		t.Fatalf("the created record's state = %q, want pending", outcome.Record.State)
	}
	epoch, ok := outcome.Record.FencingEpochValue()
	if !ok || epoch.BootID != "boot-1" || epoch.OpSeq != 1 {
		t.Fatalf("the created record's fencing epoch = %+v (ok %v), want boot-1 at op sequence 1", epoch, ok)
	}
}

func TestCreateOperationRefusesAConcurrentTerminalOperation(t *testing.T) {
	t.Parallel()
	store, err := Open(StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first, err := store.CreateOperation(OperationCreateRequest{
		ClientOperationID: "op-first", Host: "h1", Kind: KindRestart, Pair: armPair, BootID: "boot-1",
	})
	if err != nil {
		t.Fatalf("the first CreateOperation: %v", err)
	}
	if _, err := store.TransitionToState(first.Record.ID, StateComplete, &Result{OK: true, Message: "done"}, ""); err != nil {
		t.Fatalf("the first operation's terminal transition: %v", err)
	}
	// The stale scan position names the terminal transition the write's own
	// locked scan must refuse.
	_, err = store.CreateOperation(OperationCreateRequest{
		ClientOperationID: "op-second", Host: "h1", Kind: KindRestart, Pair: armPair, BootID: "boot-1",
		SequenceBefore: 0,
	})
	var concurrent *ConcurrentTerminalOpError
	if !errors.As(err, &concurrent) {
		t.Fatalf("CreateOperation error = %v, want a *ConcurrentTerminalOpError", err)
	}
	if concurrent.ID != first.Record.ID {
		t.Fatalf("the concurrent refusal names %q, want %q", concurrent.ID, first.Record.ID)
	}
}

func TestAppendProgressRefusesRecordsItCannotNote(t *testing.T) {
	t.Parallel()
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, validArmStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.AppendProgress("00000000000000000002", "  "); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("an empty progress message error = %v, want errors.Is(ErrInvalidRecord)", err)
	}
	if _, err := store.AppendProgress("00000000000000000040", "note"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("an unknown record's progress error = %v, want errors.Is(ErrRecordNotFound)", err)
	}
	if _, err := store.AppendProgress("00000000000000000002", "note"); !errors.Is(err, ErrRecordTerminal) {
		t.Fatalf("a terminal record's progress error = %v, want errors.Is(ErrRecordTerminal)", err)
	}
	oversized := strings.Repeat("x", MaxOperationMessageBytes+1)
	if _, err := store.AppendProgress("00000000000000000001", oversized); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("an oversized progress message error = %v, want errors.Is(ErrInvalidRecord)", err)
	}
}

func TestInterruptInFlightArms(t *testing.T) {
	t.Parallel()

	t.Run("an empty note refuses", func(t *testing.T) {
		t.Parallel()
		store, err := Open(StorePath(t.TempDir()))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if moved, err := store.InterruptInFlight("   "); moved != 0 || !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("InterruptInFlight(\"\") moved %d with err %v", moved, err)
		}
	})

	t.Run("a store of terminal records moves nothing", func(t *testing.T) {
		t.Parallel()
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, validArmStoreJSON)
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		moved, err := store.InterruptInFlight("controller shutdown")
		if err != nil || moved != 0 {
			t.Fatalf("InterruptInFlight over terminal records moved %d with err %v", moved, err)
		}
	})

	t.Run("a write refused before the rename moves nothing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := StorePath(dir)
		if err := os.MkdirAll(strings.TrimSuffix(path, "/"+storeFilename), 0o700); err != nil {
			t.Fatalf("mkdir store dir: %v", err)
		}
		pending := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"running",` +
			`"generation":3,"incarnationId":"inc-h1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
		writeRawStore(t, path, 0o600, pending)
		store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
			return errors.New("disk full before the rename")
		}})
		if err != nil {
			t.Fatalf("openFS: %v", err)
		}
		moved, err := store.InterruptInFlight("controller shutdown")
		if moved != 0 || err == nil {
			t.Fatalf("InterruptInFlight moved %d with err %v, want 0 moved and the refusal", moved, err)
		}
	})

	t.Run("a write whose rename landed reports the count it moved", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := StorePath(dir)
		if err := os.MkdirAll(strings.TrimSuffix(path, "/"+storeFilename), 0o700); err != nil {
			t.Fatalf("mkdir store dir: %v", err)
		}
		pending := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"running",` +
			`"generation":3,"incarnationId":"inc-h1","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}]}`
		writeRawStore(t, path, 0o600, pending)
		var calls int
		store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(_ afero.Fs, _ string) error {
			calls++
			if calls == 3 {
				return errors.New("directory sync refused behind the rename")
			}
			return nil
		}})
		if err != nil {
			t.Fatalf("openFS: %v", err)
		}
		moved, err := store.InterruptInFlight("controller shutdown")
		if moved != 1 || err == nil {
			t.Fatalf("InterruptInFlight moved %d with err %v, want the one record it durably interrupted", moved, err)
		}
		if !RenameLanded(err) {
			t.Fatal("the interruption's write failure did not report its rename landed")
		}
	})
}

func TestRecordWithoutAFencingEpochReadsAsUnbound(t *testing.T) {
	t.Parallel()
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, validArmStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	record, ok := store.Record("00000000000000000001")
	if !ok {
		t.Fatal("the store does not serve its own record")
	}
	if _, bound := record.FencingEpochValue(); bound {
		t.Fatal("a record the store did not mint a fencing epoch for read as bound")
	}
}
