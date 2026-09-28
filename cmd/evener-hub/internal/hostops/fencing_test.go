package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// Tests for §4's fencing-quarantine write: the per-host marker and the
// remote-fencing boundary land on the orphan-unverified record in one atomic
// store write, and nothing lands when the write refuses or fails. The
// BoundaryEntry union itself belongs to the crash-fencing spec; these tests
// pin the store's half — the state pairing, the durable marker, the atomicity.

// remoteFencingBoundaryJSON is the boundary a fencing-timeout quarantine
// carries, in §9's shape: the timed-out epoch, the guard-file epoch, and the
// superseded epoch's lease-tracked entries with their ownership identities.
const remoteFencingBoundaryJSON = `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
	`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"}}]}]`

// runningTestRecord persists one running deploy record for host, under a
// caller-chosen client operation id so a test can hold several per host.
func runningTestRecord(t *testing.T, store *Store, host, clientID string) Record {
	t.Helper()
	record, err := store.Create(NewRecord{
		ClientOperationID: clientID,
		Host:              host,
		Kind:              KindDeploy,
		Generation:        7,
		IncarnationID:     "incarnation-" + clientID,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", host, err)
	}
	running, err := store.Transition(record.ID, StateRunning, nil)
	if err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	return running
}

func TestQuarantineFencingPersistsRecordAndMarkerInOneWrite(t *testing.T) {
	store, path := openTestStore(t)
	record := runningTestRecord(t, store, "h1", "client-h1")
	quarantined, err := store.QuarantineFencing(record.ID, json.RawMessage(remoteFencingBoundaryJSON))
	if err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	if quarantined.State != StateOrphanUnverified {
		t.Fatalf("state = %q, want %q", quarantined.State, StateOrphanUnverified)
	}
	if got := compactJSON(t, quarantined.OrphanBoundary); got != remoteFencingBoundaryJSON {
		t.Fatalf("boundary = %s, want %s", got, remoteFencingBoundaryJSON)
	}
	if quarantined.Sequence != 0 {
		t.Fatalf("orphan-unverified carries sequence %d, want 0 (only resolution advances it)", quarantined.Sequence)
	}
	marker, ok := store.FencingQuarantine("h1")
	if !ok || marker.RecordID != record.ID {
		t.Fatalf("FencingQuarantine(h1) = (%+v, %v), want a marker naming %s", marker, ok, record.ID)
	}
	if _, ok := store.FencingQuarantine("h2"); ok {
		t.Fatal("the marker scoped past its host")
	}
	// The write is durable: a fresh process reads both halves from the file.
	fresh := reopenFresh(t, path)
	stored, ok := fresh.Record(record.ID)
	if !ok || stored.State != StateOrphanUnverified {
		t.Fatalf("reopened record = %+v (ok %v), want orphan-unverified", stored, ok)
	}
	if got := compactJSON(t, stored.OrphanBoundary); got != remoteFencingBoundaryJSON {
		t.Fatalf("reopened boundary = %s, want %s", got, remoteFencingBoundaryJSON)
	}
	if marker, ok := fresh.FencingQuarantine("h1"); !ok || marker.RecordID != record.ID {
		t.Fatalf("reopened marker = (%+v, %v), want it naming %s", marker, ok, record.ID)
	}
	// A replay of the same record is idempotent: the stored record comes back
	// unchanged, never a second transition and never a rewritten boundary.
	replayed, err := fresh.QuarantineFencing(record.ID, json.RawMessage(`[{"kind":"remote-fencing"}]`))
	if err != nil {
		t.Fatalf("QuarantineFencing(replay): %v", err)
	}
	if got := compactJSON(t, replayed.OrphanBoundary); got != remoteFencingBoundaryJSON {
		t.Fatalf("replay rewrote the boundary: %s", got)
	}
}

func TestQuarantineFencingWriteIsAtomic(t *testing.T) {
	path := StorePath(t.TempDir())
	failing := false
	store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
		if failing {
			return errors.New("before-rename fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	record := runningTestRecord(t, store, "h1", "client-h1")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	failing = true
	if _, err := store.QuarantineFencing(record.ID, json.RawMessage(remoteFencingBoundaryJSON)); err == nil {
		t.Fatal("QuarantineFencing whose rename never landed reported success")
	}
	// Neither half landed: the record is still running and no marker exists, in
	// memory and in the file.
	stored, ok := store.Record(record.ID)
	if !ok || stored.State != StateRunning {
		t.Fatalf("record after the failed write = %+v (ok %v), want running", stored, ok)
	}
	if _, ok := store.FencingQuarantine("h1"); ok {
		t.Fatal("a failed write left the marker in memory")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a failed write changed the store file:\nbefore %s\nafter  %s", before, after)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("a failed write left temp files behind: %v", temps)
	}
}

func TestQuarantineFencingRefusals(t *testing.T) {
	store, _ := openTestStore(t)
	record := runningTestRecord(t, store, "h1", "client-h1")
	sibling := runningTestRecord(t, store, "h1", "client-h1-b")
	settled := createTestRecord(t, store, "h2")
	if _, err := store.Transition(settled.ID, StateFailed, func(r *Record) {
		r.Result = &Result{OK: false, Message: "failed"}
	}); err != nil {
		t.Fatalf("Transition(failed): %v", err)
	}
	if _, err := store.QuarantineFencing("00000000000000000009", json.RawMessage(remoteFencingBoundaryJSON)); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("unknown id = %v, want ErrRecordNotFound", err)
	}
	if _, err := store.QuarantineFencing(settled.ID, json.RawMessage(remoteFencingBoundaryJSON)); !errors.Is(err, ErrRecordTerminal) {
		t.Fatalf("terminal record = %v, want ErrRecordTerminal", err)
	}
	for name, boundary := range map[string]string{
		"not an array": `{"kind":"remote-fencing"}`,
		"null":         `null`,
		"empty array":  `[]`,
	} {
		if _, err := store.QuarantineFencing(record.ID, json.RawMessage(boundary)); err == nil {
			t.Fatalf("%s boundary = nil error, want refusal", name)
		}
	}
	if _, err := store.QuarantineFencing(record.ID, json.RawMessage(remoteFencingBoundaryJSON)); err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	// A second record on the same host cannot re-point the open marker.
	if _, err := store.QuarantineFencing(sibling.ID, json.RawMessage(remoteFencingBoundaryJSON)); err == nil {
		t.Fatal("a second quarantine for one host succeeded, want refusal")
	}
	if marker, ok := store.FencingQuarantine("h1"); !ok || marker.RecordID != record.ID {
		t.Fatalf("marker after the refusal = (%+v, %v), want it still naming %s", marker, ok, record.ID)
	}
	if stored, ok := store.Record(sibling.ID); !ok || stored.State != StateRunning {
		t.Fatalf("the refused sibling = %+v (ok %v), want it untouched", stored, ok)
	}
	// A record an earlier pass already left orphan-unverified under a local
	// boundary is not this write's to re-point: the fencing replay must name it.
	local := runningTestRecord(t, store, "h3", "client-h3")
	local, err := store.Transition(local.ID, StateOrphanUnverified, func(r *Record) {
		r.OrphanBoundary = json.RawMessage(`[{"kind":"local-linux","cgroupId":"cg-1","nonce":"n1","pid":1,"startTime":"1"}]`)
	})
	if err != nil {
		t.Fatalf("Transition(orphan-unverified): %v", err)
	}
	if _, err := store.QuarantineFencing(local.ID, json.RawMessage(remoteFencingBoundaryJSON)); err == nil {
		t.Fatal("a local-reap orphan record was re-pointed by the fencing write, want refusal")
	}
	if stored, ok := store.Record(local.ID); !ok || !strings.Contains(string(stored.OrphanBoundary), "local-linux") {
		t.Fatalf("the local-reap record's boundary changed: %+v (ok %v)", stored, ok)
	}
	if _, ok := store.FencingQuarantine("h3"); ok {
		t.Fatal("the refused write left a marker for h3")
	}
}

func TestFencingQuarantineKeyIsOptionalOnRead(t *testing.T) {
	// A store file written before the key existed loads: absent and null both
	// read as "no host quarantined", never as a corrupt store.
	dir := t.TempDir()
	path := StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create store dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(validStoreJSON), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(pre-key store): %v", err)
	}
	if _, ok := store.FencingQuarantine("h1"); ok {
		t.Fatal("a pre-key store reported a quarantine marker")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(validStoreJSON, `"records":[]`, `"records":[],"fencingQuarantines":null`, 1)), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	fresh := reopenFresh(t, path)
	if _, ok := fresh.FencingQuarantine("h1"); ok {
		t.Fatal("a null-marker store reported a quarantine marker")
	}
}
