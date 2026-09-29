package hostops

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
)

// TestQuarantineQuarantinesACorruptStoreWrittenByThisBuild is the round-two H1
// repro: a store file written by this build's own API (records plus a minted
// token row, which carries `runningHealthy` and the rest of the token schema)
// whose corruption is a store-level scalar the custody snapshot does not
// depend on. The custody decode must consume the token data without judging it,
// so the file quarantines — it must not abort as incomplete custody.
func TestQuarantineQuarantinesACorruptStoreWrittenByThisBuild(t *testing.T) {
	store, path := openTestStore(t)
	record := createTestRecord(t, store, "h1")
	if _, err := store.Transition(record.ID, StateComplete, terminalChange(true)); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	mustMint(t, store, mintDefaults("h1", time.Now().UTC()))

	raw := mustReadFile(t, path)
	if !strings.Contains(string(raw), `"tokens":[`) || !strings.Contains(string(raw), `"runningHealthy":true`) {
		t.Fatalf("the fixture store does not carry a token row:\n%s", raw)
	}
	// The corruption is the store-level sequence value, below the stamp the
	// terminal record carries: every record and token row stays valid.
	corrupted := strings.Replace(string(raw), `"sequence":1`, `"sequence":0`, 1)
	if corrupted == string(raw) {
		t.Fatalf("the fixture did not carry the expected sequence value:\n%s", raw)
	}
	writeRawStore(t, path, 0o600, corrupted)
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a corrupt, token-bearing store written by this build: %v, want the custody-first quarantine", err)
	}
	wantQuarantined(t, reopened, path, corrupted)
	var imported bool
	for _, stored := range reopened.Records() {
		if stored.Host == "h1" && stored.State == StateOrphanUnverified {
			imported = true
		}
	}
	if !imported {
		t.Fatalf("the replacement store is missing the custody import for h1: %+v", reopened.Records())
	}
	if _, ok := reopened.Record(record.ID); ok {
		t.Fatalf("the replacement imported a terminal record under its original id: %+v", reopened.Records())
	}
	if tokens := storeSnapshotForTest(reopened).Tokens; len(tokens) != 0 {
		t.Fatalf("the replacement store serves %d tokens, want zero", len(tokens))
	}
}

// TestQuarantineCustodyRecordIDsAreBidirectional is the round-two H2 test: the
// recordIds rows and the import set must be one-to-one. A duplicated row that
// replaces an omitted import, a dropped row, and a duplicated id are each
// incomplete custody.
func TestQuarantineCustodyRecordIDsAreBidirectional(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	if _, err := Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	custodyPath, custodyBytes := quarantineArtifact(t, filepath.Dir(path), ".custody-")
	var custody map[string]json.RawMessage
	if err := json.Unmarshal(custodyBytes, &custody); err != nil {
		t.Fatalf("custody: %v", err)
	}

	rewrite := func(t *testing.T, rows string) []byte {
		t.Helper()
		mutated := maps.Clone(custody)
		mutated["recordIds"] = json.RawMessage(rows)
		body, err := json.Marshal(mutated)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return body
	}
	first, second := `{"recordId":"00000000000000000001","host":"h1"}`, `{"recordId":"00000000000000000003","host":"h2"}`
	cases := map[string]string{
		"a duplicated row replaces an omitted import": "[" + first + "," + first + "]",
		"an import is missing from recordIds":         "[" + first + "]",
		"a row names an id that is not an import":     "[" + first + "," + strings.Replace(second, "00000000000000000003", "00000000000000000005", 1) + "]",
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			body := rewrite(t, rows)
			// The mutated custody stands in for the file the recovery reader
			// would read; the same one-to-one rule governs both.
			if err := afero.WriteFile(afero.NewOsFs(), custodyPath, body, 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := readCustodyFile(afero.NewOsFs(), custodyPath, path); !errors.Is(err, ErrQuarantineIncomplete) {
				t.Fatalf("readCustodyFile(%s) = %v, want ErrQuarantineIncomplete", name, err)
			}
			if _, err := assembleCustodyFromBytes(t, body); !errors.Is(err, ErrQuarantineIncomplete) {
				t.Fatalf("assembleCustody(%s) = %v, want ErrQuarantineIncomplete", name, err)
			}
		})
	}
	// A valid one-to-one custody still reads.
	if err := afero.WriteFile(afero.NewOsFs(), custodyPath, custodyBytes, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := readCustodyFile(afero.NewOsFs(), custodyPath, path); err != nil {
		t.Fatalf("the untampered custody file no longer reads: %v", err)
	}
}

// matureStoreJSON is a store whose record set has legitimate retention gaps:
// one open fence (id 1) and one terminal record (id 9) survive, a tombstone
// accounts for id 8, a retained compaction mark names the smallest id its write
// removed (id 5), and the dropped-marks floor says an older write's marks were
// evicted. The file is corrupt at the store level (its sequence sits below the
// terminal record's stamp), so it is the quarantine path's input.
func matureStoreJSON(withEvidence bool) string {
	evidence := `"compactSeq":0,"tombstones":[],"compactionMarks":[],"compactionFloor":0`
	if withEvidence {
		evidence = `"compactSeq":3,` +
			`"tombstones":[{"id":"00000000000000000008","clientOperationId":"op-h9","host":"h9","kind":"deploy","state":"complete",` +
			`"generation":7,"incarnationId":"inc-h9","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
			`"hostRemoved":false,"result":{"ok":true,"message":"done"},"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":2}],` +
			`"compactionMarks":[{"seq":2,"hosts":{"h9":"00000000000000000005"}}],"compactionFloor":1`
	}
	return `{"version":1,"sequence":1,"allocatorHighWaterMark":9,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
		`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
		`{"id":"00000000000000000009","clientOperationId":"op-h9","host":"h9","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h9","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` + evidence + `}`
}

// TestQuarantineAcceptsMatureGapsWithCompactionEvidence is the round-two H3 test:
// a mature store's retention gaps are covered by its own compaction evidence and
// quarantine; the same shape without evidence, a gap above the highest visible
// id (the spec's truncation), and a floor above compactSeq all refuse.
func TestQuarantineAcceptsMatureGapsWithCompactionEvidence(t *testing.T) {
	t.Run("covered by marks and the dropped-marks floor", func(t *testing.T) {
		path := StorePath(t.TempDir())
		body := matureStoreJSON(true)
		writeRawStore(t, path, 0o600, body)
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open on a mature store with covering compaction evidence: %v, want the custody-first quarantine", err)
		}
		wantQuarantined(t, store, path, body)
		if _, ok := store.Record("00000000000000000001"); !ok {
			t.Fatalf("the replacement is missing the fence import: %+v", store.Records())
		}
	})

	refused := map[string]string{
		"no compaction evidence": matureStoreJSON(false),
		// One id above the highest visible id is §4's truncation that merely
		// omits a record: no removal evidence can cover it.
		"a gap above the highest visible id": strings.Replace(matureStoreJSON(true), `"allocatorHighWaterMark":9`, `"allocatorHighWaterMark":10`, 1),
		"a floor above compactSeq":           strings.Replace(matureStoreJSON(true), `"compactionFloor":1`, `"compactionFloor":4`, 1),
	}
	for name, body := range refused {
		t.Run(name, func(t *testing.T) {
			path := StorePath(t.TempDir())
			writeRawStore(t, path, 0o600, body)
			if store, err := Open(path); err == nil {
				t.Fatalf("Open returned %v for %s, want an incomplete-custody refusal", store, name)
			} else if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
				t.Fatalf("Open = %v, want ErrStoreCorrupt/ErrQuarantineIncomplete", err)
			}
		})
	}
}

// TestQuarantineRebuildUsesTheHighestEpochCustody is the round-two M1 test: the
// rebuild of a vanished replacement selects the custody/aside pair by validated
// quarantine epoch, and only then by filename timestamp, so a backward-moving
// clock cannot make the older quarantine's pair win.
func TestQuarantineRebuildUsesTheHighestEpochCustody(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	write := func(t *testing.T, stamp string, epoch uint64, host, recordID string) {
		t.Helper()
		hwm, err := parseAllocatorID(recordID)
		if err != nil {
			t.Fatalf("parseAllocatorID(%s): %v", recordID, err)
		}
		boundary := `[{"kind":"local-markerless","platform":"linux","nonce":"nonce-` + host + `"}]`
		custody := `{"quarantineEpoch":` + jsonNumber(epoch) + `,"quarantinedFile":` + jsonString(path) + `,` +
			`"custodiedAt":"2026-09-26T00:00:00Z","recordIds":[{"recordId":` + jsonString(recordID) + `,"host":` + jsonString(host) + `}],` +
			`"allocatorHighWaterMark":` + jsonNumber(hwm) + `,` +
			`"fences":[{"recordId":` + jsonString(recordID) + `,"host":` + jsonString(host) + `,"kind":"restart",` +
			`"clientOperationId":` + jsonString("op-"+host) + `,"generation":3,"incarnationId":` + jsonString("inc-"+host) + `,` +
			`"quarantine":false,"boundary":` + boundary + `}],` +
			`"ownership":[{"quarantineRecordId":` + jsonString(recordID) + `,"host":` + jsonString(host) + `,"kind":"restart",` +
			`"clientOperationId":` + jsonString("op-"+host) + `,"generation":3,"highWaterMark":3,"incarnationId":` + jsonString("inc-"+host) + `}]}`
		if err := os.WriteFile(quarantineCustodyPath(path, stamp), []byte(custody), 0o600); err != nil {
			t.Fatalf("WriteFile(custody): %v", err)
		}
		if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("corrupt-"+host), 0o600); err != nil {
			t.Fatalf("WriteFile(aside): %v", err)
		}
	}
	// The later epoch carries the older filename timestamp: a backward-moving
	// clock. The rebuild must still pick epoch 2.
	write(t, "20990101T000000.000000000Z", 1, "h1", "00000000000000000001")
	write(t, "20200101T000000.000000000Z", 2, "h2", "00000000000000000002")
	if err := forgetStore(path); err != nil {
		t.Fatalf("forgetStore: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a directory whose replacement vanished: %v", err)
	}
	if got := store.CursorEpoch().QuarantineEpoch; got != 2 {
		t.Fatalf("the rebuilt store reads epoch %d, want the highest validated epoch 2", got)
	}
	if _, ok := store.Record("00000000000000000002"); !ok {
		t.Fatalf("the rebuild used the older epoch's custody: %+v", store.Records())
	}
	if _, ok := store.Record("00000000000000000001"); ok {
		t.Fatalf("the rebuild imported the older epoch's record: %+v", store.Records())
	}
}

// TestQuarantineRefusesArtifactsWithTrailingData is the round-two L1 test: the
// intent and the epoch sidecar must be exactly one JSON document, matching the
// store decode path's trailing-data check.
func TestQuarantineRefusesArtifactsWithTrailingData(t *testing.T) {
	t.Run("intent", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("Open: %v", err)
		}
		// A complete quarantine artifact set: a valid custody file with its
		// aside, so only the intent's trailing data can refuse the boot.
		stamp := "20260101T000000.000000000Z"
		boundary := `[{"kind":"local-markerless","platform":"linux","nonce":"n"}]`
		custody := `{"quarantineEpoch":1,"quarantinedFile":` + jsonString(path) + `,` +
			`"custodiedAt":"2026-09-26T00:00:00Z","recordIds":[{"recordId":"00000000000000000001","host":"h1"}],` +
			`"allocatorHighWaterMark":1,` +
			`"fences":[{"recordId":"00000000000000000001","host":"h1","kind":"restart","clientOperationId":"op-h1",` +
			`"generation":3,"incarnationId":"inc-h1","quarantine":false,"boundary":` + boundary + `}],` +
			`"ownership":[{"quarantineRecordId":"00000000000000000001","host":"h1","kind":"restart","clientOperationId":"op-h1",` +
			`"generation":3,"highWaterMark":3,"incarnationId":"inc-h1"}]}`
		if err := os.WriteFile(quarantineCustodyPath(path, stamp), []byte(custody), 0o600); err != nil {
			t.Fatalf("WriteFile(custody): %v", err)
		}
		if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte(validStoreJSON), 0o600); err != nil {
			t.Fatalf("WriteFile(aside): %v", err)
		}
		intent := `{"corruptFile":` + jsonString(path) + `,"custodyFile":` + jsonString(quarantineCustodyPath(path, stamp)) +
			`,"asideFile":` + jsonString(quarantineAsidePath(path, stamp)) +
			`,"quarantinedAt":"2026-09-26T00:00:00Z","quarantineEpoch":1}{"extra":1}`
		if err := os.WriteFile(quarantineIntentPath(path), []byte(intent), 0o600); err != nil {
			t.Fatalf("WriteFile(intent): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a trailing-data intent returned %v, want a refusal", store)
		}
	})
	t.Run("epoch sidecar", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, validStoreJSON)
		if err := os.WriteFile(quarantineEpochPath(path), []byte(`{"quarantineEpoch":1}{"extra":1}`), 0o600); err != nil {
			t.Fatalf("WriteFile(sidecar): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a trailing-data sidecar returned %v, want a refusal", store)
		}
	})
}

// assembleCustodyFromBytes decodes a hand-mutated custody document and runs the
// assembly rules over it.
func assembleCustodyFromBytes(t *testing.T, body []byte) (custodyFile, error) {
	t.Helper()
	var custody custodyFile
	if err := json.Unmarshal(body, &custody); err != nil {
		t.Fatalf("Unmarshal(custody): %v", err)
	}
	return assembleCustody(custody)
}

// jsonString and jsonNumber render a Go value the way encoding/json does.
func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func jsonNumber(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
