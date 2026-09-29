package hostops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tombstoneOnlyNameStoreJSON is a corrupt store whose name h10 appears only as a
// retained tombstone: the record that carried it was compacted, and its tombstone
// is the one evidence of the name the custody snapshot must not drop.
const tombstoneOnlyNameStoreJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"compactSeq":1,` +
	`"records":[{"id":"00000000000000000001","clientOperationId":"op-h9","host":"h9","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h9","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` +
	`"tombstones":[{"id":"00000000000000000002","clientOperationId":"op-h10","host":"h10","kind":"restart","state":"interrupted",` +
	`"generation":4,"incarnationId":"inc-h10","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":false,"message":"interrupted"},"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1}]}`

// TestQuarantineKeepsANameWhoseOnlyEvidenceIsATombstone is the round-three H1
// test: ownership derivation includes retained tombstones, so a name compacted
// down to its tombstone still gets its ownership entry (and therefore stays
// closed through the replacement store) instead of vanishing with the record.
func TestQuarantineKeepsANameWhoseOnlyEvidenceIsATombstone(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, tombstoneOnlyNameStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A tombstoned id is not imported under its own id; the name's closure is
	// what must survive.
	var closed bool
	for _, record := range store.Records() {
		if record.Host == "h10" && record.State == StateOrphanUnverified {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("the tombstone-only name h10 lost its ownership entry: %+v", store.Records())
	}
	dir := filepath.Dir(path)
	_, custodyBytes := quarantineArtifact(t, dir, ".custody-")
	var custody struct {
		Ownership []struct {
			Host string `json:"host"`
		} `json:"ownership"`
	}
	if err := json.Unmarshal(custodyBytes, &custody); err != nil {
		t.Fatalf("custody: %v", err)
	}
	var names []string
	for _, entry := range custody.Ownership {
		names = append(names, entry.Host)
	}
	if strings.Join(names, ",") != "h10,h9" {
		t.Fatalf("custody ownership names %v, want both h9 and the tombstone-only h10", names)
	}
}

// TestQuarantineBoundsSyntheticNamesAndIDs is the round-three M1 test: a name
// carried only by the boundary mirror gets a synthetic client operation id
// bounded to the record schema, at the exact length extremes, and a name longer
// than the host bound refuses custody explicitly.
func TestQuarantineBoundsSyntheticNamesAndIDs(t *testing.T) {
	longest := strings.Repeat("n", MaxHostNameBytes)
	mirrorStore := func(name string) string {
		return `{"version":1,"sequence":1,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"op-h9","host":"h9","kind":"deploy","state":"complete",` +
			`"generation":7,"incarnationId":"inc-h9","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
			`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` +
			`"boundaries":{` + jsonString(name) + `:{"generation":5,"incarnationId":"inc-long","presenceEpoch":2}}}`
	}
	t.Run("the longest bounded name gets a bounded synthetic id", func(t *testing.T) {
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, mirrorStore(longest))
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open with a %d-byte mirrored name: %v", len(longest), err)
		}
		var found bool
		for _, record := range store.Records() {
			if record.Host != longest {
				continue
			}
			found = true
			if len(record.ClientOperationID) > MaxClientOperationIDBytes {
				t.Fatalf("the synthetic client operation id is %d bytes, over the %d-byte bound",
					len(record.ClientOperationID), MaxClientOperationIDBytes)
			}
			if record.ClientOperationID != quarantineClientOperationID(longest) {
				t.Fatalf("the synthetic id = %q, want the deterministic quarantineClientOperationID", record.ClientOperationID)
			}
		}
		if !found {
			t.Fatalf("no ownership import for the mirrored name: %+v", store.Records())
		}
	})
	t.Run("a name over the host bound refuses explicitly", func(t *testing.T) {
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, mirrorStore(strings.Repeat("n", MaxHostNameBytes+1)))
		store, err := Open(path)
		if store != nil {
			t.Fatalf("Open accepted a name over the host bound")
		}
		if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("Open = %v, want an incomplete-custody refusal", err)
		}
		if !strings.Contains(err.Error(), "host") {
			t.Fatalf("refusal = %v, want it to name the host bound", err)
		}
	})
}

// TestQuarantineEpochSidecarRefusesAnEmptyCounterAndNeverRegresses is the
// round-three M2 test: a zero/empty sidecar refuses, a missing sidecar is
// rewritten from the custody files' own epochs on a clean boot, and the epoch
// survives even after the custody artifacts are gone.
func TestQuarantineEpochSidecarRefusesAnEmptyCounterAndNeverRegresses(t *testing.T) {
	t.Run("an empty sidecar refuses", func(t *testing.T) {
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, validStoreJSON)
		if err := os.WriteFile(quarantineEpochPath(path), []byte(`{}`), 0o600); err != nil {
			t.Fatalf("WriteFile(sidecar): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with an empty epoch sidecar returned %v, want a refusal", store)
		}
	})
	t.Run("a zero sidecar refuses", func(t *testing.T) {
		path := StorePath(t.TempDir())
		writeRawStore(t, path, 0o600, validStoreJSON)
		if err := os.WriteFile(quarantineEpochPath(path), []byte(`{"quarantineEpoch":0}`), 0o600); err != nil {
			t.Fatalf("WriteFile(sidecar): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open with a zero epoch sidecar returned %v, want a refusal", store)
		}
	})
	t.Run("the epoch is rewritten from custody and survives cleanup", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("Open: %v", err)
		}
		asidePath, _ := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
		custodyPath, _ := quarantineArtifact(t, filepath.Dir(path), ".custody-")
		if err := os.Remove(quarantineEpochPath(path)); err != nil {
			t.Fatalf("Remove(sidecar): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		clean, err := Open(path)
		if err != nil {
			t.Fatalf("clean boot: %v", err)
		}
		if got := clean.CursorEpoch().QuarantineEpoch; got != 1 {
			t.Fatalf("the clean boot reads epoch %d, want 1", got)
		}
		var sidecar struct {
			QuarantineEpoch uint64 `json:"quarantineEpoch"`
		}
		body, err := os.ReadFile(quarantineEpochPath(path))
		if err != nil {
			t.Fatalf("the clean boot did not rewrite the sidecar: %v", err)
		}
		if err := json.Unmarshal(body, &sidecar); err != nil || sidecar.QuarantineEpoch != 1 {
			t.Fatalf("the rewritten sidecar = %s (err %v), want epoch 1", body, err)
		}
		// The operator cleans the quarantine artifacts away; the counter stays.
		if err := os.Remove(asidePath); err != nil {
			t.Fatalf("Remove(aside): %v", err)
		}
		if err := os.Remove(custodyPath); err != nil {
			t.Fatalf("Remove(custody): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		after, err := Open(path)
		if err != nil {
			t.Fatalf("boot after cleanup: %v", err)
		}
		if got := after.CursorEpoch().QuarantineEpoch; got != 1 {
			t.Fatalf("the epoch regressed to %d after the custody artifacts were removed", got)
		}
	})
}

// TestQuarantineNeverRenamesACleanStoreUnderAStaleIntent is the round-three M3
// test: the pending rename runs only while the store path still fails to load as
// corrupt. A clean store serves as-is, and a different corrupt file is
// quarantined under a fresh intent and its own aside.
func TestQuarantineNeverRenamesACleanStoreUnderAStaleIntent(t *testing.T) {
	writeCustodySet := func(t *testing.T, path, stamp string) {
		t.Helper()
		hwm := uint64(1)
		boundary := `[{"kind":"local-markerless","platform":"linux","nonce":"n"}]`
		custody := `{"quarantineEpoch":1,"quarantinedFile":` + jsonString(path) + `,` +
			`"custodiedAt":"2026-09-26T00:00:00Z","recordIds":[{"recordId":"00000000000000000001","host":"hA"}],` +
			`"allocatorHighWaterMark":` + strconv.FormatUint(hwm, 10) + `,` +
			`"fences":[{"recordId":"00000000000000000001","host":"hA","kind":"restart","clientOperationId":"op-hA",` +
			`"generation":3,"incarnationId":"inc-hA","quarantine":false,"boundary":` + boundary + `}],` +
			`"ownership":[{"quarantineRecordId":"00000000000000000001","host":"hA","kind":"restart","clientOperationId":"op-hA",` +
			`"generation":3,"highWaterMark":3,"incarnationId":"inc-hA"}]}`
		if err := os.WriteFile(quarantineCustodyPath(path, stamp), []byte(custody), 0o600); err != nil {
			t.Fatalf("WriteFile(custody): %v", err)
		}
		intent := `{"corruptFile":` + jsonString(path) + `,"custodyFile":` + jsonString(quarantineCustodyPath(path, stamp)) +
			`,"asideFile":` + jsonString(quarantineAsidePath(path, stamp)) +
			`,"quarantinedAt":"2026-09-26T00:00:00Z","quarantineEpoch":1}`
		if err := os.WriteFile(quarantineIntentPath(path), []byte(intent), 0o600); err != nil {
			t.Fatalf("WriteFile(intent): %v", err)
		}
	}
	t.Run("a clean store serves as-is and the intent is cleared", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		// A store written by this build, holding a record the quarantine custody
		// does not know about.
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		created := createTestRecord(t, store, "hClean")
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		stamp := quarantineStamp(time.Now().UTC())
		writeCustodySet(t, path, stamp)

		reopened, err := Open(path)
		if err != nil {
			t.Fatalf("Open under a stale intent with a clean store: %v", err)
		}
		if _, ok := reopened.Record(created.ID); !ok {
			t.Fatalf("the clean store was replaced instead of served: %+v", reopened.Records())
		}
		if _, err := os.Stat(quarantineAsidePath(path, stamp)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the stale intent renamed the clean store aside (stat err = %v)", err)
		}
		if _, err := os.Stat(quarantineIntentPath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the stale intent was not cleared (stat err = %v)", err)
		}
		// The cleared intent must not leave an unreferenced custody file behind:
		// the one-to-one artifact rule would refuse the next boot over it.
		if _, err := os.Stat(quarantineCustodyPath(path, stamp)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the cleared intent left its unreferenced custody file behind (stat err = %v)", err)
		}
		if reopened.Quarantine() != nil {
			t.Fatalf("Quarantine() = %+v after the stale intent's custody was cleared, want nil", reopened.Quarantine())
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		again, err := Open(path)
		if err != nil {
			t.Fatalf("the second fresh open after the stale intent was cleared: %v", err)
		}
		if _, ok := again.Record(created.ID); !ok {
			t.Fatalf("the second fresh open lost the clean store's record: %+v", again.Records())
		}
		if got := again.CursorEpoch().QuarantineEpoch; got != 1 {
			t.Fatalf("the epoch after the clear = %d, want the persisted 1", got)
		}
		if again.Quarantine() != nil {
			t.Fatalf("the second open resurrected a signal for a cleared custody: %+v", again.Quarantine())
		}
	})
	t.Run("a different corrupt file is quarantined under a new intent", func(t *testing.T) {
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("first Open: %v", err)
		}
		firstAside, _ := quarantineArtifact(t, filepath.Dir(path), ".quarantined-")
		// A second corruption lands, and a stale intent from an unrelated
		// quarantine names an aside that is present.
		writeRawStore(t, path, 0o600, corruptSequenceFile(t))
		stamp := quarantineStamp(time.Now().UTC().Add(time.Second))
		writeCustodySet(t, path, stamp)
		if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("stale-aside"), 0o600); err != nil {
			t.Fatalf("WriteFile(stale aside): %v", err)
		}
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		reopened, err := Open(path)
		if err != nil {
			t.Fatalf("Open with a stale intent over a newly corrupt store: %v", err)
		}
		var freshAside bool
		artifactDir := filepath.Dir(path)
		entries, err := os.ReadDir(artifactDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), ".quarantined-") && filepath.Join(artifactDir, entry.Name()) != firstAside {
				freshAside = true
			}
		}
		if !freshAside {
			t.Fatalf("the newly corrupt file was moved aside under the stale intent: %v", entries)
		}
		if got := reopened.CursorEpoch().QuarantineEpoch; got != 2 {
			t.Fatalf("the fresh quarantine's epoch = %d, want 2", got)
		}
	})
}

// TestQuarantineRefusesIncoherentCustodyMetadata is the round-three M4 test:
// ownership high-water marks must cover their generation.
func TestQuarantineRefusesIncoherentCustodyMetadata(t *testing.T) {
	mutate := func(t *testing.T, custody []byte, change func(map[string]json.RawMessage)) []byte {
		t.Helper()
		var top map[string]json.RawMessage
		if err := json.Unmarshal(custody, &top); err != nil {
			t.Fatalf("custody: %v", err)
		}
		change(top)
		body, err := json.Marshal(top)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return body
	}
	dir := t.TempDir()
	path := StorePath(dir)
	writeRawStore(t, path, 0o600, quarantinedStoreWithRemoteFenceJSON)
	if _, err := Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, custodyBytes := quarantineArtifact(t, filepath.Dir(path), ".custody-")

	t.Run("ownership highWaterMark below its generation", func(t *testing.T) {
		body := mutate(t, custodyBytes, func(top map[string]json.RawMessage) {
			var ownership []map[string]json.RawMessage
			if err := json.Unmarshal(top["ownership"], &ownership); err != nil {
				t.Fatalf("ownership: %v", err)
			}
			ownership[0]["highWaterMark"] = json.RawMessage(`1`)
			raw, err := json.Marshal(ownership)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			top["ownership"] = raw
		})
		if _, err := assembleCustodyFromBytes(t, body); !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("assembleCustody = %v, want ErrQuarantineIncomplete for highWaterMark below generation", err)
		}
	})
}

// TestQuarantineRefusesFenceIDsThatAliasAnEarlierCustody is the round-three M5
// test: a fence import whose id an earlier custody file already handed out under
// a different record identity refuses; the same lineage's fence keeps its id.
func TestQuarantineRefusesFenceIDsThatAliasAnEarlierCustody(t *testing.T) {
	newDir := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		path := StorePath(dir)
		writeRawStore(t, path, 0o600, quarantinedStoreJSON)
		if _, err := Open(path); err != nil {
			t.Fatalf("first Open: %v", err)
		}
		return dir, path
	}
	t.Run("a different record identity under an earlier custody id refuses", func(t *testing.T) {
		_, path := newDir(t)
		// The first custody handed id 3 to host h2. This file's fence claims id 3
		// for a different host and incarnation.
		body := `{"version":1,"sequence":1,"allocatorHighWaterMark":5,"records":[` +
			`{"id":"00000000000000000003","clientOperationId":"op-h9","host":"h9","kind":"restart","state":"orphan-unverified",` +
			`"generation":6,"incarnationId":"inc-h9","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"n"}],` +
			`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
			`{"id":"00000000000000000005","clientOperationId":"op-h5","host":"h5","kind":"deploy","state":"complete",` +
			`"generation":7,"incarnationId":"inc-h5","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
			`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`
		writeRawStore(t, path, 0o600, body)
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		if store, err := Open(path); err == nil {
			t.Fatalf("Open accepted a fence id aliasing an earlier custody's import: %+v", store.Records())
		} else if !errors.Is(err, ErrStoreCorrupt) || !errors.Is(err, ErrQuarantineIncomplete) {
			t.Fatalf("Open = %v, want an incomplete-custody refusal", err)
		}
	})
	t.Run("the same lineage's fence keeps its original id", func(t *testing.T) {
		dir, path := newDir(t)
		// The first custody's fence for h1 keeps record id 1; the replacement is
		// corrupted again and its fence is the same record identity.
		body := `{"version":1,"sequence":0,"allocatorHighWaterMark":1,"records":[` +
			`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
			`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
			`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}],` +
			`"tombstones":[{"id":"00000000000000000003","clientOperationId":"q-h2","host":"h2","kind":"restart","state":"interrupted",` +
			`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
			`"hostRemoved":false,"result":{"ok":false,"message":"interrupted"},"compactedAt":"2026-09-26T00:00:00Z","compactedSeq":1}],` +
			`"compactSeq":1,"compactionMarks":[{"seq":1,"hosts":{"h2":"00000000000000000003"}}]}`
		writeRawStore(t, path, 0o600, body)
		if err := forgetStore(path); err != nil {
			t.Fatalf("forgetStore: %v", err)
		}
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open on the same lineage's repeated fence: %v", err)
		}
		if _, ok := store.Record("00000000000000000001"); !ok {
			t.Fatalf("the repeated fence lost its original id: %+v", store.Records())
		}
		if len(dir) == 0 {
			t.Fatal("unreachable")
		}
	})
}

// TestOperationsReadResolvesAHostGenerationOnAQuarantinedStore is the round-three
// M6 test: a host-pinned read naming only a generation resolves against the
// synthesized pair of a quarantined (mirror-less) host instead of refusing.
func TestOperationsReadResolvesAHostGenerationOnAQuarantinedStore(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	generation := uint64(3)
	page, err := store.ReadOperations(OperationsQuery{Host: "h1", Generation: &generation})
	if err != nil {
		t.Fatalf("host+generation read on a quarantined store: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != "00000000000000000001" {
		t.Fatalf("host+generation read = %+v, want the h1 fence import", page.Records)
	}
	if page.Generation == nil || *page.Generation != 3 || page.IncarnationID != "inc-h1" {
		t.Fatalf("the page pair = %v/%q, want the synthesized 3/inc-h1", page.Generation, page.IncarnationID)
	}
	wrong := uint64(9)
	if _, err := store.ReadOperations(OperationsQuery{Host: "h1", Generation: &wrong}); err == nil {
		t.Fatalf("a generation the host never ran under was served")
	}
}
